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

// TestEveryLineOfTheRequirementAndBudgetProducersIsADeclaredEvent drives the
// engine scenarios that write the read requirement population, observation
// cover, membership cardinality, count population scope, budget assertion and
// item accounting lines and requires every message written to be the message
// of a declared event.
func TestEveryLineOfTheRequirementAndBudgetProducersIsADeclaredEvent(t *testing.T) {
	t.Parallel()
	events := contextfabric.RequirementLineEventsFromEngineForTest(t)
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	for _, event := range events.Populations {
		sink.RecordReadRequirementPopulation(ctx, principalOrg1, event)
	}
	for _, event := range events.Covers {
		sink.RecordReadRequirementObservationCover(ctx, principalOrg1, event)
	}
	for _, event := range events.Cardinalities {
		sink.RecordMembershipCardinality(ctx, principalOrg1, event)
	}
	for _, event := range events.CountScopes {
		sink.RecordCountPopulationScope(ctx, principalOrg1, event)
	}
	for _, event := range events.Assertions {
		sink.RecordBudgetAssertion(ctx, principalOrg1, event)
	}
	for _, event := range events.Accountings {
		sink.RecordItemAccounting(ctx, principalOrg1, event)
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
	if len(written) < 5 {
		t.Fatalf("the producers wrote %d distinct messages, want the lines of the scenarios: %v (population %d, cover %d, cardinality %d, count scope %d, assertion %d, accounting %d)",
			len(written), keysOf(written), len(events.Populations), len(events.Covers), len(events.Cardinalities), len(events.CountScopes), len(events.Assertions), len(events.Accountings))
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
