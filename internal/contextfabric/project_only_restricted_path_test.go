package contextfabric

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12299 r3: the work-item tuple dispatch and reuse take the restricted
// path for a project-only caller, whatever its repository list
// (workItemRepositoryRestricted). Other classes decide as before.
func TestAProjectOnlyCallerIsWorkItemRepositoryRestricted(t *testing.T) {
	t.Parallel()
	for _, scopes := range [][]string{nil, {}, {"*"}} {
		if !workItemRepositoryRestricted(storage.Principal{OrgID: "org_1", RepositoryScopes: scopes, ProjectScopes: []string{"project.v2:github:P-1"}}, nil) {
			t.Errorf("%q: a project-only caller is not restricted for the work-item tuple", scopes)
		}
	}
	for _, tc := range []struct {
		scopes    []string
		requested []string
		want      bool
	}{{nil, nil, false}, {[]string{"*"}, nil, false}, {[]string{" * "}, nil, false}, {[]string{"acme/api"}, nil, true}, {nil, []string{"acme/api"}, true}} {
		if got := workItemRepositoryRestricted(storage.Principal{OrgID: "org_1", RepositoryScopes: tc.scopes}, tc.requested); got != tc.want {
			t.Errorf("%q %q: workItemRepositoryRestricted = %v, want %v as before", tc.scopes, tc.requested, got, tc.want)
		}
	}
}

// CHAOS-12299 r3 extra site, work_item_stored_serving.go: a stored work-item
// tuple whose census proof is unavailable is served only on a universal
// grant. A project-only principal's "*" is not one: it is not found.
func TestAProjectOnlyPrincipalIsNotServedAStoredWorkItemTupleOnItsStarList(t *testing.T) {
	t.Parallel()
	for _, scopes := range [][]string{{"*"}, {"org/repo", "*"}} {
		candidate := workItemTuplePayloadFixture(t)
		candidate.Status = InvestigationComplete
		candidate.Coverage.Sources = []SourceObservation{}
		principal := storage.Principal{OrgID: "org-1", RepositoryScopes: scopes, ProjectScopes: []string{"project.v2:github:P-1"}}
		if got := ServeStoredWorkItemTuple(candidate, workItemTupleSemanticStateFixture(), SemanticStateReadAvailable, principal); got.Disposition == WorkItemTupleByIDStored {
			t.Errorf("%q: a project-only principal was served a stored work-item tuple on its star list", scopes)
		}
	}
}
