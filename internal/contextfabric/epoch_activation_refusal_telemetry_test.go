package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestSlogEpochActivationRefusedIsAWarnCarryingEveryInput pins the production
// cf_epoch_activation_refused sink: one Warn record per refusal, carrying
// every field the guard decided from, each string attribute through the log
// barrier (a version string is recorded data, not a constant).
func TestSlogEpochActivationRefusedIsAWarnCarryingEveryInput(t *testing.T) {
	var buffer bytes.Buffer
	telemetry := SlogGraphLifecycleTelemetry{Logger: slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	telemetry.RecordEpochActivationRefused(context.Background(), EpochActivationRefusal{
		OrgID: "org-1", Transition: LifecycleTransitionRollback, ActiveEpoch: 4, CandidateEpoch: 3,
		Source: "dev_health_teams_projects", Reason: EpochActivationRefusedSourceVersion,
		RecordedSourceVersion: "devhealthsource.teams_projects.v16\nforged=1", CurrentSourceVersion: "devhealthsource.teams_projects.v17",
	})
	lines := strings.Split(strings.TrimRight(buffer.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("records = %d, want exactly 1:\n%s", len(lines), buffer.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	want := map[string]any{
		"level": "WARN", "msg": "context_fabric: graph epoch activation refused",
		"org_id": "org-1", "transition": "rollback", "active_epoch": float64(4), "candidate_epoch": float64(3),
		"source": "dev_health_teams_projects", "reason": "source_version_mismatch",
		"recorded_source_version": SanitizeLogAttr("devhealthsource.teams_projects.v16\nforged=1"),
		"current_source_version":  "devhealthsource.teams_projects.v17",
	}
	for key, value := range want {
		if record[key] != value {
			t.Errorf("%s = %#v, want %#v", key, record[key], value)
		}
	}
	if strings.Contains(record["recorded_source_version"].(string), "\n") {
		t.Errorf("recorded_source_version reached the log unsanitized: %q", record["recorded_source_version"])
	}
}

func TestSlogEpochBuildAbortedIsAWarnCarryingBothEpochs(t *testing.T) {
	var buffer bytes.Buffer
	telemetry := SlogGraphLifecycleTelemetry{Logger: slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	telemetry.RecordEpochBuildAborted(context.Background(), "org-1\nforged=1", 4, 5)
	lines := strings.Split(strings.TrimRight(buffer.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("records = %d, want exactly 1:\n%s", len(lines), buffer.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	want := map[string]any{
		"level": "WARN", "msg": "context_fabric: graph epoch build aborted",
		"org_id": SanitizeLogAttr("org-1\nforged=1"), "active_epoch": float64(4), "aborted_epoch": float64(5),
	}
	for key, value := range want {
		if record[key] != value {
			t.Errorf("%s = %#v, want %#v", key, record[key], value)
		}
	}
	if strings.Contains(record["org_id"].(string), "\n") {
		t.Errorf("org_id reached the log unsanitized: %q", record["org_id"])
	}
}
