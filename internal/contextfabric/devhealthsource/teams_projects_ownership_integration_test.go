package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// The ownership aggregation is the one producer in this issue whose
// correctness lives entirely in SQL -- a GROUP BY, an argMax and two joins --
// so the package's fake cannot test it at all: fakeClient returns canned rows
// for a statement without executing it. These tests run the real statement
// against a real, production-typed ClickHouse (codex round-1 F1/F2).

// TestOwnershipCollapseDoesNotMergeAcrossProviders is codex round-1 F1.
// team_project_ownership joins projects on project_key, and project_key is
// only unique WITHIN a provider. Collapsing without carrying provider merges
// two providers' independent ownership assertions, and the join then
// fabricates cross-provider project->team edges -- an edge asserting an
// ownership nobody ever recorded -- and can emit two rows that resolve to the
// same projects.id, duplicating a RelationshipID and wedging the batch.
//
// Live today there are zero cross-provider project_key collisions (verified
// across every organization: 0 keys under >1 provider in projects, 0 in
// team_project_ownership), so this is a latent defect rather than an active
// one. That is exactly why it needs a test: nothing in the schema prevents
// the collision, and the failure is silent fabrication rather than an error.
func subOwnershipCollapseDoesNotMergeAcrossProviders(t *testing.T, ctx context.Context, fixture *ownershipFixture) {

	batch := fixture.project(t, ctx)
	for _, forbidden := range []struct{ project, team string }{
		{"PROJ-GITHUB", "TEAM-GITLAB"},
		{"PROJ-GITLAB", "TEAM-GITHUB"},
	} {
		id := "relationship:project_team:" + forbidden.project + ":" + forbidden.team + ":native"
		if hasRelationship(batch, id) {
			t.Errorf("fabricated a cross-provider ownership edge %q -- the two providers' assertions were merged on a shared project_key", id)
		}
	}
	for _, wanted := range []struct{ project, team string }{
		{"PROJ-GITHUB", "TEAM-GITHUB"},
		{"PROJ-GITLAB", "TEAM-GITLAB"},
	} {
		id := "relationship:project_team:" + wanted.project + ":" + wanted.team + ":native"
		if !hasRelationship(batch, id) {
			t.Errorf("lost the genuine same-provider ownership edge %q while scoping by provider", id)
		}
	}
	assertUniqueRelationshipIDs(t, batch)
}

// TestOwnershipWindowTakesTheLatestAssertion is codex round-1 F2. The
// approved semantics is "the latest assertion wins", keyed on valid_from --
// not "the largest valid_to", which is what max() gives.
//
// Both directions are asserted because each fails a different way and a
// producer can easily get one right and the other wrong:
//   - later assertion CLOSES EARLIER: max() reports the older, later date, so
//     an ownership that ended in March looks live until December.
//   - later assertion is OPEN: the NULL must win. This is the ClickHouse trap
//     codex flagged, and it is real -- verified directly against this server:
//     plain argMax(valid_to, valid_from) SKIPS the NULL and returns the older
//     row's date, holding a closed edge open. argMax(tuple(valid_to),
//     valid_from).1 preserves it.
func subOwnershipWindowTakesTheLatestAssertion(t *testing.T, ctx context.Context, fixture *ownershipFixture) {

	batch := fixture.project(t, ctx)

	closedLate := relationshipByID(t, batch, "relationship:project_team:PROJ-CLOSED:TEAM-GITHUB:native")
	if closedLate.ValidTo == nil {
		t.Fatal("PROJ-CLOSED: the latest assertion closed the ownership, so the edge must carry an end")
	}
	if !closedLate.ValidTo.Equal(ownershipLatestClose) {
		t.Errorf("PROJ-CLOSED: ValidTo = %v, want the LATEST assertion's valid_to %v (max() would wrongly report the older assertion's later date %v)",
			closedLate.ValidTo, ownershipLatestClose, ownershipStaleFarFutureClose)
	}

	stillOpen := relationshipByID(t, batch, "relationship:project_team:PROJ-OPEN:TEAM-GITHUB:native")
	if stillOpen.ValidTo != nil {
		t.Errorf("PROJ-OPEN: ValidTo = %v, want nil -- the latest assertion left the window open, and a NULL valid_to must not be skipped in favour of an older closed row", stillOpen.ValidTo)
	}
	if stillOpen.ValidFrom == nil || !stillOpen.ValidFrom.Equal(ownershipFirstSeen) {
		t.Errorf("PROJ-OPEN: ValidFrom = %v, want the EARLIEST observed assertion %v", stillOpen.ValidFrom, ownershipFirstSeen)
	}
}

