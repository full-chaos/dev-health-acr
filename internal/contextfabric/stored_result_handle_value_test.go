package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A handle is a literal the caller typed or a census offered (a pull request
// number), not a graph node id: a prior result that echoes one must not be
// refused as an absent subject.
func TestStoredResultGateDoesNotLookUpAHandleValueAsANode(t *testing.T) {
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project:p1"}
	for _, principal := range []storage.Principal{
		{OrgID: "org_1", RepositoryScopes: []string{"*"}},
		{OrgID: "org_1", RepositoryScopes: []string{"repo-a"}},
	} {
		result := gateResult(project)
		result.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
			{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedValue: "747"},
		}
		graph := &gateGraph{capturingGraphReader: &capturingGraphReader{}, outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectAdmitted}}
		decision := NewStoredResultGate(graph).Authorize(context.Background(), principal,
			StoredInvestigationResult{Result: result, GrantDigest: StoredResultGrantDigest(principal)}, StoredResultSurfacePriorResult)
		if decision.Decision != StoredResultAdmitted || decision.AbsentCount != 0 {
			t.Fatalf("scope %v: decision=%s reason=%s absent=%d, want admitted with no absent subject", principal.RepositoryScopes, decision.Decision, decision.Reason, decision.AbsentCount)
		}
	}
}
