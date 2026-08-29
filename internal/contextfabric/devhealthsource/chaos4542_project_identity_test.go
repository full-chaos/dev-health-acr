package devhealthsource

import (
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-go/readers"
)

// CHAOS-4542. queryProjectTeams -- the producer of every OWNED_BY_TEAM
// project<->team graph edge -- joined `projects` to team_project_ownership
// on project_key. CHAOS-4530 nulls project_key on the UUID-keyed ownership
// rows and leaves real Linear projects' projects.project_key nil by design,
// so that join matches NOTHING for Linear once 4530 deploys: this producer
// would emit zero edges and the graph would silently lose team<->project
// for every Linear project.
//
// These pin the statement, because the fake client returns canned rows
// regardless of the SQL and cannot observe any of it.
func TestChaos4542_ProjectTeamsResolvesOnProjectIdentity(t *testing.T) {
	t.Parallel()
	statement := projectTeamsStatement(cursorState{})

	// 1. The CATALOG expansion, not the subject-filtered one. This producer
	//    paginates the whole org and binds no `ids`; the filtered form fails
	//    with `Code: 456 Substitution 'ids' is not set`, which is exactly
	//    how the first attempt at this change broke.
	if strings.Contains(statement, "{ids:") {
		t.Errorf("the producer references an ids parameter it never binds; a catalog walker must use ProjectIdentityCatalogSQL\n%s", statement)
	}
	if !strings.Contains(statement, "id AS scope") || !strings.Contains(statement, "project_key AS scope") {
		t.Errorf("the identity expansion is missing; both scope rows must be present\n%s", statement)
	}
	// 2. The old key-only join is gone, in both of its parts.
	if strings.Contains(statement, "USING (provider, project_key)") {
		t.Errorf("the producer still joins USING (provider, project_key); that matches nothing for Linear after CHAOS-4530\n%s", statement)
	}
	if strings.Contains(statement, "WHERE o.project_key != ''") {
		t.Errorf("the producer still drops rows whose project_key is empty; every real Linear project is one\n%s", statement)
	}
	// 3. Equality-only ON -- ClickHouse 24.8, which acr's fixtures pin,
	//    rejects an ON containing OR or a function call (Code: 403) under
	//    both analyzer settings.
	for _, onClause := range strings.Split(statement, " ON ")[1:] {
		condition := strings.SplitN(onClause, "\n", 2)[0]
		if strings.Contains(condition, " OR ") || strings.Contains(condition, "has(") {
			t.Errorf("a JOIN ON condition is not a plain equality (%q); the old analyzer rejects it\n%s", condition, statement)
		}
	}
	// 4. The wedge guard: grouped on the RESOLVED projects.id. Grouping on
	//    the source's own project_id lets two rows that resolve to one
	//    project duplicate a RelationshipID -- the batch is then rejected,
	//    a rejected batch never advances a checkpoint, and the org's
	//    projection wedges PERMANENTLY.
	if !strings.Contains(statement, "GROUP BY o.project_id, o.provider, o.team_id, o.source_name") {
		t.Errorf("the producer does not group on the resolved projects.id; two id-space rows for one project would duplicate a RelationshipID and wedge the projection\n%s", statement)
	}
	if !strings.Contains(statement, "org_id = {org_id:String}") {
		t.Errorf("the producer lost its org scoping\n%s", statement)
	}
}

