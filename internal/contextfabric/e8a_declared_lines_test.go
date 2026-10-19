package contextfabric_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// TestEveryLineOfThePlanAndCohortNarrowingProducersIsADeclaredEvent drives the
// engine scenarios that write the plan narrowing, synthesis retry selection,
// cohort narrowing disclosure and grouped cohort completeness lines, and
// requires every message written to be the message of a declared event.
func TestEveryLineOfThePlanAndCohortNarrowingProducersIsADeclaredEvent(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	plan := contextfabric.PlanEventsFromEngineForTest(t)
	for _, event := range plan.Narrowings {
		sink.RecordPlanNarrowing(ctx, principalOrg1, event)
	}
	for _, event := range plan.Retries {
		sink.RecordSynthesisRetrySelection(ctx, principalOrg1, event)
	}
	for _, event := range plan.Disclosures {
		sink.RecordCohortNarrowingDisclosure(ctx, principalOrg1, event)
	}
	for _, event := range contextfabric.GroupedCohortEventsForTest(t) {
		sink.RecordGroupedCohortCompleteness(ctx, principalOrg1, event)
	}
	declared := map[string]bool{}
	for _, event := range eventspec.All {
		declared[event.Msg] = true
	}
	written, undeclared := map[string]bool{}, map[string]bool{}
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("log line is not JSON: %v: %s", err, line)
		}
		msg, _ := record["msg"].(string)
		written[msg] = true
		if !declared[msg] {
			undeclared[msg] = true
		}
	}
	if len(written) != 4 {
		t.Fatalf("the producers wrote %d distinct messages, want the four events: %v", len(written), keysOf(written))
	}
	names := make([]string, 0, len(undeclared))
	for msg := range undeclared {
		names = append(names, msg)
	}
	sort.Strings(names)
	if len(names) > 0 {
		t.Errorf("%d messages written by the producers are not a declared event: %q", len(names), names)
	}
}

// TestSynthesisRetrySelectionDeclaresItsClosedVocabularies requires the retry
// selection declaration to refuse a family, stage and basis outside
// the producer's vocabularies: the declaration is certified against the same
// values the plan narrowing line is.
func TestSynthesisRetrySelectionDeclaresItsClosedVocabularies(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*contextfabric.PlanNarrowingEvent){
		"family": func(e *contextfabric.PlanNarrowingEvent) { e.Family = "future_family" },
		"stage":  func(e *contextfabric.PlanNarrowingEvent) { e.Stage = "future_stage" },
		"basis":  func(e *contextfabric.PlanNarrowingEvent) { e.Basis = "future_basis" },
	} {
		event := contextfabric.PlanNarrowingEvent{
			Family: contextfabric.QuestionFamilyUnclassified, FamilyVersion: contextfabric.QuestionFamilyTableVersion,
			Stage: "assembled_result", Overrun: "fits", LedgerStatus: "reconciled", QuotaAvailability: contextfabric.ItemQuotaBounded,
		}
		mutate(&event)
		var buf bytes.Buffer
		sink, ctx := answerLineSink(&buf, slog.LevelInfo)
		sink.RecordSynthesisRetrySelection(ctx, principalOrg1, event)
		certifyRefused(t, buf.Bytes(), eventspec.SynthesisRetrySelection, "a retry selection line with an unknown "+name)
	}
}
