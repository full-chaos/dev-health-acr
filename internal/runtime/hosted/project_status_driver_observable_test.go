package hosted

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestTheDeployedTelemetryReportsTheProjectStatusDriverDecision(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	telemetry := contextFabricEngineTelemetry(Options{Logger: logger})
	if telemetry == nil {
		t.Fatal("the hosted installation returned no engine telemetry")
	}
	telemetry.RecordProjectStatusDrivers(context.Background(), storage.Principal{OrgID: "org_obs"}, contextfabric.ProjectStatusDriverEvent{
		Emitted: 2, SkippedBudget: 1, SkippedIDClash: 3, Missing: []contextfabric.FactKind{contextfabric.FactWorkload},
	})
	if buf.Len() == 0 {
		t.Fatal("the deployed telemetry emitted nothing for a project status driver decision")
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &rec); err != nil {
		t.Fatalf("captured line is not JSON: %v", err)
	}
	if level, _ := rec["level"].(string); level != "INFO" {
		t.Errorf("level = %q, want INFO", level)
	}
	for key, want := range map[string]float64{"emitted": 2, "skipped_budget": 1, "skipped_id_clash": 3} {
		if got, _ := rec[key].(float64); got != want {
			t.Errorf("%s = %v, want %v", key, rec[key], want)
		}
	}
	missing, _ := rec["missing_kinds"].([]any)
	if len(missing) != 1 || missing[0] != "workload" {
		t.Errorf("missing_kinds = %v, want [workload]", rec["missing_kinds"])
	}
}
