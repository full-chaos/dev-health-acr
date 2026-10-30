package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-11975: a result carries context_fabric_investigation_result.v2 only
// when it carries at least one membership-verify anchor option. The
// follow-up reconcile removes the anchor option of the remembered subject;
// when that was the material's only (v2) anchor option, the result must not
// stay v2 with no anchor option at all -- the v1-shaped body labelled v2
// that the published schema cannot tell from a real v2 result.
func TestAFollowUpThatRemovesTheOnlyV2AnchorOptionIsNotV2(t *testing.T) {
	response := declaredNoneCohortResponse(SubjectTeam)
	response.material.Missing = []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor}
	response.material.AnchorOptions = []AnchorOption{anchorOptionFor(probeAlpha)}
	response.material.AnchorOptionsRequireV2 = true
	_, _, two := followUpTurns(t, carryTurnOptions{before: declaredNone(SubjectTeam)}, response)
	if line := two.transitions[0]; line.BoundAnchorCarry != BoundAnchorCarryFollowUpOfferedReconciled {
		t.Fatalf("carry = %s, want follow_up_offered_reconciled", line.BoundAnchorCarry)
	}
	anchors := 0
	if two.result.StructureNeeds != nil {
		anchors = len(two.result.StructureNeeds.AnchorOptions)
	}
	if two.result.SchemaVersion == InvestigationResultSchemaV2 && anchors == 0 {
		t.Fatalf("schema_version = %s with %d anchor options: a v2 result must carry a membership-verify anchor option", two.result.SchemaVersion, anchors)
	}
}

// The same material with a second v2 anchor option left after the reconcile
// stays v2: the invariant holds and the promotion is kept.
func TestAFollowUpThatKeepsAV2AnchorOptionStaysV2(t *testing.T) {
	response := declaredNoneCohortResponse(SubjectTeam)
	response.material.Missing = []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor}
	response.material.AnchorOptions = []AnchorOption{anchorOptionFor(probeAlpha), anchorOptionFor(probeBeta)}
	response.material.AnchorOptionsRequireV2 = true
	_, _, two := followUpTurns(t, carryTurnOptions{before: declaredNone(SubjectTeam)}, response)
	if two.result.SchemaVersion != InvestigationResultSchemaV2 || two.result.StructureNeeds == nil || len(two.result.StructureNeeds.AnchorOptions) != 1 {
		t.Fatalf("schema_version = %s, structure_needs = %+v; want v2 with the one remaining anchor option", two.result.SchemaVersion, two.result.StructureNeeds)
	}
}

// withOneV2AnchorOption gives result the one membership-verify anchor option
// a v2 result must carry (CHAOS-11975).
func withOneV2AnchorOption(result InvestigationResult) InvestigationResult {
	result.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{
		Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor},
		AnchorOptions: []contractsv1.ContextFabricAnchorOption{contractsv1.ContextFabricAnchorOptionV2{
			ReceiptID: "ancr_confirm00001", OptionID: "opt_repo_a", Label: "repoA",
			Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository_widget_svc_a",
			MatchedTermHash: "aa11bb22cc33dd44ee55ff66", OfferSource: contractsv1.ContextFabricStructureOfferEngine,
		}.ToV1Wire()},
	}
	return result
}
