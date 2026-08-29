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
	//
	// RESPELLED by defect 6, not relaxed: the arm now matches the key SCOPE
	// ROW (o.project_key = p.scope, scope_kind = 'key') rather than
	// p.project_key, which every scope row carries. The assertion keeps its
	// original job -- this arm has been dropped three separate times -- and
	// tracks the spelling that is now load-bearing.
	if !strings.Contains(statement, "o.project_key = p.scope") {
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

// CHAOS-4542 defect 6, acr side. The graph producer's key arm must select the
// KEY SCOPE ROW, never p.project_key -- a column every scope row carries,
// which is how an id row satisfied a key-shaped guard and two projects
// sharing a key both matched an ownership row that named neither.
//
// The scope arm must carry NO kind restriction: it compares project_id,
// which is whichever id space that row uses, and today's GitLab rows hold a
// project KEY there. Restricting it reads like tightening and drops them.
func TestChaos4542_KeyArmSelectsTheKeyScopeRowAndTheScopeArmDoesNot(t *testing.T) {
	t.Parallel()
	statement := projectTeamsStatement(cursorState{})
	if strings.Contains(statement, "o.project_key = p.project_key") {
		t.Error("the key arm joins p.project_key, which EVERY scope row carries -- an id row with the same key matches, which is defect 6")
	}
	if !strings.Contains(statement, "o.project_key = p.scope") || !strings.Contains(statement, "WHERE p.scope_kind = 'key'") {
		t.Error("the key arm must match the key scope row and name scope_kind = 'key'")
	}
	if strings.Contains(statement, "p.key_resolution_count") {
		t.Error("key_resolution_count is TELEMETRY after v0.5.5; a consumer reading it as a guard reads a per-scope-row number as if it described a key")
	}
	// 24.8 rejects an ON that is not a plain column equality, so the kind
	// restriction must sit in a WHERE -- 'key' is a literal.
	for _, on := range strings.Split(statement, " ON ")[1:] {
		clause := strings.SplitN(on, "\n", 2)[0]
		if strings.Contains(clause, "scope_kind") {
			t.Errorf("scope_kind appears in a JOIN ON (%q): 24.8 rejects a non-equality ON with Code: 403", clause)
		}
	}
}

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

// The conflict ledger key must carry the FULL suppressed row identity.
//
// Three iterations of the same counting bug landed on this one number: keyed
// on the resolved edge it double-counted one disagreement; carried as a
// representative it collapsed several disagreeing rows into one; keyed on
// (provider, ref, key) alone it collapsed the same triple across different
// teams, sources and validity windows. Each fix was correct about the
// dimension it addressed and silent about the next one.
//
// The lesson is not "add another field". It is that a count of SUPPRESSED
// ROWS must be keyed on everything that makes a row distinct, and that the
// grouping this number is computed inside erases exactly those dimensions --
// which is why the identity is assembled in SQL, before the aggregation, and
// asserted here rather than trusted.
func TestChaos4542_ConflictIdentityCarriesEveryRowDimension(t *testing.T) {
	t.Parallel()
	statement := projectTeamsStatement(cursorState{})
	for _, dimension := range []string{"o.ownership_ref", "o.ownership_key", "o.team_id", "o.source_name"} {
		if !strings.Contains(statement, dimension+", '\\0'") && !strings.Contains(statement, "'\\0', "+dimension) {
			t.Errorf("the conflict identity omits %s -- two suppressed rows differing only in that dimension collapse to one, understating what was dropped", dimension)
		}
	}
	// Collected from conflicting rows ONLY: a clean row sharing the group
	// must never be named as the offender.
	if !strings.Contains(statement, "groupUniqArrayIf(") || !strings.Contains(statement, ", o.identity_conflict = 1)") {
		t.Error("conflict identities must be collected only from rows flagged as conflicting")
	}
}
