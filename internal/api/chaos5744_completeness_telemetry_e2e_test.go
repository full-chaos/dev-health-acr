package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// TestCompletenessTelemetryReachesTheTraceOnARealInvestigateCall drives the
// real HTTP work-item-fresh producer -- the same fixture
// TestWorkItemFreshPartialRowsDoNotBecomeContent uses, real registry, real
// frame derivation, real Engine.Investigate -- with the PRODUCTION
// SlogEngineTelemetry sink wired to a captured buffer instead of discarded,
// and asserts that the completeness-authority, observation-cover and
// plan-narrowing lines all reach the trace carrying the new fields with
// real, non-empty values.
func TestCompletenessTelemetryReachesTheTraceOnARealInvestigateCall(t *testing.T) {
	logs := &bytes.Buffer{}
	f := newFreshTupleProducerFixture(t, "status_partial")
	f.dependencies.Telemetry = contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	engine, err := contextfabric.NewEngine(f.dependencies, f.engineOptions)
	if err != nil {
		t.Fatal(err)
	}
	f.engine = engine

	body := investigationRequestBody()
	body.Question = "What is the state and count of this project's work items?"
	body.TimeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{RelativeID: contextfabric.RelativeWindowTrailing90D}
	result := serveFreshTupleRequest(t, f, body)
	if len(result.Completeness.Outcomes) == 0 {
		t.Fatal("the real investigation produced no outcome rows to found the trace on")
	}

	lines := map[string]map[string]any{}
	for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if raw == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			t.Fatalf("sink emitted undecodable JSON: %v (%s)", err, raw)
		}
		if msg, _ := record["msg"].(string); msg != "" {
			lines[msg] = record
		}
	}

	authority, ok := lines["context fabric completeness authority"]
	if !ok {
		t.Fatal("no completeness-authority line in the trace")
	}
	if authority["deciding_requirement"] == "" || authority["deciding_requirement"] == nil {
		t.Errorf("completeness authority line: deciding_requirement is empty: %v", authority)
	}
	if authority["deciding_outcome"] != "unavailable" {
		t.Errorf("completeness authority line: deciding_outcome = %v, want unavailable (the first unavailable row absorbs)", authority["deciding_outcome"])
	}
	if authority["deciding_requirement"] != "count/member/work_item" {
		t.Errorf("completeness authority line: deciding_requirement = %v, want count/member/work_item (the first unavailable row in the outcome set)", authority["deciding_requirement"])
	}
	if authority["deciding_cause_coverage"] != "fact_pruned" {
		t.Errorf("completeness authority line: deciding_cause_coverage = %v, want fact_pruned", authority["deciding_cause_coverage"])
	}
	if authority["outcome_rows_unavailable"].(float64) == 0 {
		t.Errorf("completeness authority line: outcome_rows_unavailable = %v, want > 0 -- the health/member/work_item row (no_declaring_producer) is also unavailable, absorbed but still counted in the digest", authority["outcome_rows_unavailable"])
	}
	outcomeRowsTotal, _ := authority["outcome_rows_total"].(float64)
	if outcomeRowsTotal == 0 {
		t.Errorf("completeness authority line: outcome_rows_total = %v, want > 0", authority["outcome_rows_total"])
	}
	claimedWork, _ := authority["claimed_facts_work"].(float64)
	if claimedWork == 0 {
		t.Errorf("completeness authority line: claimed_facts_work = %v, want > 0 (the work fact survived the status partial)", authority["claimed_facts_work"])
	}

	cover, ok := lines["context fabric observation cover"]
	if !ok {
		t.Fatal("no observation-cover line in the trace")
	}
	if cover["outcome"] == "" || cover["outcome"] == nil {
		t.Errorf("observation cover line: outcome is empty: %v", cover)
	}
	if cover["failed"].(float64) == 0 {
		t.Errorf("observation cover line: failed = %v, want > 0 -- the status read genuinely failed on this pass", cover["failed"])
	}

	narrowing, ok := lines["context fabric plan narrowing"]
	if !ok {
		t.Fatal("no plan-narrowing line in the trace")
	}
	if narrowing["outcome_completeness_state"] == "" || narrowing["outcome_completeness_state"] == nil {
		t.Errorf("plan narrowing line: outcome_completeness_state is empty on a served answer: %v", narrowing)
	}

	t.Logf("completeness authority line: %v", authority)
	t.Logf("observation cover line: %v", cover)
	t.Logf("plan narrowing line: %v", narrowing)
}
