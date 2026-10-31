package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12229: under a requested project narrowing, "*" on
// authorization_projects is not evidence of membership. A node's stated
// membership (authorization_project_identities, ProjectMembershipOf) decides:
// a member is admitted and proven; a stated non-member is not admitted when
// every requested id resolved; anything else keeps the unverified path
// (admitted, disclosed by ProjectNarrowingUnproven, floored to partial).

const (
	requestedIdentity = "project.v2:linear:proj-1"
	otherIdentity     = "project.v2:linear:proj-2"
)

func narrowedTo(complete bool, identities ...string) contextfabric.RequestedScope {
	return contextfabric.RequestedScope{
		ProjectIDs:       []string{"proj-1"},
		ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: identities, Complete: complete},
	}
}

func wildcardProjectNode(stated interface{}) map[string]interface{} {
	attributes := map[string]interface{}{"authorization_repositories": []string{"acme/api"}, "authorization_projects": "*"}
	if stated != nil {
		attributes[AuthorizationProjectIdentitiesAttr] = stated
	}
	return attributes
}

func TestARequestedProjectNarrowingAdmitsByStatedMembership(t *testing.T) {
	cases := []struct {
		name       string
		scope      contextfabric.RequestedScope
		attributes map[string]interface{}
		admitted   bool
		unproven   bool
	}{
		{"a stated member", narrowedTo(true, requestedIdentity), wildcardProjectNode([]string{requestedIdentity}), true, false},
		{"a stated member, as a graph read returns it", narrowedTo(true, requestedIdentity), wildcardProjectNode([]interface{}{otherIdentity, requestedIdentity}), true, false},
		{"a stated member of another project, every id resolved", narrowedTo(true, requestedIdentity), wildcardProjectNode([]string{otherIdentity}), false, false},
		{"stated in no project, every id resolved", narrowedTo(true, requestedIdentity), wildcardProjectNode([]string{}), false, false},
		{"a stated non-member, a requested id unresolved", narrowedTo(false, requestedIdentity), wildcardProjectNode([]string{otherIdentity}), true, true},
		{"a stated non-member, no requested id resolved", narrowedTo(false), wildcardProjectNode([]string{otherIdentity}), true, true},
		{"unstated membership", narrowedTo(true, requestedIdentity), wildcardProjectNode(nil), true, true},
		{"no resolution ran (today's path)", contextfabric.RequestedScope{ProjectIDs: []string{"proj-1"}}, wildcardProjectNode([]string{otherIdentity}), true, true},
		{"a real project list still decides", narrowedTo(true, requestedIdentity), map[string]interface{}{"authorization_repositories": []string{"acme/api"}, "authorization_projects": []string{"proj-2"}, AuthorizationProjectIdentitiesAttr: []string{requestedIdentity}}, false, false},
		{"the requested raw id on a real project list", narrowedTo(true, requestedIdentity), map[string]interface{}{"authorization_repositories": []string{"acme/api"}, "authorization_projects": []string{"proj-1"}}, true, false},
	}
	principals := map[string]storage.Principal{
		"restricted":   {OrgID: "org_1", RepositoryScopes: []string{"acme/api"}},
		"unrestricted": {OrgID: "org_1"},
	}
	for principalName, principal := range principals {
		for _, tc := range cases {
			if got := AuthorizedAttributes(principal, tc.scope, tc.attributes); got != tc.admitted {
				t.Errorf("%s, %s: AuthorizedAttributes = %v, want %v", principalName, tc.name, got, tc.admitted)
			}
			if !tc.admitted {
				continue
			}
			if got := ProjectNarrowingUnproven(tc.scope, tc.attributes); got != tc.unproven {
				t.Errorf("%s, %s: ProjectNarrowingUnproven = %v, want %v", principalName, tc.name, got, tc.unproven)
			}
		}
	}
}

// The repository grant decides first, as before: stated membership never
// admits a node outside the caller's repositories.
func TestStatedMembershipNeverWidensTheRepositoryGrant(t *testing.T) {
	restricted := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/web"}}
	if AuthorizedAttributes(restricted, narrowedTo(true, requestedIdentity), wildcardProjectNode([]string{requestedIdentity})) {
		t.Fatal("a member node outside the caller's repositories was admitted")
	}
}
