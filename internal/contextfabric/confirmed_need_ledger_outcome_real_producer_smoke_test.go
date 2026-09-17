package contextfabric

// A same-package smoke test over RunConfirmedNeedLedgerOutcomeRealProducerScenarioForTest:
// the emitted line's own "outcome" key must equal the scenario name, so a
// regression in the scenario wiring itself (not the eventspec declaration)
// fails HERE, close to the fixture, rather than only as an opaque
// certification failure in the sibling _test package.

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestConfirmedNeedLedgerOutcomeRealProducerScenariosEmitTheNamedOutcome(t *testing.T) {
	t.Parallel()
	scenarios := ConfirmedNeedLedgerOutcomeRealProducerScenarios()
	if len(scenarios) != len(confirmedNeedLedgerOutcomes()) {
		t.Fatalf("scenario count = %d, want %d (one per ConfirmedNeedLedgerOutcome)", len(scenarios), len(confirmedNeedLedgerOutcomes()))
	}
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			raw, _ := RunConfirmedNeedLedgerOutcomeRealProducerScenarioForTest(t, scenario)
			var line map[string]any
			for _, rawLine := range bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n")) {
				if len(rawLine) == 0 {
					continue
				}
				var rec map[string]any
				if err := json.Unmarshal(rawLine, &rec); err != nil {
					t.Fatalf("captured line is not JSON: %v -- line: %s", err, rawLine)
				}
				if rec["msg"] == "context fabric confirmed need ledger" {
					line = rec
				}
			}
			if line == nil {
				t.Fatalf("no confirmed need ledger JSON line captured for scenario %q", scenario)
			}
			if got, _ := line["outcome"].(string); got != scenario {
				t.Fatalf("outcome = %q, want %q", got, scenario)
			}
		})
	}
}
