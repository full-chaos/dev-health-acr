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

// TestEveryLineOfTheEvidenceCoverageAndPeriodDeltaProducersIsADeclaredEvent
// drives the producers of the evidence label fallback, coverage entry cap,
// coverage disclosure, status category composition and period delta lines and
// requires every message they write to be the message of a declared event.
func TestEveryLineOfTheEvidenceCoverageAndPeriodDeltaProducersIsADeclaredEvent(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	contextfabric.E6StatusCategoryCompositionForTest(ctx, sink)
	contextfabric.PeriodDeltaForTest(ctx, sink, "compared")
	served, omitted, fallbacks := contextfabric.CoverageCapAndLabelsForTest()
	sink.RecordCoverageEntriesCapped(ctx, principalOrg1, served, omitted)
	sink.RecordEvidenceLabelFallback(ctx, principalOrg1, fallbacks)
	for _, verdict := range contextfabric.CoverageDisclosureVerdictsForTest() {
		sink.RecordCoverageDisclosurePhrasing(ctx, principalOrg1, verdict.Outcome, verdict.Violation, verdict.Phrased, verdict.Total)
	}
	declared := map[string]bool{}
	for _, event := range eventspec.All {
		declared[event.Msg] = true
	}
	undeclared := map[string]bool{}
	written := 0
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("log line is not JSON: %v: %s", err, line)
		}
		written++
		if msg, _ := record["msg"].(string); !declared[msg] {
			undeclared[msg] = true
		}
	}
	if written < 5 {
		t.Fatalf("the producers wrote %d lines, want every one of the five events", written)
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