// The keyset condition must move to HAVING with the aggregate, and must be
// the SAME condition -- a second spelling would mean a page boundary that
// skips or replays rows, silently.
func TestChaos4542_KeysetPaginationFollowsTheAggregateIntoHaving(t *testing.T) {
	t.Parallel()
	cursor := cursorState{Since: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC), After: "project:team:native"}
	statement := projectTeamsStatement(cursor)

	if !strings.Contains(statement, " HAVING ") {
		t.Fatalf("the keyset condition is not emitted as HAVING; a WHERE cannot reference max(o.updated_at)\n%s", statement)
	}
	if strings.Contains(statement, "WHERE (max(o.updated_at)") {
		t.Errorf("the keyset condition is still in a WHERE over an aggregate\n%s", statement)
	}
	// Delegation, not duplication: the HAVING body must be exactly
	// sincePredicate's condition with its leading " AND " removed.
	want := strings.TrimPrefix(sincePredicate(cursor, "max(o.updated_at)", projectTeamsRowKey), " AND ")
	if !strings.Contains(statement, "HAVING "+want) {
		t.Errorf("the HAVING body is not sincePredicate's condition; the two spellings have drifted\nwant: %s\n%s", want, statement)
	}
	// ORDER BY must use the same aggregate the predicate does, or the page
	// boundary and the sort disagree.
	if !strings.Contains(statement, "ORDER BY max(o.updated_at) ASC") {
		t.Errorf("ORDER BY does not follow the same aggregate as the keyset predicate\n%s", statement)
	}
}

// The helpers come from dev-health-go, not a fourth local copy of the join.
func TestChaos4542_TheProducerConsumesTheSharedIdentityHelpers(t *testing.T) {
	t.Parallel()
	statement := projectTeamsStatement(cursorState{})
	if !strings.Contains(statement, readers.ProjectIdentityCatalogSQL()) {
		t.Errorf("the producer does not use readers.ProjectIdentityCatalogSQL verbatim; a local copy would drift from the fact readers' definition of project identity\n%s", statement)
	}
	if !strings.Contains(statement, readers.ProjectIdentityMatchSQL("o", "project_ref")) {
		t.Errorf("the producer does not use readers.ProjectIdentityMatchSQL\n%s", statement)
	}
	// The key-to-key arm must be present too. Arm A alone loses an
	// ownership row whose project_id correlates with nothing while its
	// project_key is the only column tying it to a project -- caught live
	// by the "tied assertions resolve deterministically" fixture, which
	// seeds exactly that shape.
	if !strings.Contains(statement, "o.project_key = p.project_key") {
		t.Errorf("the producer dropped the key-to-key arm; ownership rows keyed only by project_key would vanish\n%s", statement)
	}
	if strings.Count(statement, "UNION ALL") < 3 {
		t.Errorf("the two ownership arms are not unioned at row level (want the identity expansion's union plus the arm union)\n%s", statement)
	}
}

// The checkpoint marker MUST move with the join (P1-2).
//
// projectionrun checkpoints are keyed (org_id, source) and compared per
// source, and incremental catch-up under an unchanged marker never revisits
// an already-committed ownership interval -- team_project_ownership's own
// updated_at does not move just because this producer's join did. So for an
// organization already projected under v7 the identity fix would deploy and
// change nothing at all: the edges it holds are the OLD key-only edges,
// which after CHAOS-4530 are the only edges it will ever have.
//
// Pinned as literals on purpose. Every other guard in this file reads the
// producer's own output and would stay green with the marker left at v7 --
// a silent no-op is exactly what this class of bug looks like, and the v3
// entry in TeamsProjectsSourceVersion's doc comment records the last time
// two identity changes shipped under a stale marker unnoticed.
func TestChaos4542_CheckpointMarkerMovedWithTheJoin(t *testing.T) {
	t.Parallel()
	const deployedBefore = "devhealthsource.teams_projects.v7"
	if TeamsProjectsSourceVersion == deployedBefore {
		t.Fatalf("TeamsProjectsSourceVersion = %q: unchanged from the value already in every projected organization's checkpoint row, so this fix is a no-op for all of them", TeamsProjectsSourceVersion)
	}
	if want := "devhealthsource.teams_projects.v8"; TeamsProjectsSourceVersion != want {
		t.Fatalf("TeamsProjectsSourceVersion = %q, want %q -- changing this constant is a deliberate full-rebuild decision, so update this test with the reason in the constant's doc comment", TeamsProjectsSourceVersion, want)
	}
}
