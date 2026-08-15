package main

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
)

// TestFalkorGraphTelemetryCarriesThePassedInLogger is the CHAOS-3835
// round-4 finding-3 constructor-site proof: falkorGraphTelemetry (used by
// openProjectionBackend to build falkorConfig.Telemetry) must wire the
// caller's logger into the returned SlogTelemetry, not construct a
// SlogTelemetry{} zero value that would silently fall back to
// slog.Default() -- a DIFFERENT logger than the one ACR_LOG_LEVEL and
// main.go's JSON handler actually configure.
//
// Mutation check: reverting falkorGraphTelemetry to `return
// falkorgraph.SlogTelemetry{}` makes this test fail (Logger would be nil,
// not the passed-in pointer).
func TestFalkorGraphTelemetryCarriesThePassedInLogger(t *testing.T) {
	logger := discardLogger()
	telemetry := falkorGraphTelemetry(logger)

	slogTelemetry, ok := telemetry.(falkorgraph.SlogTelemetry)
	if !ok {
		t.Fatalf("falkorGraphTelemetry returned %T, want falkorgraph.SlogTelemetry", telemetry)
	}
	if slogTelemetry.Logger != logger {
		t.Fatal("falkorGraphTelemetry must wire the passed-in logger, not construct a zero-value SlogTelemetry that falls back to slog.Default()")
	}
}
