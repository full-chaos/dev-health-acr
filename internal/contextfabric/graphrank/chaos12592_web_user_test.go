package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12592: a web user reads the whole organization in the graph. The node
// gate admits a repository-less work item with no native link, a team with no
// ownership row and a project whose list is "*"; the census is not
// grant-scoped. A credential that names repositories keeps every one of those
// refusals, and a requested repository scope still narrows a web user.
func TestAWebUserSeesTheWholeOrganizationInTheGraph(t *testing.T) {
	t.Parallel()
	web := userprincipal.WebUser(t, "org_1")
	bound := userprincipal.Narrowed(t, "org_1", []string{"acme/api"}, []string{"acme/api"})
	nodes := map[string]map[string]interface{}{
		"a repository-less work item": {"subject_kind": string(contextfabric.SubjectWorkItem), "canonical_id": "work_item.v2:0:linear:X-1",
			authorizationRepositoriesAttr: []string{contractsv1.ContextFabricNoRepositorySentinel}},
		"a team with no ownership row": {"subject_kind": string(contextfabric.SubjectTeam), "canonical_id": "team:t-1",
			authorizationRepositoriesAttr: []string{contractsv1.ContextFabricNoTeamOwnershipSentinel}},
		"a project whose list is *": {"subject_kind": string(contextfabric.SubjectProject), "canonical_id": "project.v2:linear:P-1",
			authorizationRepositoriesAttr: "*"},
		"a repository outside the bound grant": {"subject_kind": string(contextfabric.SubjectRepository), "canonical_id": "repository:r-2",
			authorizationRepositoriesAttr: []string{"acme/other"}},
	}
	for name, attributes := range nodes {
		if !AuthorizedAttributes(web, contextfabric.RequestedScope{}, attributes) {
			t.Errorf("%s: refused to a web user", name)
		}
		if AuthorizedAttributes(bound, contextfabric.RequestedScope{}, attributes) {
			t.Errorf("%s: admitted to a repository-bound credential", name)
		}
	}
	if AuthorizedAttributes(web, contextfabric.RequestedScope{RepositorySlugs: []string{"acme/api"}}, nodes["a repository outside the bound grant"]) {
		t.Error("a requested repository scope no longer narrows a web user")
	}
	if grantScopedCensus(web) || !grantScopedCensus(bound) {
		t.Errorf("grant-scoped census: web %t, bound %t; want false, true", grantScopedCensus(web), grantScopedCensus(bound))
	}
}
