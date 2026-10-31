package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-12231: ProjectNarrowingUnproven is true exactly when a requested
// project narrowing admits a node or edge on the bare "*" project string,
// the one value scopeContainsAttr admits for every requested project.
func TestProjectNarrowingUnprovenOnlyForTheWildcardString(t *testing.T) {
	narrowed := contextfabric.RequestedScope{ProjectIDs: []string{"proj-1"}}
	cases := []struct {
		name      string
		requested contextfabric.RequestedScope
		projects  interface{}
		present   bool
		want      bool
	}{
		{name: "narrowed, wildcard string", requested: narrowed, projects: "*", present: true, want: true},
		{name: "not narrowed, wildcard string", requested: contextfabric.RequestedScope{}, projects: "*", present: true, want: false},
		{name: "narrowed, real project list", requested: narrowed, projects: []string{"proj-1"}, present: true, want: false},
		// A "*" ENTRY in a list is compared byte for byte (exactScopeMatch),
		// so it admits no other project and proves nothing either way.
		{name: "narrowed, list holding a star entry", requested: narrowed, projects: []string{"*"}, present: true, want: false},
		// Any other bare string breaks the attribute convention and is
		// denied by scopeContainsAttr, so it never passes the narrowing.
		{name: "narrowed, other bare string", requested: narrowed, projects: "proj-1", present: true, want: false},
		{name: "narrowed, key absent", requested: narrowed, present: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attributes := map[string]interface{}{}
			if tc.present {
				attributes[authorizationProjectsAttr] = tc.projects
			}
			if got := ProjectNarrowingUnproven(tc.requested, attributes); got != tc.want {
				t.Fatalf("ProjectNarrowingUnproven(%v, %#v) = %v, want %v", tc.requested.ProjectIDs, attributes, got, tc.want)
			}
		})
	}
}