var (
	ownershipFirstSeen            = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ownershipLaterAssertion       = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	ownershipStaleFarFutureClose  = time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	ownershipLatestClose          = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	ownershipSupersededEarlyClose = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
)

type ownershipFixture struct {
	orgID  string
	source *devhealthsource.TeamsProjectsSource
	direct clickhousedriver.Conn
}

// TestOwnershipProducerAgainstRealClickHouse is the single entry point for
// every ownership assertion that needs real SQL, and it exists in this shape
// for a measured reason: these started as six separate top-level tests, each
// spawning its own ClickHouse container. Six extra containers under `-race`
// starved the Postgres-backed packages in the same run -- pginvestigation and
// storage/postgres both blew past their deadlines in `make verify` while
// passing comfortably in isolation. One container, one schema, and a distinct
// ORGANIZATION per subtest gives the same isolation the source itself
// guarantees, at a fraction of the cost.
func TestOwnershipProducerAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	cases := []struct {
		name  string
		orgID string
		run   func(*testing.T, context.Context, *ownershipFixture)
	}{
		{"collapse does not merge across providers", "30000000-0000-4000-8000-000000000001", subOwnershipCollapseDoesNotMergeAcrossProviders},
		{"window takes the latest assertion", "30000000-0000-4000-8000-000000000002", subOwnershipWindowTakesTheLatestAssertion},
		{"ambiguous project key omits the edge", "30000000-0000-4000-8000-000000000003", subAmbiguousProjectKeyOmitsTheEdgeRatherThanGuessing},
		{"ambiguous rows do not stall pagination", "30000000-0000-4000-8000-000000000004", subAmbiguousRowsDoNotStallPagination},
		{"tied assertions resolve deterministically", "30000000-0000-4000-8000-000000000005", subTiedOwnershipAssertionsResolveDeterministically},
		{"ambiguity guard is scoped to one organization", "30000000-0000-4000-8000-000000000006", subAmbiguityGuardIsScopedToOneOrganization},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.run(t, ctx, newOwnershipFixture(t, ctx, query, direct, testCase.orgID))
		})
	}
}

