package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// newSlowPollServer answers device authorization immediately and then holds
// every token poll open past the client's per-request timeout, so the poll
// fails with a DeadlineExceeded while the grant itself still has its full
// validated lifetime left.
func newSlowPollServer(t *testing.T, delay time.Duration) (*httptest.Server, func() int) {
	t.Helper()
	fixture := registerLifecycleFixture(t)
	var mu sync.Mutex
	authorizations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/oauth/device_authorization":
			mu.Lock()
			authorizations++
			code := strings.Repeat(string(rune('a'+authorizations-1)), 32)
			mu.Unlock()
			writeLifecycleJSON(t, w, contractsv1.DeviceAuthorizationResponse{SchemaVersion: contractsv1.DeviceAuthorizationResponseSchema, DeviceCode: code, UserCode: "ABCDEFGH", VerificationURI: deviceVerificationURI, ExpiresIn: 600, Interval: 5})
		case "/api/v1/oauth/token":
			// Hold past the client's per-request bound, then answer. The
			// client has already given up; the point is that the failure it
			// sees is a request timeout, not the grant running out.
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
			}
			w.WriteHeader(http.StatusBadRequest)
			writeLifecycleJSON(t, w, contractsv1.OAuthDeviceErrorResponse{SchemaVersion: contractsv1.OAuthDeviceErrorSchema, Error: contractsv1.OAuthDeviceErrorAuthorizationPending})
		default:
			fixture.recordProblem("unexpected slow-poll request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, func() int {
		mu.Lock()
		defer mu.Unlock()
		return authorizations
	}
}

// A per-request timeout and a spent grant both surface as
// context.DeadlineExceeded, and the poll loop classified either as "device
// authorization expired". A single slow response therefore ended login against
// a grant with minutes of life left, and spent none of the restart budget that
// exists for exactly this case.
//
// The grant here is the full validated 600 seconds and never expires; only the
// client's own one-second request bound does.
func TestLoginRestartsRatherThanReportingExpiry_whenOnlyThePerRequestTimeoutElapses(t *testing.T) {
	// Given
	// Three times the client's one-second request bound: long enough that the
	// bound is unambiguously what fires, short enough that the fixture is torn
	// down promptly if a handler outlives the client that gave up on it.
	server, authorizations := newSlowPollServer(t, 3*time.Second)
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TimeoutEnvironment, "1s")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)
	withImmediateDevicePoll(t)

	// When
	code, stderr := captureStderr(t, func() int { return runCLI([]string{"login", "--no-browser"}) })

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("login exit code = %d, want %d after both authorizations timed out", code, lifecycleExitFailure)
	}
	if strings.Contains(stderr, "device authorization expired") {
		t.Fatalf("login stderr = %q, want a slow request reported as unreachable rather than as a spent grant", stderr)
	}
	if !strings.Contains(stderr, "could not reach the server twice") {
		t.Fatalf("login stderr = %q, want the transport exhaustion message", stderr)
	}
	if got := authorizations(); got != maxDeviceAuthorizations {
		t.Fatalf("device authorizations = %d, want %d: a per-request timeout must spend the shared restart", got, maxDeviceAuthorizations)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential persisted after a failed login: %v", err)
	}
}

// waitForDevicePoll is the real seam every other test replaces, so its own
// behavior is asserted directly: it must report the grant context's state
// rather than completing as though the wait had succeeded. A wait that
// returned nil on a finished context would send one more poll against a code
// the flow has already given up on.
func TestWaitForDevicePollReportsTheContextStateRatherThanCompleting(t *testing.T) {
	t.Run("cancelled before the wait", func(t *testing.T) {
		// Given
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// When
		start := time.Now()
		err := waitForDevicePoll(ctx, time.Hour)

		// Then
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waitForDevicePoll = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("waitForDevicePoll took %v on a cancelled context, want an immediate return", elapsed)
		}
	})

	t.Run("cancelled during the wait", func(t *testing.T) {
		// Given
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()
		t.Cleanup(cancel)

		// When
		err := waitForDevicePoll(ctx, time.Hour)

		// Then
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waitForDevicePoll = %v, want context.Canceled", err)
		}
	})

	t.Run("deadline exceeded during the wait", func(t *testing.T) {
		// Given
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		t.Cleanup(cancel)

		// When
		err := waitForDevicePoll(ctx, time.Hour)

		// Then
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("waitForDevicePoll = %v, want context.DeadlineExceeded", err)
		}
	})

	t.Run("interval elapses first", func(t *testing.T) {
		// Given
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		t.Cleanup(cancel)

		// When
		err := waitForDevicePoll(ctx, time.Millisecond)

		// Then
		if err != nil {
			t.Fatalf("waitForDevicePoll = %v, want nil when the interval elapses first", err)
		}
	})
}
