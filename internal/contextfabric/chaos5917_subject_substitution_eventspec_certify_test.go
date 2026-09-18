package contextfabric_test

// Certifies the REAL emitted confirmed-need-ledger line, driven through each
// guard outcome's own real producer
// (contextfabric.RunSubjectSubstitutionRealProducerScenarioForTest), against
// eventspec.ConfirmedNeedLedger -- the same split the capture axis already
// applies to capture_skip_reason. Nothing here builds an event by struct
// literal: every line is one the production sink wrote while
// Engine.Investigate ran.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
)

func TestEveryRealSubjectSubstitutionProducerCertifiesAgainstTheDeclaration(t *testing.T) {
	t.Parallel()
	scenarios := contextfabric.SubjectSubstitutionRealProducerScenarios()
	if len(scenarios) != contextfabric.SubjectSubstitutionOutcomeCount {
		t.Fatalf("scenarios = %d, want one per declared outcome (%d)", len(scenarios), contextfabric.SubjectSubstitutionOutcomeCount)
	}
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			raw, orgID := contextfabric.RunSubjectSubstitutionRealProducerScenarioForTest(t, scenario)
			log, err := certify.Parse(raw)
			if err != nil {
				t.Fatalf("certify.Parse() on production slog output: %v", err)
			}
			if len(log.LinesWithMsg(eventspec.ConfirmedNeedLedger.Msg)) == 0 {
				t.Fatalf("confirmed-need-ledger lines = 0, want at least 1")
			}
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.ConfirmedNeedLedger,
				Want:  map[string]any{"org_id": orgID, "substitution_guard": scenario},
			}); err != nil {
				t.Fatalf("the real emitted line failed certification against eventspec.ConfirmedNeedLedger: %v", err)
			}
		})
	}
}
