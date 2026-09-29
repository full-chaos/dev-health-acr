package directread_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// workItemGraph is one organization graph with one repository the caller is
// granted; enough for the grant listing a restricted handle lookup takes.
type workItemGraph struct{ certifyGraph }

func (workItemGraph) ListSubjectsByKind(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, kind, after string, _ int) (directread.LookupPage, error) {
	if kind != "repository" || after != "" {
		return directread.LookupPage{}, nil
	}
	return directread.LookupPage{Nodes: []directread.LookupNode{{Kind: "repository", CanonicalID: "repository:inside", Label: "acme/inside-repo",
		Attributes: map[string]interface{}{"authorization_repositories": []string{"acme/inside-repo"}}}}}, nil
}
func (workItemGraph) FindSubjectsByExactName(context.Context, storage.Principal, contextfabric.ResolvedGraphBinding, string, []string) (directread.LookupPage, error) {
	return directread.LookupPage{}, nil
}
func (workItemGraph) ReadSubjectNodes(context.Context, storage.Principal, contextfabric.ResolvedGraphBinding, []contextfabric.SubjectRef) ([]directread.LookupNode, error) {
	return nil, nil
}

// Venue re-roll 2 defect (e6fd76be): a repository-restricted handle lookup
// anchors the engine's census on each granted repository, but the census has
// NO repository anchor for work items (Linear work items carry the zero
// repo_id; devhealthsource census registry). The REAL census function
// refused the anchor, and find_subjects answered 503 unavailable on every
// try. The state to reach: the typed refusal invalid_request /
// scope_required (a kind this credential cannot be served by handle), with
// no count, never an outage and never the organization-wide census.
func TestChaos7126_RestrictedWorkItemHandleIsATypedRefusalNotAnOutage(t *testing.T) {
	inside := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:inside"}
	graph := workItemGraph{certifyGraph{nodes: map[string][]graphrank.CandidateNode{
		graphrank.SubjectKey(inside): {{Attributes: map[string]interface{}{"authorization_repositories": []string{"acme/inside-repo"}}}},
	}}}
	gate := directread.NewSubjectGate(graph, nil)
	// The production census over no ClickHouse client: the anchor is refused
	// while the discriminator is built, before any query.
	lookup := directread.NewSubjectLookup(graph, gate, nil).WithOwnershipAndHandles(nil, devhealthsource.NewCensusFunc(nil), graph)
	principal := storage.Principal{OrgID: "org_1", Subject: "u", CredentialID: "c", RepositoryScopes: []string{"acme/inside-repo"}}
	sum := sha256.Sum256([]byte("work-item-handle"))
	ctx := observability.WithRequestID(context.Background(), "req_"+hex.EncodeToString(sum[:16]))
	_, err := lookup.Find(ctx, principal, directread.FindRequest{Handle: "CHAOS-4322"})
	if errors.Is(err, directread.ErrFindUnavailable) {
		t.Fatalf("a restricted work-item handle is an outage (503): %v", err)
	}
	if !errors.Is(err, directread.ErrFindScopeRequired) || !errors.Is(err, directread.ErrFindInvalidRequest) {
		t.Fatalf("want the typed scope_required refusal, got %v", err)
	}
}
