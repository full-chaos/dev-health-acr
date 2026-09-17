package contextfabric_test

// Certifies the REAL confirmed-need-ledger line -- driven through each
// subject_anchor-axis scenario's own already-established real producer,
// from contextfabric.RunCaptureAxisFieldRealProducerScenarioForTest --
// against eventspec.ConfirmedNeedLedger, both the claimed field/value pairs
// and (via certify.Certify's own field validation) every OTHER declared
// closed field on the same line. The same split
// confirmed_need_ledger_capture_axis_real_producer_eventspec_certify_test.go
// already applies to the capture-skip-reason family.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
)

func TestEveryRealCaptureAxisFieldProducerCertifiesAgainstTheDeclaration(t *testing.T) {
	t.Parallel()
	scenarios := contextfabric.CaptureAxisFieldRealProducerScenarios()
	if len(scenarios) == 0 {
		t.Fatal("CaptureAxisFieldRealProducerScenarios() is empty")
	}
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			raw, orgID, want := contextfabric.RunCaptureAxisFieldRealProducerScenarioForTest(t, scenario)
			log, err := certify.Parse(raw)
			if err != nil {
				t.Fatalf("certify.Parse() on production slog output: %v", err)
			}
			lines := log.LinesWithMsg(eventspec.ConfirmedNeedLedger.Msg)
			if len(lines) != 1 {
				t.Fatalf("confirmed-need-ledger lines = %d, want exactly 1", len(lines))
			}
			assertWant := map[string]any{"org_id": orgID}
			for key, value := range want {
				assertWant[key] = value
			}
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.ConfirmedNeedLedger,
				Want:  assertWant,
			}); err != nil {
				t.Fatalf("the real emitted line failed certification against eventspec.ConfirmedNeedLedger: %v", err)
			}
		})
	}
}
