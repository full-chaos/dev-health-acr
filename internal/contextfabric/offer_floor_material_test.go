package contextfabric

import (
	"context"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func investigateWithMaterial(t *testing.T, material StructureOfferMaterial) InvestigationResult {
	t.Helper()
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		material:   material,
		context:    emptyGraphContext(),
	}
	interpretation := bootstrapInterpretation()
	interpretation.SubjectTerms = []string{"Phantom"}
	engine := buildWindowGateEngine(t, &countingInterpreter{interpretation: interpretation}, graph, newMapResultStore())
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result
}

func hasSubjectNotFound(result InvestigationResult) bool {
	for _, limitation := range result.Limitations {
		if strings.Contains(limitation, "among the kinds searched") {
			return true
		}
	}
	return false
}

// TestAFloorOutcomeWithARedeemableOfferIsNotTheSubjectNotFoundTerminal: the
// material carries the floor outcome AND an offer the caller can redeem, so
// the terminal must not report "nothing was offered".
func TestAFloorOutcomeWithARedeemableOfferIsNotTheSubjectNotFoundTerminal(t *testing.T) {
	t.Parallel()
	floor := OfferFloorOutcome{Refused: true, SearchedKinds: []string{"team"}}
	withOffer := investigateWithMaterial(t, StructureOfferMaterial{
		SubjectFloor: floor,
		KindOptions:  []contractsv1.ContextFabricKindOption{{Kind: SubjectPullRequest, Label: "a pull request", OfferSource: contractsv1.ContextFabricStructureOfferEngine}},
	})
	if hasSubjectNotFound(withOffer) {
		t.Fatalf("limitations = %q: the subject-not-found sentence was reported although an offer was redeemable", withOffer.Limitations)
	}
	control := investigateWithMaterial(t, StructureOfferMaterial{SubjectFloor: floor})
	if control.Status != InvestigationNoMatch || !hasSubjectNotFound(control) {
		t.Fatalf("control: status=%q limitations=%q, want the floor no_match with the sentence", control.Status, control.Limitations)
	}
}

// TestTheFloorTerminalNamesTheKindsOfTheEffectiveFloor: the kinds in the
// sentence are the material's searched kinds, in order, "none" when empty.
func TestTheFloorTerminalNamesTheKindsOfTheEffectiveFloor(t *testing.T) {
	t.Parallel()
	named := investigateWithMaterial(t, StructureOfferMaterial{SubjectFloor: OfferFloorOutcome{Refused: true, SearchedKinds: []string{"project", "team"}}})
	if !strings.Contains(strings.Join(named.Limitations, "|"), "among the kinds searched (project, team)") {
		t.Fatalf("limitations = %q, want the kinds project, team", named.Limitations)
	}
	none := investigateWithMaterial(t, StructureOfferMaterial{SubjectFloor: OfferFloorOutcome{Refused: true}})
	if !strings.Contains(strings.Join(none.Limitations, "|"), "among the kinds searched (none)") {
		t.Fatalf("limitations = %q, want (none)", none.Limitations)
	}
}