// newOwnershipFixture seeds one organization's baseline rows into the SHARED
// schema. Organization scoping is what keeps the subtests independent, which
// is the same boundary the producer enforces -- so a leak between subtests
// would itself be a real finding.
func newOwnershipFixture(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, direct clickhousedriver.Conn, orgID string) *ownershipFixture {
	t.Helper()
	at := ownershipLaterAssertion
	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	seedTeam := func(id, provider string) {
		mustSeed("teams "+id, `INSERT INTO teams VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, id+" name", "", at, orgID, provider, id, uint8(1))
	}
	seedProject := func(id, provider, key string) {
		mustSeed("projects "+id, `INSERT INTO projects VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, orgID, provider, key, id+" name", uint8(1), "started", "", at)
	}
	seedOwnership := func(provider, teamID, projectID, key string, validFrom time.Time, validTo any) {
		mustSeed("team_project_ownership "+projectID, `INSERT INTO team_project_ownership VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			orgID, provider, teamID, projectID, key, "native", validFrom, validTo, at)
	}

	// AMBIGUOUS: two projects sharing one (provider, project_key). The join
	// fans out to two DISTINCT projects.id, so the RelationshipIDs differ and
	// batch.Validate stays silent -- the producer must catch this itself.
	seedProject("PROJ-AMBIG-A", "github", "AMBIG-KEY")
	seedProject("PROJ-AMBIG-B", "github", "AMBIG-KEY")

	seedTeam("TEAM-GITHUB", "github")
	seedTeam("TEAM-GITLAB", "gitlab")

	// F1: ONE project_key, two providers, two independent ownerships.
	seedProject("PROJ-GITHUB", "github", "SHARED-KEY")
	seedProject("PROJ-GITLAB", "gitlab", "SHARED-KEY")
	seedOwnership("github", "TEAM-GITHUB", "PROJ-GITHUB", "SHARED-KEY", ownershipFirstSeen, nil)
	seedOwnership("gitlab", "TEAM-GITLAB", "PROJ-GITLAB", "SHARED-KEY", ownershipFirstSeen, nil)

	// F2a: the LATER assertion closes EARLIER than the older one.
	seedProject("PROJ-CLOSED", "github", "CLOSED-KEY")
	seedOwnership("github", "TEAM-GITHUB", "PROJ-CLOSED", "CLOSED-KEY", ownershipFirstSeen, ownershipStaleFarFutureClose)
	seedOwnership("github", "TEAM-GITHUB", "PROJ-CLOSED", "CLOSED-KEY", ownershipLaterAssertion, ownershipLatestClose)

	// F2b: the LATER assertion is OPEN and must not be skipped for the
	// older closed one.
	seedProject("PROJ-OPEN", "github", "OPEN-KEY")
	seedOwnership("github", "TEAM-GITHUB", "PROJ-OPEN", "OPEN-KEY", ownershipFirstSeen, ownershipSupersededEarlyClose)
	seedOwnership("github", "TEAM-GITHUB", "PROJ-OPEN", "OPEN-KEY", ownershipLaterAssertion, nil)

	seedOwnership("github", "TEAM-GITHUB", "PROJ-AMBIG-A", "AMBIG-KEY", ownershipFirstSeen, nil)

	source, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatalf("new teams/projects source: %v", err)
	}
	return &ownershipFixture{orgID: orgID, source: source, direct: direct}
}

func (f *ownershipFixture) project(t *testing.T, ctx context.Context) contextfabric.ProjectionBatch {
	t.Helper()
	batch, available, err := f.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: f.orgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("project ownership fixture: %v", err)
	}
	if !available {
		t.Fatal("expected an ownership batch to be available")
	}
	return batch
}

func hasRelationship(batch contextfabric.ProjectionBatch, relationshipID string) bool {
	for _, relationship := range batch.Relationships {
		if relationship.RelationshipID == relationshipID {
			return true
		}
	}
	return false
}

// assertUniqueRelationshipIDs restates ContextFabricProjectionBatch.Validate's
// own uniqueness rule at the point of failure. Validate would already have
// rejected the batch, but that surfaces as an opaque build error; this names
// the duplicated edge, which is what an operator debugging a wedged
// organization actually needs.
func assertUniqueRelationshipIDs(t *testing.T, batch contextfabric.ProjectionBatch) {
	t.Helper()
	seen := map[string]int{}
	for _, relationship := range batch.Relationships {
		seen[relationship.RelationshipID]++
	}
	for id, count := range seen {
		if count > 1 {
			t.Errorf("relationship %q emitted %d times -- duplicate relationship IDs fail batch validation and wedge the organization", id, count)
		}
	}
}

// TestAmbiguousProjectKeyOmitsTheEdgeRatherThanGuessing closes the failure
// mode the F1 rewrite MOVED rather than removed.
//
// Grouping ownership on (provider, project_key) and resolving through
// projects assumes (org, provider, project_key) names exactly one project.
// That holds in live data today, but nothing in the schema enforces it, and
// the failure is invisible where every other duplicate is loud: a key
// resolving to two projects fans the join out to two rows with two DISTINCT
// projects.id, so the RelationshipIDs differ and
// ContextFabricProjectionBatch.Validate never trips. The result would be a
// fabricated ownership edge to a project the source never asserted -- the
// same class of silent invention as the cross-provider merge, one level down.
//
// The producer omits BOTH candidates rather than picking one, and does not
// fail the batch: one ambiguous key must not take an organization's whole
// projection down, and guessing between two projects is exactly the
// "discoveries may not mint canonical truth" line.
func subAmbiguousProjectKeyOmitsTheEdgeRatherThanGuessing(t *testing.T, ctx context.Context, fixture *ownershipFixture) {

	batch := fixture.project(t, ctx)
	for _, projectID := range []string{"PROJ-AMBIG-A", "PROJ-AMBIG-B"} {
		id := "relationship:project_team:" + projectID + ":TEAM-GITHUB:native"
		if hasRelationship(batch, id) {
			t.Errorf("emitted %q from an ambiguous project_key -- two projects share (github, AMBIG-KEY), so which one the source meant is unknowable and neither may be guessed", id)
		}
	}
	// The unambiguous edges must survive: omission is per-key, never a
	// whole-batch refusal.
	for _, id := range []string{
		"relationship:project_team:PROJ-GITHUB:TEAM-GITHUB:native",
		"relationship:project_team:PROJ-OPEN:TEAM-GITHUB:native",
	} {
		if !hasRelationship(batch, id) {
			t.Errorf("lost the unambiguous edge %q -- one ambiguous key must not suppress the rest", id)
		}
	}
	assertUniqueRelationshipIDs(t, batch)
}

// TestAmbiguousRowsDoNotStallPagination is codex round-2 F1, and it is the
// defect the ambiguity omission introduced.
//
// Omission happens AFTER the raw-row limit, in the scan, so an omitted row
// consumes page budget while contributing no candidate. A page whose rows are
// all omitted therefore produces an EMPTY candidate set, and pagedBatch
// returns available=false without ever building a batch -- so the cursor never
// advances past those rows. The next tick reads the same page, omits the same
// rows, and stops again: the source reports "caught up" forever while valid
// edges sitting beyond the ambiguous block are never reached.
//
// Cursor advancement has to be driven by RAW ROWS CONSUMED, not by candidates
// emitted. This seeds a full page of ambiguous rows ahead of a valid edge and
// asserts the walk both reaches the edge and terminates.
func subAmbiguousRowsDoNotStallPagination(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	fixture.seedAmbiguousBlockThenValidEdge(t, ctx)

	const wantEdge = "relationship:project_team:PROJ-BEYOND:TEAM-GITHUB:native"
	cursor := ""
	found := false
	for page := 0; page < 40; page++ {
		batch, available, err := fixture.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{
			OrgID: fixture.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if !available {
			break
		}
		if batch.NextCursor == cursor {
			t.Fatalf("page %d: cursor did not advance past a page of omitted rows -- projection would repeat this page forever", page)
		}
		cursor = batch.NextCursor
		if hasRelationship(batch, wantEdge) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("never reached %q -- a block of omitted ambiguous rows stalled the walk before it", wantEdge)
	}
}

// seedAmbiguousBlockThenValidEdge writes more ambiguous ownership rows than
// one page holds, all timestamped after the fixture's other rows, followed by
// a single valid edge later still -- so at least one whole page consists only
// of rows that emit nothing.
func (f *ownershipFixture) seedAmbiguousBlockThenValidEdge(t *testing.T, ctx context.Context) {
	t.Helper()
	// The bulk PROJECT rows are timestamped EARLIER than the ownership rows
	// they make ambiguous. That separation is the whole point: if the project
	// entities shared the ownership rows' timestamp they would emit entity
	// candidates into the same page, the batch would be non-empty, and the
	// cursor would advance for a reason unrelated to the omitted rows --
	// hiding the stall. With entities consumed first, a later page consists
	// of nothing but rows that emit nothing.
	early := ownershipLaterAssertion.Add(1 * time.Hour)
	block := ownershipLaterAssertion.Add(24 * time.Hour)
	beyond := ownershipLaterAssertion.Add(48 * time.Hour)
	// Each generated key resolves to TWO projects, so it is ambiguous and the
	// join yields two rows per key -- comfortably past one page.
	mustExec(t, ctx, f.direct, `INSERT INTO projects
SELECT concat('P-AMBIG-A-', toString(number)), ?, 'github', concat('BULK-', toString(number)), 'bulk a', 1, 'started', '', ?
FROM numbers(150)`, f.orgID, early)
	mustExec(t, ctx, f.direct, `INSERT INTO projects
SELECT concat('P-AMBIG-B-', toString(number)), ?, 'github', concat('BULK-', toString(number)), 'bulk b', 1, 'started', '', ?
FROM numbers(150)`, f.orgID, early)
	mustExec(t, ctx, f.direct, `INSERT INTO team_project_ownership
SELECT ?, 'github', 'TEAM-GITHUB', concat('P-AMBIG-A-', toString(number)), concat('BULK-', toString(number)), 'native', ?, NULL, ?
FROM numbers(150)`, f.orgID, block, block)

	// Also early, so the page holding the ambiguous block cannot be rescued
	// by this project's own entity candidate.
	mustExec(t, ctx, f.direct, `INSERT INTO projects VALUES (?, ?, 'github', 'BEYOND-KEY', 'beyond', 1, 'started', '', ?)`,
		"PROJ-BEYOND", f.orgID, early)
	mustExec(t, ctx, f.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', ?, 'BEYOND-KEY', 'native', ?, NULL, ?)`,
		f.orgID, "PROJ-BEYOND", beyond, beyond)
}

func mustExec(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, statement string, args ...any) {
	t.Helper()
	if err := direct.Exec(ctx, statement, args...); err != nil {
		t.Fatalf("seed: %v\n%s", err, statement)
	}
}

// TestTiedOwnershipAssertionsResolveDeterministically is codex round-2 F2.
//
// argMax keyed on valid_from alone has no ordering between two assertions
// stamped at the SAME instant, so a group holding one open and one closed
// assertion at that instant could project either -- flipping between runs
// with merge order. A flaky pass here IS the defect, so this projects
// repeatedly and asserts every run agrees.
//
// Reaching the tie takes care: (org, provider, project_id, team_id, source,
// valid_from) is team_project_ownership's full ORDER BY, so two rows agreeing
// on all of it collapse under FINAL and no tie survives. The rows must differ
// in a column inside that key but OUTSIDE the producer's GROUP BY, and
// project_id is the only one -- which is realistic precisely because this
// table's project_id is unreliable (the same project_key legitimately appears
// under different project_id values; that is Trap B).
//
// The tiebreak is open-wins, and the justification is semantic rather than
// empirical: live data cannot adjudicate it, because the writer has never
// emitted a closed ownership row at all (0 of 618 carry a valid_to). A
// same-instant assertion of ongoing ownership outranks a same-instant
// closure, so a live owner is never hidden by a simultaneous close.
func subTiedOwnershipAssertionsResolveDeterministically(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	at := ownershipLaterAssertion
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', 'TIE-KEY', 'tie', 1, 'started', '', ?)`,
		"PROJ-TIE", fixture.orgID, at)
	// Same instant, same group after collapse; one closes, one leaves open.
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', 'ownership-row-closed', 'TIE-KEY', 'native', ?, ?, ?)`,
		fixture.orgID, ownershipFirstSeen, ownershipLatestClose, at)
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', 'ownership-row-open', 'TIE-KEY', 'native', ?, NULL, ?)`,
		fixture.orgID, ownershipFirstSeen, at)

	const tieEdge = "relationship:project_team:PROJ-TIE:TEAM-GITHUB:native"
	for run := 0; run < 8; run++ {
		edge := relationshipByID(t, fixture.project(t, ctx), tieEdge)
		if edge.ValidTo != nil {
			t.Fatalf("run %d: ValidTo = %v, want nil -- a same-instant assertion of ongoing ownership outranks a same-instant closure, and the result must not depend on merge order", run, edge.ValidTo)
		}
	}
}

