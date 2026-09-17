package contextfabric_test

// Certifies the REAL frame-validation line the real repair producers emit
// -- repairCountKindCollapse and repairCompareGroupedCollapse (both reached
// only through frameRepairTable -> validateProposedFrame) and
// requestedJudgmentForGoals -- against eventspec.FrameValidation, from the
// production slog JSON bytes contextfabric.RunFrameValidationRealProducerScenarioForTest
// wrote. frame_validation_certify_test.go's own sweeps build the event by
// struct literal and prove the declaration and the certifier agree with
// each other; this file proves the real producers stay inside what they
// agreed on, the same split chaos5582_eventspec_certify_test.go already
// applies to the window-continuation-decision line.

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
)

func TestEveryRealFrameValidationProducerScenarioCertifiesAgainstTheDeclaration(t *testing.T) {
	t.Parallel()
	scenarios := contextfabric.FrameValidationRealProducerScenarios()
	if len(scenarios) == 0 {
		t.Fatal("FrameValidationRealProducerScenarios() is empty")
	}
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			raw, orgID := contextfabric.RunFrameValidationRealProducerScenarioForTest(t, scenario)
			log, err := certify.Parse(raw)
			if err != nil {
				t.Fatalf("certify.Parse() on production slog output: %v", err)
			}
			lines := log.LinesWithMsg(eventspec.FrameValidation.Msg)
			if len(lines) != 1 {
				t.Fatalf("frame-validation lines = %d, want exactly 1", len(lines))
			}
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.FrameValidation,
				Want:  map[string]any{"org_id": orgID},
			}); err != nil {
				t.Fatalf("the real emitted line failed certification against eventspec.FrameValidation: %v", err)
			}
			// accepted_judgment is OPEN (composed over goalJudgmentPhrase, not
			// a flat closed set -- see certifyAcceptedJudgmentAgainstAlphabet's
			// own doc comment), so certify.Certify's ClosedVocabulary check
			// never touches it. requestedJudgmentForGoals is one of the two
			// real producers this file exists to reach, so its real output
			// is certified here at the fragment level explicitly.
			if err := certifyAcceptedJudgmentAgainstAlphabet(lines[0]); err != nil {
				t.Fatalf("accepted_judgment failed the alphabet check on a real emitted line: %v", err)
			}
		})
	}
}
