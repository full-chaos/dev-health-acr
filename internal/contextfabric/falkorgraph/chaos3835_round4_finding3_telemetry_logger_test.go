package falkorgraph

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// This file is the CHAOS-3835 codex ROUND-4 review finding-3 proof (P2,
// config.go:239 plus two constructor sites): SlogTelemetry{} (no Logger)
// was being constructed at cmd/acr-projector/runtime.go and
// internal/runtime/hosted/open.go, so every graph signal -- including the
// CHAOS-3835 id-only skip counts, whose entire purpose is being visible at
// the operator's configured level -- fell back to slog.Default(), which
// ignores ACR_LOG_LEVEL and whatever handler each binary's main.go
// actually wires to its output. The AGENTS.md:87-89 "reported, never
// inferred" invariant was satisfied only cosmetically: the signal fired,
// but not through the channel an operator actually configured.

// TestSlogTelemetryUsesTheInjectedLoggerNotDefault is the base-layer
// proof: SlogTelemetry{Logger: X} must emit through X, not slog.Default(),
// for a signal at a level slog.Default()'s handler would show but the
// injected logger's handler is configured to suppress (or vice versa) --
// the only way to prove it's actually the INJECTED logger doing the work,
// not a coincidental default that happens to also write somewhere visible.
func TestSlogTelemetryUsesTheInjectedLoggerNotDefault(t *testing.T) {
	t.Parallel()
	var injected bytes.Buffer
	// The injected logger is configured at Warn -- ABOVE Info -- so an
	// Info-level signal must NOT appear through it, proving output is
	// governed by the injected logger's OWN level configuration rather
	// than being unconditionally written regardless of source.
	logger := slog.New(slog.NewTextHandler(&injected, &slog.HandlerOptions{Level: slog.LevelWarn}))
	telemetry := SlogTelemetry{Logger: logger}

	telemetry.RecordVectorRetrievalSuppressed(context.Background(), "org-1")

	if injected.Len() != 0 {
		t.Fatalf("an Info-level signal must be suppressed by the injected logger's OWN Warn-level handler, got %q", injected.String())
	}

	// Now prove the injected logger IS actually wired (not simply
	// discarding everything): a Warn-level signal must reach it.
	telemetry.RecordVectorRetrievalDegraded(context.Background(), "org-1")
	if !strings.Contains(injected.String(), "org_id=org-1") {
		t.Fatalf("a Warn-level signal must reach the injected logger, got %q", injected.String())
	}
}

// TestSlogTelemetryZeroValueFallsBackToDefault documents the fallback
// SlogTelemetry{} (no Logger) intentionally keeps for callers that
// generally want "just log somewhere" -- the round-4 finding is that
// PRODUCTION callers must never rely on this fallback, not that the
// fallback itself is wrong to have.
func TestSlogTelemetryZeroValueFallsBackToDefault(t *testing.T) {
	t.Parallel()
	telemetry := SlogTelemetry{}
	if telemetry.logger() == nil {
		t.Fatal("SlogTelemetry{}.logger() must never return nil")
	}
}
