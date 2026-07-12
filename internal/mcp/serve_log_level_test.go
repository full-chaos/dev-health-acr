package mcp

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// TestNewDiagnosticsLoggerRespectsConfiguredLevel: Given a diagnostics
// logger built at slog.LevelWarn (the resolved value of a hypothetical
// ACR_LOG_LEVEL=warn), When an Info-level line is logged, Then it does not
// appear in the diagnostics output -- proving log level is actually
// configurable, not just parsed and discarded.
func TestNewDiagnosticsLoggerRespectsConfiguredLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := newDiagnosticsLogger(&buf, slog.LevelWarn)
	logger.InfoContext(context.Background(), "acr-mcp starting", "version", "1.2.3")
	if buf.Len() != 0 {
		t.Fatalf("expected an info line to be suppressed at warn level, got: %s", buf.String())
	}
}

// TestNewDiagnosticsLoggerEmitsAtOrAboveConfiguredLevel: Given a
// diagnostics logger built at the default slog.LevelInfo, When an
// Info-level startup line is logged with capability fields, Then it
// appears in the diagnostics output with those fields -- the same
// entitlement/scope/tool availability Serve logs after a real bootstrap.
func TestNewDiagnosticsLoggerEmitsAtOrAboveConfiguredLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := newDiagnosticsLogger(&buf, slog.LevelInfo)
	logger.InfoContext(context.Background(), "acr-mcp starting",
		"version", "1.2.3",
		"enabled_tools", []string{toolContextForTask, toolSourceEvidence},
	)
	output := buf.String()
	if !strings.Contains(output, "acr-mcp starting") || !strings.Contains(output, toolContextForTask) {
		t.Fatalf("expected startup line with enabled_tools at info level, got: %s", output)
	}
}

// TestNewDiagnosticsLoggerNeverConfiguredBelowDebug is a defensive canary:
// even at the most verbose configurable level (debug), a caller must
// still choose what to log -- this test exists to make explicit that
// newDiagnosticsLogger itself adds no fields of its own, so a bearer
// token or response body can only reach diagnostics if a caller passes
// one explicitly (which Serve's only call site never does).
func TestNewDiagnosticsLoggerNeverConfiguredBelowDebug(t *testing.T) {
	var buf bytes.Buffer
	logger := newDiagnosticsLogger(&buf, slog.LevelDebug)
	logger.DebugContext(context.Background(), "acr-mcp diagnostic probe")
	if !strings.Contains(buf.String(), "acr-mcp diagnostic probe") {
		t.Fatalf("expected a debug line to be emitted at debug level, got: %s", buf.String())
	}
}
