package contextfabric

import "testing"

// CHAOS-12601, Codex r2 (executed): a semantic continuation took the derived
// requirements of a snapshot saved before the investment producer declared the
// organization's allocation_breakdown, so the old "no declaring producer" row
// came back. Admission checks the requirement derivation version, which moves
// with that declaration, so such a snapshot is not continued.
func TestASnapshotFromBeforeTheOrganizationObligationIsNotContinued(t *testing.T) {
	t.Parallel()
	stored := StoredInvestigationResult{
		SemanticStateRead: SemanticStateReadAvailable,
		SemanticState: &PersistedSemanticState{
			FamilyTableVersion: QuestionFamilyTableVersion, FrameVersion: QuestionFrameVersion,
			RequirementDerivationVersion: "requirement-derivation.v2", Family: QuestionFamilyInvestmentAllocation,
		},
	}
	plan := &AnswerPlan{Family: QuestionFamilyInvestmentAllocation}
	if got := semanticStateAdmission(stored, plan); got != ContinuationReasonContextVersionMismatch {
		t.Fatalf("admission = %q, want %q: the snapshot was derived before the organization obligation", got, ContinuationReasonContextVersionMismatch)
	}
}
