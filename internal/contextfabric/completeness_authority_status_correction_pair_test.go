package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12460. The deciding pair travels whole or not at all, which is the
// contract's rule (validate_context_fabric_result.go). A blank requirement is a
// budget lever's row, whose identity is blank on purpose.
func TestTheDecidingPairTravelsWholeOrNotAtAll(t *testing.T) {
	const requirement = "state/subject/team"
	narrowed := contractsv1.ContextFabricRequirementNarrowed
	for name, row := range map[string]struct {
		observation     CompletenessAuthorityObservation
		wantRequirement string
		wantOutcome     contractsv1.ContextFabricPlanRequirementOutcome
	}{
		"both":                {CompletenessAuthorityObservation{DecidingRequirement: requirement, DecidingOutcome: narrowed}, requirement, narrowed},
		"a blank requirement": {CompletenessAuthorityObservation{DecidingOutcome: narrowed}, "", ""},
		"a blank outcome":     {CompletenessAuthorityObservation{DecidingRequirement: requirement}, "", ""},
		"neither":             {CompletenessAuthorityObservation{}, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			got := statusCorrectionFor(InvestigationResult{Status: InvestigationDegraded}, InvestigationPartial, row.observation)
			if got == nil {
				t.Fatal("the correction was lost: it stands, whatever decided it")
			}
			if got.ReplacedStatus != InvestigationDegraded || got.Direction != contractsv1.ContextFabricStatusCorrectionDegradedToPartial {
				t.Fatalf("correction = %+v", got)
			}
			if got.DecidingRequirement != row.wantRequirement || got.DecidingOutcome != row.wantOutcome {
				t.Fatalf("pair = %q / %q, want %q / %q", got.DecidingRequirement, got.DecidingOutcome, row.wantRequirement, row.wantOutcome)
			}
		})
	}
}
