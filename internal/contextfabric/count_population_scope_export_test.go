package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/observability"
)

// RunCountPopulationScopeScenarioForTest is a test-only export: it drives one
// named cell of scopeCells through Engine.Investigate with the PRODUCTION slog
// JSON handler, so the external eventspec certification pin judges the real
// emitted bytes.
func RunCountPopulationScopeScenarioForTest(t *testing.T, scenario string) (log []byte, requestID string) {
	t.Helper()
	for _, cell := range scopeCells() {
		if cell.name != scenario {
			continue
		}
		var buf bytes.Buffer
		telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
		requestID = "req_57750000000000000000000000000001"
		runScopeCell(t, observability.WithRequestID(context.Background(), requestID), newScopeEngine(t, cell, telemetry))
		return buf.Bytes(), requestID
	}
	t.Fatalf("unknown count population scope scenario %q", scenario)
	return nil, ""
}
