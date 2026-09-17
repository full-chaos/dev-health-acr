package contextfabric_test

// Certifies the REAL confirmed-need-ledger line -- driven through each
// CaptureSkipReason's own already-established real producer, from
// contextfabric.RunCaptureSkipReasonRealProducerScenarioForTest -- against
// eventspec.ConfirmedNeedLedger. confirmed_need_ledger_certify_test.go's own
// sweep builds the event by struct literal and proves the declaration and
// the certifier agree with each other; this file proves the real capture-
// skip-reason producers stay inside what they agreed on, one per named
// exit, the same split
// confirmed_need_ledger_outcome_real_producer_eventspec_certify_test.go
// already applies to the ledger's own outcome family.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
)

func TestEveryRealCaptureSkipReasonProducerCertifiesAgainstTheDeclaration(t *testing.T) {
	t.Parallel()
	scenarios := contextfabric.CaptureSkipReasonRealProducerScenarios()
	if len(scenarios) == 0 {
		t.Fatal("CaptureSkipReasonRealProducerScenarios() is empty")
	}
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			raw, orgID := contextfabric.RunCaptureSkipReasonRealProducerScenarioForTest(t, scenario)
			log, err := certify.Parse(raw)
			if err != nil {
				t.Fatalf("certify.Parse() on production slog output: %v", err)
			}
			lines := log.LinesWithMsg(eventspec.ConfirmedNeedLedger.Msg)
			if len(lines) == 0 {
				t.Fatalf("confirmed-need-ledger lines = 0, want at least 1")
			}
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.ConfirmedNeedLedger,
				Want:  map[string]any{"org_id": orgID, "capture_skip_reason": scenario},
			}); err != nil {
				t.Fatalf("the real emitted line failed certification against eventspec.ConfirmedNeedLedger: %v", err)
			}
		})
	}
}