// TestAmbiguityGuardIsScopedToOneOrganization is the self-found gap from the
// round-2 org-scope review, now a test rather than an argument.
//
// The ambiguity guard counts projects per (provider, project_key) with a
// window function whose PARTITION BY does NOT include org_id. It is correct
// only because the org filter sits INSIDE the subquery the window runs over,
// so the window never sees another organization's rows. That correctness
// rests entirely on a subquery boundary, and hoisting the WHERE outward is a
// plausible, well-intentioned refactor that nothing objected to -- a proof
// carrying its own copy.
//
// Two organizations sharing (provider, project_key), each with ONE project:
// neither is ambiguous, and both edges must project. Flatten the filter and
// the window counts across organizations, both keys look ambiguous, and both
// valid edges silently vanish.
func subAmbiguityGuardIsScopedToOneOrganization(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	const otherOrg = "40000000-0000-4000-8000-000000000004"
	at := ownershipLaterAssertion

	// The OTHER organization's project and ownership, sharing this
	// organization's provider and project_key.
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', 'CROSS-ORG-KEY', 'other org project', 1, 'started', '', ?)`,
		"PROJ-OTHER-ORG", otherOrg, at)
	mustExec(t, ctx, fixture.direct, `INSERT INTO teams VALUES (?, ?, '', ?, ?, 'github', ?, 1)`,
		"TEAM-OTHER-ORG", "other org team", at, otherOrg, "TEAM-OTHER-ORG")
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-OTHER-ORG', ?, 'CROSS-ORG-KEY', 'native', ?, NULL, ?)`,
		otherOrg, "PROJ-OTHER-ORG", ownershipFirstSeen, at)

	// This organization's own project under the SAME (provider, project_key).
	mustExec(t, ctx, fixture.direct, `INSERT INTO projects VALUES (?, ?, 'github', 'CROSS-ORG-KEY', 'this org project', 1, 'started', '', ?)`,
		"PROJ-THIS-ORG", fixture.orgID, at)
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership VALUES (?, 'github', 'TEAM-GITHUB', ?, 'CROSS-ORG-KEY', 'native', ?, NULL, ?)`,
		fixture.orgID, "PROJ-THIS-ORG", ownershipFirstSeen, at)

	batch := fixture.project(t, ctx)
	const wantEdge = "relationship:project_team:PROJ-THIS-ORG:TEAM-GITHUB:native"
	if !hasRelationship(batch, wantEdge) {
		t.Fatalf("%q was omitted -- the ambiguity window counted another organization's project, so a single unambiguous project looked like two", wantEdge)
	}
	// The other organization's rows must not leak into this projection at all.
	for _, forbidden := range []string{
		"relationship:project_team:PROJ-OTHER-ORG:TEAM-OTHER-ORG:native",
		"relationship:project_team:PROJ-OTHER-ORG:TEAM-GITHUB:native",
	} {
		if hasRelationship(batch, forbidden) {
			t.Errorf("projected %q -- another organization's ownership crossed the tenant boundary", forbidden)
		}
	}
}
