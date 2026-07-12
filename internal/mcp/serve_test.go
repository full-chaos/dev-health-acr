package mcp

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCausedByCallerCancellationWhenCtxCancelledAndErrMatchesReturnsTrue(t *testing.T) {
	// Given a context cancelled by the caller (e.g. SIGINT/SIGTERM via
	// signal.NotifyContext)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When the returned error is exactly that context's cancellation
	got := causedByCallerCancellation(ctx, ctx.Err())

	// Then it is recognised as a caller-driven shutdown
	if !got {
		t.Fatal("expected caller-context cancellation to be recognised as an expected shutdown")
	}
}

func TestCausedByCallerCancellationWhenCtxLiveReturnsFalse(t *testing.T) {
	// Given a context that was never cancelled
	ctx := context.Background()

	// When an unrelated SDK/transport error is returned
	got := causedByCallerCancellation(ctx, errors.New("session ended with error"))

	// Then it is not classified as a caller-driven shutdown, so it still
	// propagates
	if got {
		t.Fatal("expected an unrelated error on a live context to propagate")
	}
}

func TestCausedByCallerCancellationWhenCtxCancelledButErrUnrelatedReturnsFalse(t *testing.T) {
	// Given a context the caller cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When the returned error is a genuine SDK/transport failure, not
	// ctx.Err()
	got := causedByCallerCancellation(ctx, errors.New("transport write failed"))

	// Then the unrelated error still propagates even though the context
	// happens to be cancelled
	if got {
		t.Fatal("expected an unrelated error to propagate even on a cancelled context")
	}
}

// TestServeExitsCleanlyOnSIGTERM launches the real acr-mcp binary,
// completes a live MCP handshake against it (proving Serve is past
// bootstrap and blocked inside Run's ctx.Done()/session select), then
// sends SIGTERM directly to the subprocess and asserts a clean (0) exit --
// the process-level counterpart to causedByCallerCancellation, which locks
// the same decision at the unit level.
func TestServeExitsCleanlyOnSIGTERM(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-spawning E2E test in -short mode")
	}
	binPath := buildACRMCPBinary(t)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONFixture(t, w, http.StatusOK, validCapabilitiesFixture())
	}))
	t.Cleanup(server.Close)

	caPath := filepath.Join(t.TempDir(), "sigterm-ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "serve")
	cmd.Env = append(os.Environ(),
		"ACR_API_URL="+server.URL,
		"ACR_API_CA_BUNDLE="+caPath,
		"ACR_API_TOKEN="+fixtureToken(0x51),
		"ACR_SIDECAR_VERSION=1.0.0",
	)
	var stderr fixtureStderrBuffer
	cmd.Stderr = &stderr

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "sigterm-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect over CommandTransport failed: %v\nstderr: %s", err, stderr.String())
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM: %v", err)
	}
	// session.Close reaps the subprocess through the SDK's own
	// sync.Once-guarded close path, which also fires automatically once
	// the SDK's read loop observes the pipe close from our SIGTERM; calling
	// cmd.Wait directly here would race with that internal goroutine.
	_ = session.Close()
	if cmd.ProcessState == nil {
		t.Fatal("process did not report an exit state after session.Close")
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("expected SIGTERM to exit cleanly (0), got exit code %d\nstderr: %s", code, stderr.String())
	}
}
