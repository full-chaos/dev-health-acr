package devhealthfacts

import (
	"strings"
	"testing"
)

// TestOwnedRepositoriesSourceGoldenCHAOS7119 pins the fact reads' SQL to the
// exact bytes it rendered before CHAOS-7119 moved the rule into
// ownershipresolve (captured from ad394890's ownedRepositoriesSource). The
// move must not change a single fact read: any drift here is a fact-side
// behavior change riding in on a graph-side ticket.
func TestOwnedRepositoriesSourceGoldenCHAOS7119(t *testing.T) {
	const preMove = `(
	SELECT o.team_id AS team_id,
		coalesce(toString(o.repo_id), toString(r.id)) AS repo_key,
		o.repo_full_name AS repo_full_name
	FROM (
		SELECT org_id, provider, team_id, repo_id, repo_full_name
		FROM team_repo_ownership FINAL
		WHERE org_id = {org_id:String}` + "FILTER" + `
	) AS o
	LEFT JOIN (
		SELECT org_id, provider, id, repo, 1 AS matched
		FROM repos FINAL
		WHERE org_id = {org_id:String}
	) AS r
		ON r.org_id = o.org_id
		   AND r.provider = o.provider
		   AND lower(r.repo) = lower(o.repo_full_name)
	WHERE (o.repo_id IS NOT NULL OR r.matched = 1)
	  AND coalesce(toString(o.repo_id), toString(r.id)) IN (
		  SELECT toString(id) AS id
		  FROM repos FINAL
		  WHERE org_id = {org_id:String}
	  )
)`
	for _, filter := range []string{"", " AND team_id IN {ids:Array(String)}"} {
		want := strings.Replace(preMove, "FILTER", filter, 1)
		if got := ownedRepositoriesSource(filter); got != want {
			t.Fatalf("filter %q: fact-read ownership SQL drifted from the pre-move bytes.\n--- got ---\n%s\n--- want ---\n%s", filter, got, want)
		}
	}
}
