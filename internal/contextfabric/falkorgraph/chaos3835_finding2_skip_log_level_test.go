package falkorgraph

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// This file is the CHAOS-3835 codex review finding-2 proof (P1,
// config.go:221-223 in the pre-fix revision): skip counts were logged at
// Debug when nothing was cleared, invisible at the production default
// ACR_LOG_LEVEL=info, violating internal/contextfabric/AGENTS.md L87-89
// ("skips must be reported, not inferred").

// TestRecordVectorProjectionEmitsAtInfoWhenSkipsAreNonzero is the finding-2
// proof: a batch with cleared==0 but a nonzero skip count must emit at
// Info (an enabled level under a handler configured for Info), not Debug.
//
// Mutation check: reverting the config.go fix (folding the skipped-count
// branch back into the unconditional Debug case) makes this test fail --
// the Info-level handler would produce no output at all. Verified live
// against the pre-fix code.
func TestRecordVectorProjectionEmitsAtInfoWhenSkipsAreNonzero(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	telemetry := SlogTelemetry{Logger: logger}

	telemetry.RecordVectorProjection(context.Background(), "org-1", 5, 0, 0, 3)

	if buf.Len() == 0 {
		t.Fatal("a nonzero skip count with cleared==0 produced no output at Info level -- skips must be reported, not inferred (internal/contextfabric/AGENTS.md L87-89)")
	}
	if !strings.Contains(buf.String(), "skipped_id_only=3") {
		t.Errorf("log output = %q, want it to carry skipped_id_only=3", buf.String())
	}
}

// TestRecordVectorProjectionStaysQuietAtInfoWhenNothingSkippedOrCleared
// pins the OTHER half of finding 2's ruling: an ordinary steady-state batch
// (nothing cleared, nothing skipped) must stay at Debug, so healthy
// operation does not turn into per-batch Info noise.
func TestRecordVectorProjectionStaysQuietAtInfoWhenNothingSkippedOrCleared(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	telemetry := SlogTelemetry{Logger: logger}

	telemetry.RecordVectorProjection(context.Background(), "org-1", 5, 0, 0, 0)

	if buf.Len() != 0 {
		t.Errorf("a zero-skip, zero-cleared batch must stay quiet at Info level, got %q", buf.String())
	}
}
