package contextfabric_test

// Certifies the REAL confirmed-need-ledger line -- driven through a real
// Engine.Investigate call, one per ConfirmedNeedLedgerOutcome, from
// contextfabric.RunConfirmedNeedLedgerOutcomeRealProducerScenarioForTest --
// against eventspec.ConfirmedNeedLedger. confirmed_need_ledger_certify_test.go's
// own sweep builds the event by struct literal and proves the declaration
// and the certifier agree with each other; this file proves the real
// producer (resolveConfirmedNeedLedger) stays inside what they agreed on,
// the same split frame_validation_real_producer_eventspec_certify_test.go
// already established for the frame-validation line.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
)

func TestEveryRealConfirmedNeedLedgerOutcomeCertifiesAgainstTheDeclaration(t *testing.T) {
	t.Parallel()
	scenarios := contextfabric.ConfirmedNeedLedgerOutcomeRealProducerScenarios()
	if len(scenarios) == 0 {
		t.Fatal("ConfirmedNeedLedgerOutcomeRealProducerScenarios() is empty")
	}
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			raw, orgID := contextfabric.RunConfirmedNeedLedgerOutcomeRealProducerScenarioForTest(t, scenario)
			log, err := certify.Parse(raw)
			if err != nil {
				t.Fatalf("certify.Parse() on production slog output: %v", err)
			}
			lines := log.LinesWithMsg(eventspec.ConfirmedNeedLedger.Msg)
			if len(lines) != 1 {
				t.Fatalf("confirmed-need-ledger lines = %d, want exactly 1", len(lines))
			}
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.ConfirmedNeedLedger,
				Want:  map[string]any{"org_id": orgID, "outcome": scenario},
			}); err != nil {
				t.Fatalf("the real emitted line failed certification against eventspec.ConfirmedNeedLedger: %v", err)
			}
		})
	}
}
