package hosted

import (
	"io"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
)

// TestFalkorGraphTelemetryCarriesThePassedInLogger is the CHAOS-3835
// round-4 finding-3 constructor-site proof: falkorGraphTelemetry (used by
// buildContextFabricInvestigator to build graphConfig.Telemetry) must wire
// request.options.Logger into the returned SlogTelemetry, not construct a
// SlogTelemetry{} zero value that would silently fall back to
// slog.Default() -- a DIFFERENT logger than the one this process's
// Options.Logger (and ACR_LOG_LEVEL) actually configure.
//
// Mutation check: reverting falkorGraphTelemetry to `return
// falkorgraph.SlogTelemetry{}` makes this test fail (Logger would be nil,
// not the passed-in pointer).
func TestFalkorGraphTelemetryCarriesThePassedInLogger(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	telemetry := falkorGraphTelemetry(logger)

	slogTelemetry, ok := telemetry.(falkorgraph.SlogTelemetry)
	if !ok {
		t.Fatalf("falkorGraphTelemetry returned %T, want falkorgraph.SlogTelemetry", telemetry)
	}
	if slogTelemetry.Logger != logger {
		t.Fatal("falkorGraphTelemetry must wire the passed-in logger, not construct a zero-value SlogTelemetry that falls back to slog.Default()")
	}
}
