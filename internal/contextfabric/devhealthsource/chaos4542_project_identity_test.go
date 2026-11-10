package devhealthsource

import (
	"strings"
	"testing"
)

// The catalog ambiguity count is a FACT ABOUT THE CATALOG, and both halves of
// that matter.
//
// What it replaced tried to reconstruct which ownership rows had been
// eliminated, from aggregate SQL, and was wrong in four consecutive review
// rounds in both directions -- over-reporting omissions that never happened,
// missing ones that did, doubling a single disagreement, and claiming
// truncation at exactly its limit. That work left with its own ticket. This
// reports only what a plain catalog query can establish.
//
// So the statement must NOT reach for the ownership table or the scope
// expansion: the moment it does, it is making claims about edges again.
func TestChaos4542_CatalogCountMakesNoClaimAboutDroppedEdges(t *testing.T) {
	t.Parallel()
	statement := ambiguousProjectKeysInCatalogStatement
	for _, forbidden := range []string{"team_project_ownership", "p.scope", "scope_kind", "NOT IN"} {
		if strings.Contains(statement, forbidden) {
			t.Errorf("the catalog count references %q -- that makes it a claim about which edges were dropped, which is the reconstruction this replaced", forbidden)
		}
	}
	// Org-scoped and bounded: one tenant's projects must never be counted
	// into another's, and a pathological catalog must not grow this without
	// limit.
	if !strings.Contains(statement, "org_id = {org_id:String}") {
		t.Error("the catalog count must be scoped to the organization")
	}
	if !strings.Contains(statement, "LIMIT {census_limit:UInt32}") {
		t.Error("the catalog count must be bounded")
	}
	// It counts KEYS naming several projects, not projects and not rows.
	if !strings.Contains(statement, "GROUP BY provider, project_key") || !strings.Contains(statement, "HAVING count() > 1") {
		t.Error("the catalog count must group by (provider, project_key) and keep only keys naming more than one project")
	}
	if !strings.Contains(statement, "project_key != ''") {
		t.Error("an empty key names nothing and is never ambiguous; counting it is how a project-level number became a per-match one (defect 8)")
	}
}
