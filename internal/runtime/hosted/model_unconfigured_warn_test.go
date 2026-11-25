package hosted

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// A deployment with no model provider says so once at start-up, so an operator
// does not have to read a per-request 503 to find out.
func TestAnUnconfiguredModelRuntimeIsLoggedOnceAtStartup(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))

	modelRuntime, err := newContextFabricModelRuntime(context.Background(), envLookup(map[string]string{}), nil, logger)

	if err != nil || modelRuntime != nil {
		t.Fatalf("runtime = %#v, err = %v, want nil and nil", modelRuntime, err)
	}
	const message = "context fabric model runtime not configured"
	if count := strings.Count(out.String(), message); count != 1 {
		t.Fatalf("log lines with %q = %d, want 1; log=%q", message, count, out.String())
	}
	if !strings.Contains(out.String(), "level=WARN") {
		t.Fatalf("level is not WARN: %q", out.String())
	}
}
