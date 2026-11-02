package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12592: a web user reads no project reach and no link reach, and a
// repository's walk takes it as unnarrowed when no repository is requested. A
// credential that names repositories keeps every one of those reads and rules.
func TestAWebUserTakesTheUnrestrictedGraphReads(t *testing.T) {
	t.Parallel()
	web := userprincipal.WebUser(t, "org_1")
	bound := userprincipal.Narrowed(t, "org_1", []string{"acme/api"}, []string{"acme/api"})
	if needsProjectReach(web) || !needsProjectReach(bound) {
		t.Fatalf("project reach: web %t, bound %t; want false, true", needsProjectReach(web), needsProjectReach(bound))
	}
	adapter := newFakeAdapter(t, &fakeConn{})
	if _, held := adapter.withLinkReach(context.Background(), "key", web).Value(linkReachContextKey{}).(*linkReach); held {
		t.Fatal("a web user reads the link reach")
	}
	if _, held := adapter.withLinkReach(context.Background(), "key", bound).Value(linkReachContextKey{}).(*linkReach); !held {
		t.Fatal("a repository-bound credential does not read the link reach")
	}
	if (treeWalkState{principal: web}).narrowed() {
		t.Fatal("a web user's walk is narrowed with no repository requested")
	}
	if !(treeWalkState{principal: web, scope: contextfabric.RequestedScope{RepositorySlugs: []string{"acme/api"}}}).narrowed() {
		t.Fatal("a requested repository scope no longer narrows a web user's walk")
	}
	if !(treeWalkState{principal: bound}).narrowed() {
		t.Fatal("a repository-bound credential's walk is not narrowed")
	}
}
