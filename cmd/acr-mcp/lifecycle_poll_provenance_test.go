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
// the first token poll open past the client's per-request timeout, so that poll
// fails with a DeadlineExceeded while the grant itself still has its full
// validated lifetime left. The second poll succeeds with the given token, and
// the issued credential can be acknowledged.
func newSlowPollServer(t *testing.T, token string) (*httptest.Server, func() int, *lifecycleFixtureState) {
	t.Helper()
	fixture := registerLifecycleFixture(t)
	var mu sync.Mutex
	authorizations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer recordFixturePanic(fixture, w)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/oauth/device_authorization":
			if r.Header.Get("Authorization") != "" {
				fixture.recordProblem("device authorization request unexpectedly had bearer authorization")
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			var request contractsv1.DeviceAuthorizationRequest
			if !decodeStrictLifecycleFixtureRequest(t, fixture, w, r, &request) {
				return
			}
			mu.Lock()
			authorizations++
			code := strings.Repeat(string(rune('a'+authorizations-1)), 32)
			mu.Unlock()
			writeLifecycleJSON(t, w, contractsv1.DeviceAuthorizationResponse{SchemaVersion: contractsv1.DeviceAuthorizationResponseSchema, DeviceCode: code, UserCode: "ABCDEFGH", VerificationURI: deviceVerificationURI, ExpiresIn: 600, Interval: 5})
		case "/api/v1/oauth/token":
			if r.Header.Get("Authorization") != "" {
				fixture.recordProblem("device token request unexpectedly had bearer authorization")
				writeLifecycleFixtureRefusal(t, w, http.StatusUnauthorized)
				return
			}
			var pollRequest contractsv1.DeviceTokenRequest
			if !decodeStrictLifecycleFixtureRequest(t, fixture, w, r, &pollRequest) {
				return
			}
			index, scripted := fixture.nextPoll(2)
			if !scripted {
				w.WriteHeader(http.StatusBadRequest)
				writeLifecycleJSON(t, w, contractsv1.OAuthDeviceErrorResponse{SchemaVersion: contractsv1.OAuthDeviceErrorSchema, Error: contractsv1.OAuthDeviceErrorAccessDenied})
				return
			}
			if index == 0 {
				// Wait for the client to abandon this request. This proves the
				// per-request bound fired without depending on wall-clock scheduling.
				<-r.Context().Done()
				w.WriteHeader(http.StatusBadRequest)
				writeLifecycleJSON(t, w, contractsv1.OAuthDeviceErrorResponse{SchemaVersion: contractsv1.OAuthDeviceErrorSchema, Error: contractsv1.OAuthDeviceErrorAuthorizationPending})
				return
			}
			createdAt := time.Now().UTC().Truncate(time.Second)
			expiresAt := createdAt.Add(30 * 24 * time.Hour)
			writeLifecycleJSON(t, w, contractsv1.DeviceTokenResponse{SchemaVersion: contractsv1.DeviceTokenResponseSchema, AccessToken: token, TokenType: "Bearer", ExpiresIn: 30 * 24 * 60 * 60, Credential: deviceLoginCredential(createdAt, "credential-after-timeout", &expiresAt)})
		case "/api/v1/auth/credentials/self/ack":
			serveLifecycleAck(t, fixture, w, r, token, "credential-after-timeout")
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
	}, fixture
}

// A per-request timeout and a spent grant both surface as
// context.DeadlineExceeded, and the poll loop classified either as "device
// authorization expired". A single slow response therefore ended login against
// a grant with minutes of life left, and spent none of the restart budget that
// exists for exactly this case.
//
// The grant here is the full validated 600 seconds and never expires; only the
// client's own one-second request bound does. The server replaces a credential
// the client never acknowledged, so the right reaction is to poll again, not to
// stop or to restart the authorization.
func TestLoginRetriesThePollAfterALostResponse_whenOnlyThePerRequestTimeoutElapses(t *testing.T) {
	// Given
	token := validDoctorToken(131)
	server, authorizations, state := newSlowPollServer(t, token)
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
	if code != 0 {
		t.Fatalf("login exit code = %d, want 0 after the second poll; stderr=%s", code, stderr)
	}
	if strings.Contains(stderr, "device authorization expired") || strings.Contains(stderr, "may have been redeemed") {
		t.Fatalf("login stderr = %q, want a slow request to be retried silently", stderr)
	}
	if got := authorizations(); got != 1 {
		t.Fatalf("device authorizations = %d, want 1: a lost poll response must not restart", got)
	}
	if _, polls, _, _ := state.counts(); polls != 2 {
		t.Fatalf("device token polls = %d, want 2", polls)
	}
	if got := state.ackCount(); got != 1 {
		t.Fatalf("acknowledgements = %d, want 1", got)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("credential missing after a successful login: %v", err)
	}
	if string(contents) != token+"\n" {
		t.Fatal("persisted credential is not the token from the second poll")
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

// context.Canceled and context.DeadlineExceeded arrive on the same branch, and
// the pre-fix code collapsed both into "device authorization expired". They are
// not the same event and must not report the same cause: a cancelled grant is
// an interrupted session, an expired one is a spent grant, and an operator told
// the wrong one looks in the wrong place.
//
// The grant context is cancelled for real rather than simulated with an error
// value, because the error value is exactly what cannot distinguish the two.
func TestLoginReportsCancellationRatherThanExpiry_whenTheGrantContextIsCancelled(t *testing.T) {
	// Given
	server, authorizations, _ := newSlowPollServer(t, validDoctorToken(132))
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)
	originalGrant := lifecycleGrantContext
	lifecycleGrantContext = func(ctx context.Context, _ int) (context.Context, context.CancelFunc) {
		grantCtx, cancel := context.WithCancel(ctx)
		cancel()
		return grantCtx, cancel
	}
	t.Cleanup(func() { lifecycleGrantContext = originalGrant })

	// When
	code, stderr := captureStderr(t, func() int { return runCLI([]string{"login", "--no-browser"}) })

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("login exit code = %d, want %d for a cancelled grant", code, lifecycleExitFailure)
	}
	if !strings.Contains(stderr, "device authorization was cancelled") {
		t.Fatalf("login stderr = %q, want the cancellation named", stderr)
	}
	if strings.Contains(stderr, "device authorization expired") {
		t.Fatalf("login stderr = %q, want a cancelled grant distinguished from a spent one", stderr)
	}
	// Cancellation is terminal like expiry: the grant is over, so restarting
	// would spend the bounded budget on an authorization that cannot complete.
	if got := authorizations(); got != 1 {
		t.Fatalf("device authorizations = %d, want exactly one; a cancelled grant must not spend the restart budget", got)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential persisted after a cancelled authorization: %v", err)
	}
}
