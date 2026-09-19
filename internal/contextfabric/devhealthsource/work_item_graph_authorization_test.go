package devhealthsource

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The graph projection authorizes a work item from the repository scope it
// stores on the node, not from the SQL work-item relation. A repo-less item
// is projected with the no-repository sentinel only, so on the graph path a
// repository-scoped principal is still denied it even where the SQL readers
// admit it through its project's owned repositories or a native link. This
// pins that the graph path stays fail-closed until it derives its own scope.
func TestRepoLessWorkItemStaysDeniedOnTheGraphPathForARepositoryScopedPrincipal(t *testing.T) {
	t.Parallel()
	scope := workItemAuthorization(zeroRepositoryID, "")
	if len(scope.RepositorySlugs) != 1 || scope.RepositorySlugs[0] != noRepositorySentinel {
		t.Fatalf("repo-less projection scope = %#v, want only the no-repository sentinel", scope)
	}
	attributes := map[string]interface{}{"authorization_repositories": scope.RepositorySlugs}

	scoped := storage.Principal{OrgID: "org", RepositoryScopes: []string{"acme/granted"}}
	if graphrank.AuthorizedAttributes(scoped, contextfabric.RequestedScope{}, attributes) {
		t.Fatal("a repository-scoped principal is authorized for a repo-less work item on the graph path")
	}
	// Control: the same node is visible organization-wide, so the denial
	// above is the scope decision, not an unreadable node.
	if !graphrank.AuthorizedAttributes(storage.Principal{OrgID: "org"}, contextfabric.RequestedScope{}, attributes) {
		t.Fatal("an organization-wide principal is denied a repo-less work item on the graph path")
	}
	// Control: a node that names the granted repository is visible to the
	// scoped principal, so the matcher is live.
	named := map[string]interface{}{"authorization_repositories": workItemAuthorization("30000000-0000-4000-8000-000000000001", "acme/granted").RepositorySlugs}
	if !graphrank.AuthorizedAttributes(scoped, contextfabric.RequestedScope{}, named) {
		t.Fatal("a repository-scoped principal is denied a work item in its granted repository on the graph path")
	}
}
