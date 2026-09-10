package modelprovider

import (
	"bytes"
	"log/slog"
	"testing"
)

// CHAOS-5380 wiring proof, the exact shape CHAOS-4355 used for Telemetry
// (TestRuntimeConfigPropagatesTelemetry, same file family): the per-attempt
// decision line is worthless in production if genkitruntime.Config.Logger
// stays nil, because genkitruntime.New then substitutes slog.Default() --
// and this repository never calls slog.SetDefault, so the line lands on Go's
// own stderr text handler instead of the service's configured, collected
// sink. "A logger interface, a struct field, or a Debug-only line is not
// compliance" (cf-standing-rules 2026-09-06T19:20Z).
func TestRuntimeConfigPropagatesLogger(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	cfg := Config{Provider: "test-provider", Model: "test-model", Logger: logger}

	if got := runtimeConfig(nil, cfg, cfg.Model, nil); got.Logger != logger {
		t.Fatalf("runtimeConfig().Logger = %#v, want the configured logger", got.Logger)
	}
	if got := runtimeConfigWithPhrasing(nil, cfg, cfg.Model, nil); got.Logger != logger {
		t.Fatalf("runtimeConfigWithPhrasing().Logger = %#v, want the configured logger", got.Logger)
	}
}

// TestRuntimeConfigLeavesLoggerNilWhenConfigDoesNotSetIt is the negative
// half, mirroring TestRuntimeConfigLeavesTelemetryNilWhenConfigDoesNotSetIt:
// a caller that sets no logger must keep getting nil, so genkitruntime's own
// documented slog.Default() fallback still applies rather than a
// package-internal default silently substituted here.
func TestRuntimeConfigLeavesLoggerNilWhenConfigDoesNotSetIt(t *testing.T) {
	cfg := Config{Provider: "test-provider", Model: "test-model"}
	if got := runtimeConfig(nil, cfg, cfg.Model, nil); got.Logger != nil {
		t.Fatalf("runtimeConfig().Logger = %#v, want nil", got.Logger)
	}
}
