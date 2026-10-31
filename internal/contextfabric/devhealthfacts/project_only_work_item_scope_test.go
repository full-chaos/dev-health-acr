package devhealthfacts

import (
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// CHAOS-12299 r1 F2: a project-only principal's work-item grant is the zero
// selector set, never the organization-wide one, whatever its repository list
// holds. Every work-item reader renders it exactly as it renders a grant that
// names no valid repository, which denies every row, so a member work item's
// blocker or required child outside the grant is never read and its id never
// served. That is the codex r1 repro: with "all" a member's blocker to a
// non-member was served.
func TestAProjectOnlyWorkItemGrantDeniesEveryWorkItem(t *testing.T) {
	t.Parallel()
	deny := workItemRepositoryAuthorization(storage.Principal{OrgID: "org_1", RepositoryScopes: []string{" "}}, nil)
	for _, scopes := range [][]string{nil, {}, {"*"}, {"acme/api"}} {
		principal := storage.Principal{OrgID: "org_1", RepositoryScopes: scopes, ProjectScopes: []string{"project.v2:github:P-1"}}
		scope := workItemRepositoryAuthorization(principal, nil)
		if granted := scope.RepositorySelectors.Granted; granted.All || len(granted.ExactSlugs) != 0 || len(granted.Owners) != 0 {
			t.Errorf("%q: granted = %+v, want the zero set", scopes, granted)
		}
		if organizationWideAuthorization(scope) {
			t.Errorf("%q: the organization-wide fast path took a project-only grant", scopes)
		}
		got, want := readers.WorkItemScopeSQL(scope), readers.WorkItemScopeSQL(deny)
		if got.AuthorizationExpr != want.AuthorizationExpr || !reflect.DeepEqual(got.Bindings, want.Bindings) {
			t.Errorf("%q: the rendered grant differs from a deny grant:\n%s\n%v\nwant\n%s\n%v", scopes, got.AuthorizationExpr, got.Bindings, want.AuthorizationExpr, want.Bindings)
		}
	}
	if !organizationWideAuthorization(workItemRepositoryAuthorization(storage.Principal{OrgID: "org_1"}, nil)) {
		t.Error("an unrestricted principal no longer takes the organization-wide grant")
	}
}
