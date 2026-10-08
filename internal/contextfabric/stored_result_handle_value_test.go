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
		decision := NewStoredResultGate(graph).WithHandleLiteralMatcher(func(value string) bool { return value == "747" }).Authorize(context.Background(), principal,
			StoredInvestigationResult{Result: result, GrantDigest: StoredResultGrantDigest(principal)}, StoredResultSurfacePriorResult)
		if decision.Decision != StoredResultAdmitted || decision.AbsentCount != 0 {
			t.Fatalf("scope %v: decision=%s reason=%s absent=%d, want admitted with no absent subject", principal.RepositoryScopes, decision.Decision, decision.Reason, decision.AbsentCount)
		}
	}
}

// Every structure member is classified: a member whose applied value is a
// canonical id (anchor and candidate options carry opt.CanonicalID) reaches the
// graph decision; a handle's value does too unless it is a handle literal
// (StoredResultGate.subjectsOf); kind and window values are literals.
func TestStoredSubjectStructureMembersCoverTheVocabulary(t *testing.T) {
	canonical := map[contractsv1.ContextFabricStructureNeedKind]bool{
		contractsv1.ContextFabricStructureNeedSubjectAnchor:    true,
		contractsv1.ContextFabricStructureNeedSubjectCandidate: true,
		contractsv1.ContextFabricStructureNeedExpectedKind:     false,
		contractsv1.ContextFabricStructureNeedSubjectHandle:    true,
		contractsv1.ContextFabricStructureNeedWindow:           false,
	}
	for _, member := range contractsv1.ContextFabricStructureNeedKindVocabulary() {
		want, classified := canonical[member]
		if !classified {
			t.Fatalf("structure member %q is not classified: decide whether its applied value is a canonical id", member)
		}
		if storedSubjectStructureMembers[member] != want {
			t.Errorf("member %q: reaches the graph decision = %v, want %v", member, storedSubjectStructureMembers[member], want)
		}
	}
}
