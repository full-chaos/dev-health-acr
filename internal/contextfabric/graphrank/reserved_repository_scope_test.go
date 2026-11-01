package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12298 (c2): a reserved repository scope is never a match. Without the
// explicit denial, exactScopeMatch's fallback (either side fails to parse as a
// slug, so compare raw) admits a node whose repository list holds the same
// reserved value as the caller's.
func TestScopeMatchDeniesAReservedRepositoryScope(t *testing.T) {
	const reserved = "acr-reserved:project-only"
	for _, kind := range []ScopeValueKind{ScopeValueRepositoryName, ScopeValueIdentifier} {
		if ScopeMatch([]string{reserved}, reserved, kind) {
			t.Errorf("kind %d: a reserved scope matched itself", kind)
		}
		if ScopeMatch([]string{"acr-reserved:project-only/x"}, "acr-reserved:project-only/*", kind) {
			t.Errorf("kind %d: a reserved owner wildcard matched", kind)
		}
	}
	// Unchanged: the existing node sentinels still match only themselves on the
	// raw fallback, and real slugs still match.
	if !ScopeMatch([]string{"acme/api"}, "ACME/API", ScopeValueRepositoryName) {
		t.Error("a real slug no longer matches")
	}
}

// The node rule for a restricted caller: a principal whose repository list
// holds the reserved value (and no project grant) is never admitted to a node
// that carries it.
func TestAReservedRepositoryScopeAdmitsNoNode(t *testing.T) {
	const reserved = "acr-reserved:project-only"
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{reserved}}
	node := map[string]interface{}{
		"subject_kind":               string(contextfabric.SubjectRepository),
		"authorization_repositories": []string{reserved},
	}
	if AuthorizedAttributes(principal, contextfabric.RequestedScope{}, node) {
		t.Fatal("a reserved repository scope admitted a node carrying it")
	}
}
