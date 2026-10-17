package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
)

func TestProjectorStatementCapPrefersTheOverride(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	if got := projectorStatementCap(config.ProjectorConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil))); got != projectorStatementMaxMemoryUsage {
		t.Fatalf("cap without an override = %d, want the default %d", got, projectorStatementMaxMemoryUsage)
	}
	got := projectorStatementCap(config.ProjectorConfig{StatementMaxMemoryBytes: 4 << 30, StatementMaxMemoryBytesSet: true}, logger)
	if got != 4<<30 {
		t.Fatalf("cap with the override = %d, want %d", got, uint64(4<<30))
	}
	if !strings.Contains(logs.String(), "max_memory_usage_bytes=4294967296") || !strings.Contains(logs.String(), "source="+config.EnvProjectorStatementMaxMemory) {
		t.Fatalf("effective cap not logged with its source: %q", logs.String())
	}
}
