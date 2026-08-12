package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestOpenRuntimeStaysDisabledWhenProjectionIsNotEnabled(t *testing.T) {
	cfg, err := config.LoadProjector()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := openRuntime(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	if runtime.Coordinator != nil {
		t.Fatal("coordinator must not start when projection is disabled")
	}
	if len(runtime.Checks) != 0 {
		t.Fatal("a disabled runtime must not report readiness checks that were never wired")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestOpenRuntimeStaysDisabledWhenBackingStoresAreUnconfigured(t *testing.T) {
	cfg, err := config.LoadProjector()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProjectionEnabled = true // Postgres/ClickHouse DSNs remain unset
	runtime, err := openRuntime(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	if runtime.Coordinator != nil {
		t.Fatal("coordinator must not start without Postgres/ClickHouse configured")
	}
}
