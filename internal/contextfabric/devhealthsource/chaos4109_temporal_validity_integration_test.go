package devhealthsource_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4109: temporal/as-of project validity intervals. Before this
// ticket, querySubjectProjectMemberships' transition arm reported ONLY the
// latest touch of a (subject, project) pair (project_membership_presence's
// own `WHERE latest_to_project_id = project_id` filter), so a work item
// that moved OFF a project left no trace of ever having belonged to it --
// an as-of question about that earlier membership had nothing to read.
// This file proves the fix against a REAL ClickHouse (the leadInFrame
// window function membershipTouchesAsOfSQL/membershipIntervalsSubquery
// depend on cannot be faked by fakeClient's canned rows): seed an
// add -> remove(move) -> re-add sequence for ONE (subject, project) pair
// and assert the producer emits BOTH the earlier CLOSED interval and the
// later OPEN one as distinct graph edges.

const chaos4109OrgID = "org-4109-temporal-validity"
const chaos4109RepoID = "30000000-0000-4000-8000-000000000001"
const chaos4109RepoSlug = "acme/temporal"
const chaos4109WorkItemID = "linear:CHAOS-9101"
const chaos4109ProjectAID = "40000000-0000-4000-8000-00000000000a"
const chaos4109ProjectBID = "40000000-0000-4000-8000-00000000000b"

// chaos4109Seed writes: WI-1 added to project A at t1, moved A->B at t2,
// moved B->A at t3 -- the add/remove/re-add sequence the ticket's own scope
// asks for, seeded as three project_membership_transitions rows (a move is
// ONE row carrying both sides, per that table's own doc comment: an add is
// ("", P), a removal is (P, ""), a move is (P, Q)).
func chaos4109Seed(t *testing.T, ctx context.Context, direct interface {
	Exec(ctx context.Context, query string, args ...any) error
}, t1, t2, t3 time.Time) {
	t.Helper()
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	mustSeed("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4109RepoID, chaos4109OrgID, chaos4109RepoSlug, "linear", t1)
	mustSeed("project A", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4109ProjectAID, chaos4109OrgID, "Project A", "", "linear", "backlog", "", uint8(1), t1)
	mustSeed("project B", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4109ProjectBID, chaos4109OrgID, "Project B", "", "linear", "backlog", "", uint8(1), t1)
	// project_id here is a decoy: the whole point of this producer's
	// transition-arm read is that it must NEVER be consulted once a subject
	// has transition history (querySubjectProjectMemberships' own doc
	// comment on the work_item_column arm's exclusion). Seeded pointing at
	// project A's current state to prove that a stale/irrelevant column
	// value cannot leak into an as-of answer.
	mustSeed("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4109WorkItemID, chaos4109RepoID, chaos4109OrgID, "Temporal issue", "open", "", "", "linear", chaos4109ProjectAID, t3)
	mustSeed("transition: add to A", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
		chaos4109OrgID, chaos4109RepoID, chaos4109WorkItemID, chaos4109ProjectAID, t1, t1, "ev-4109-1")
	mustSeed("transition: move A->B", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', ?, ?, '', '', '', ?, ?, ?)`,
		chaos4109OrgID, chaos4109RepoID, chaos4109WorkItemID, chaos4109ProjectAID, chaos4109ProjectBID, t2, t2, "ev-4109-2")
	mustSeed("transition: move B->A", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', ?, ?, '', '', '', ?, ?, ?)`,
		chaos4109OrgID, chaos4109RepoID, chaos4109WorkItemID, chaos4109ProjectBID, chaos4109ProjectAID, t3, t3, "ev-4109-3")
}

// TestTransitionHistoryProjectsClosedAndOpenIntervals is the producer-side
// half of CHAOS-4109's own scope: an add -> remove -> re-add sequence for
// ONE (subject, project) pair must project TWO distinct
// BELONGS_TO_PROJECT edges to project A (an earlier CLOSED interval and a
// later OPEN one), not one edge covering the whole span (which would
// falsely claim membership during the B stretch) and not merely the
// latest one (which would erase the earlier stretch entirely -- the exact
// CHAOS-4193-era limitation this ticket fixes).
func TestTransitionHistoryProjectsClosedAndOpenIntervals(t *testing.T) {
	ctx := context.Background()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	chaos4109Seed(t, ctx, direct, t1, t2, t3)

	source, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: chaos4109OrgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}

	var toA, toB []contractsv1.ContextFabricRelationshipProjection
	for _, relationship := range batch.Relationships {
		if relationship.Type != contractsv1.ContextFabricRelationshipBelongsToProject {
			continue
		}
		switch relationship.To.CanonicalID {
		case mustProjectCanonicalID(t, "linear", chaos4109ProjectAID):
			toA = append(toA, relationship)
		case mustProjectCanonicalID(t, "linear", chaos4109ProjectBID):
			toB = append(toB, relationship)
		}
	}

	if len(toA) != 2 {
		t.Fatalf("edges to project A = %d, want 2 (one closed interval for the first stretch, one open for the current one); got %+v", len(toA), toA)
	}
	if len(toB) != 1 {
		t.Fatalf("edges to project B = %d, want 1 (the single closed stretch while the item sat on B); got %+v", len(toB), toB)
	}

	closedA, openA := toA[0], toA[1]
	if closedA.ValidTo == nil {
		closedA, openA = toA[1], toA[0]
	}
	if closedA.ValidTo == nil {
		t.Fatalf("neither project-A edge carries a ValidTo; want exactly one closed, one open: %+v", toA)
	}
	if openA.ValidTo != nil {
		t.Fatalf("both project-A edges carry a ValidTo; want exactly one closed, one open: %+v", toA)
	}
	if closedA.ValidFrom == nil || !closedA.ValidFrom.Equal(t1) {
		t.Fatalf("closed A interval ValidFrom = %v, want %v", closedA.ValidFrom, t1)
	}
	if closedA.ValidTo == nil || !closedA.ValidTo.Equal(t2) {
		t.Fatalf("closed A interval ValidTo = %v, want %v (the move-out to B)", closedA.ValidTo, t2)
	}
	if openA.ValidFrom == nil || !openA.ValidFrom.Equal(t3) {
		t.Fatalf("open A interval ValidFrom = %v, want %v (the re-add)", openA.ValidFrom, t3)
	}
	if openA.ValidTo != nil {
		t.Fatalf("open A interval ValidTo = %v, want nil (currently a member)", openA.ValidTo)
	}
	if closedA.RelationshipID == openA.RelationshipID {
		t.Fatalf("closed and open A intervals must carry DISTINCT RelationshipIDs, both got %q", closedA.RelationshipID)
	}

	b := toB[0]
	if b.ValidFrom == nil || !b.ValidFrom.Equal(t2) {
		t.Fatalf("B interval ValidFrom = %v, want %v", b.ValidFrom, t2)
	}
	if b.ValidTo == nil || !b.ValidTo.Equal(t3) {
		t.Fatalf("B interval ValidTo = %v, want %v (the move back to A)", b.ValidTo, t3)
	}

	// The stale work_items.project_id column value (seeded pointing at A)
	// must never surface as a THIRD, column-arm edge: this subject has
	// transition history, so the column arm is excluded entirely
	// (project_membership_presence's own subjects_with_history filter).
	if len(toA)+len(toB) != 3 {
		t.Fatalf("total BELONGS_TO_PROJECT edges for this subject = %d, want exactly 3 (2 to A + 1 to B) -- a 4th would mean the column arm leaked through", len(toA)+len(toB))
	}
}

func mustProjectCanonicalID(t *testing.T, provider, id string) string {
	t.Helper()
	canonicalID, omitted, err := identity.Derive(identity.KindProject, []string{provider, id}, nil)
	if err != nil || omitted {
		t.Fatalf("identity.Derive(project, %s, %s): omitted=%v err=%v", provider, id, omitted, err)
	}
	return canonicalID
}

// TestSameTimestampIntervalsGetDistinctRelationshipIDs is codex xhigh
// review R1's HIGH finding on the CHAOS-4109 PR, fixed: two intervals for
// the SAME (subject, project) pair CAN legally open at the EXACT SAME
// occurred_at -- project_membership_transitions' own ORDER BY tiebreaks on
// event_id precisely because ties are real (a batch re-sync, a clock
// correction, or -- as seeded here -- three events landing in the same
// millisecond). Before this fix, querySubjectProjectMemberships' own
// RelationshipID suffix and keyset-pagination rowKey used ONLY
// observedAt/observed_at, so the closed interval this seed produces
// ([t,t), from event e1) and the open one ([t,now), from event e3) minted
// the IDENTICAL RelationshipID -- one candidate silently clobbering the
// other via the graph write's MERGE-on-id semantics, and
// ContextFabricProjectionBatch.Validate rejecting a batch that carried
// both as distinct candidates.
//
// Reverting the eventID fix (relationshipIDIntervalSuffix in
// teams_projects_edges.go, and the rowKey's trailing `event_id` segment)
// makes this test fail with exactly that collision -- verified by hand,
// restored via content diff against the pre-mutation file, never via git
// checkout or a build/vet pass alone (this package's own AGENTS.md
// mutation-testing discipline).
func TestSameTimestampIntervalsGetDistinctRelationshipIDs(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-4109-same-timestamp"
	const repoID = "30000000-0000-4000-8000-000000000002"
	const workItemID = "linear:CHAOS-9102"
	const projectID = "40000000-0000-4000-8000-00000000000c"
	const otherProjectID = "40000000-0000-4000-8000-00000000000d"
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	mustSeed("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repoID, orgID, "acme/same-ts", "linear", t0)
	mustSeed("project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, orgID, "Project C", "", "linear", "backlog", "", uint8(1), t0)
	mustSeed("other project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		otherProjectID, orgID, "Project D", "", "linear", "backlog", "", uint8(1), t0)
	mustSeed("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workItemID, repoID, orgID, "Same-timestamp issue", "open", "", "", "linear", projectID, t0)
	// Three touches of project C, ALL sharing the identical occurred_at t0,
	// ordered only by event_id: e1 (ADD C), e2 (REMOVE C / move to D),
	// e3 (ADD C / move back from D). This is a well-formed
	// add-remove-re-add sequence -- not the malformed two-ADDs-in-a-row
	// case -- because e2 sorts between e1 and e3 by event_id.
	mustSeed("transition: add to C (e1)", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
		orgID, repoID, workItemID, projectID, t0, t0, "e1")
	mustSeed("transition: move C->D (e2)", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', ?, ?, '', '', '', ?, ?, ?)`,
		orgID, repoID, workItemID, projectID, otherProjectID, t0, t0, "e2")
	mustSeed("transition: move D->C (e3)", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', ?, ?, '', '', '', ?, ?, ?)`,
		orgID, repoID, workItemID, otherProjectID, projectID, t0, t0, "e3")

	source, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}

	projectCID := mustProjectCanonicalID(t, "linear", projectID)
	var toC []contractsv1.ContextFabricRelationshipProjection
	for _, relationship := range batch.Relationships {
		if relationship.Type == contractsv1.ContextFabricRelationshipBelongsToProject && relationship.To.CanonicalID == projectCID {
			toC = append(toC, relationship)
		}
	}
	if len(toC) != 2 {
		t.Fatalf("edges to project C = %d, want 2 (a zero-width closed interval from e1/e2, and an open one from e3), got %+v", len(toC), toC)
	}
	if toC[0].RelationshipID == toC[1].RelationshipID {
		t.Fatalf("both same-timestamp project-C intervals minted the SAME RelationshipID %q -- the graph write's MERGE-on-id would silently drop one interval", toC[0].RelationshipID)
	}
	seenValidTo := map[bool]int{}
	for _, edge := range toC {
		seenValidTo[edge.ValidTo == nil]++
		if edge.ValidFrom == nil || !edge.ValidFrom.Equal(t0) {
			t.Fatalf("edge ValidFrom = %v, want %v for every same-timestamp interval: %+v", edge.ValidFrom, t0, edge)
		}
	}
	if seenValidTo[true] != 1 || seenValidTo[false] != 1 {
		t.Fatalf("want exactly one open (ValidTo=nil) and one closed interval among the two, got %+v", toC)
	}
}

// TestDuplicateAddIsAContinuationNotMalformed pins the team-lead ruling of
// 2026-08-25: a work item ADDED to a project, then ADDED again with no
// intervening REMOVE, is a legitimate continuation of the SAME open
// interval -- #1896's discard-and-replay path and an ordinary re-sync of
// an unchanged board membership both produce this for a subject that never
// left. It must NOT be treated as malformed (this ticket's FIRST version
// of this rule dropped it, wrongly): ValidFrom stays the first ADD's
// timestamp, no second interval is created, and the subject keeps
// attributing to the project through the ORIGINAL interval.
//
// THREE consecutive ADD touches (codex xhigh review LOW, confirmed real:
// two ADDs alone cannot distinguish a single-lookahead fix from the
// filter-before-recompute one this PR actually ships -- membershipIntervalsSubquery's
// own doc comment explains why an arbitrary-length run needs the dup rows
// filtered OUT of `closed` entirely, not merely skipped one step ahead).
// Also asserts the INFO-level duplicate_add telemetry line, the read-side
// half of the same proof TestDanglingRemoveIsCountedNotSilentlyDropped
// already gives for malformed touches.
func TestDuplicateAddIsAContinuationNotMalformed(t *testing.T) {
	ctx := context.Background()
	const workItemID = "linear:CHAOS-9103"
	const projectID = "40000000-0000-4000-8000-00000000000e"
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)

	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	mustSeed("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4109RepoID, chaos4109OrgID, chaos4109RepoSlug, "linear", t1)
	mustSeed("project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, chaos4109OrgID, "Project E", "", "linear", "backlog", "", uint8(1), t1)
	mustSeed("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workItemID, chaos4109RepoID, chaos4109OrgID, "Duplicate-add issue", "open", "", "", "linear", projectID, t3)
	// THREE ADD touches in a row, no REMOVE between any of them -- proves
	// the filter-out-then-recompute algorithm handles a run, not just one
	// repeated touch.
	mustSeed("transition: add (1)", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
		chaos4109OrgID, chaos4109RepoID, workItemID, projectID, t1, t1, "ev-dup-1")
	mustSeed("transition: add (2, duplicate)", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
		chaos4109OrgID, chaos4109RepoID, workItemID, projectID, t2, t2, "ev-dup-2")
	mustSeed("transition: add (3, duplicate)", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
		chaos4109OrgID, chaos4109RepoID, workItemID, projectID, t3, t3, "ev-dup-3")

	logged := &bytes.Buffer{}
	source, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	source.WithLogger(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: chaos4109OrgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}

	projectCID := mustProjectCanonicalID(t, "linear", projectID)
	var toProject []contractsv1.ContextFabricRelationshipProjection
	for _, relationship := range batch.Relationships {
		if relationship.Type == contractsv1.ContextFabricRelationshipBelongsToProject && relationship.To.CanonicalID == projectCID {
			toProject = append(toProject, relationship)
		}
	}
	if len(toProject) != 1 {
		t.Fatalf("edges to the project = %d, want exactly 1 -- a run of duplicate ADDs must NOT mint a second interval each, and attribution must still continue through the original one: %+v", len(toProject), toProject)
	}
	edge := toProject[0]
	if edge.ValidFrom == nil || !edge.ValidFrom.Equal(t1) {
		t.Fatalf("ValidFrom = %v, want %v (the FIRST add, not either duplicate)", edge.ValidFrom, t1)
	}
	if edge.ValidTo != nil {
		t.Fatalf("ValidTo = %v, want nil -- the item is still a member; a duplicate ADD must never appear to CLOSE anything", edge.ValidTo)
	}
	output := logged.String()
	if !strings.Contains(output, "duplicate_add_entity=1") {
		t.Fatalf("want duplicate_add_entity=1 (one distinct entity carries the duplicate run), got:\n%s", output)
	}
	if !strings.Contains(output, "duplicate_add_entity_rows=2") {
		t.Fatalf("want duplicate_add_entity_rows=2 (both trailing touches counted as rows), got:\n%s", output)
	}
}

// TestDuplicateAddThenRemoveClosesFromTheOriginalInterval is the sibling
// case codex xhigh review's LOW finding asked for: a duplicate ADD is not
// always the LAST touch in the sequence. ADD, ADD (duplicate), then REMOVE
// must still close the ORIGINAL (first-add) interval at the REMOVE's own
// timestamp -- proving the `closed` CTE's row_number/touch_count/
// leadInFrame recompute (after dup rows are filtered OUT of its input)
// correctly treats the duplicate as if it were never there at all, rather
// than as an extra row that shifts the REMOVE's lookback by one.
func TestDuplicateAddThenRemoveClosesFromTheOriginalInterval(t *testing.T) {
	ctx := context.Background()
	const workItemID = "linear:CHAOS-9105"
	const projectID = "40000000-0000-4000-8000-000000000011"
	const otherProjectID = "40000000-0000-4000-8000-000000000012"
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)

	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	mustSeed("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4109RepoID, chaos4109OrgID, chaos4109RepoSlug, "linear", t1)
	mustSeed("project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, chaos4109OrgID, "Project F", "", "linear", "backlog", "", uint8(1), t1)
	mustSeed("other project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		otherProjectID, chaos4109OrgID, "Project G", "", "linear", "backlog", "", uint8(1), t1)
	mustSeed("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workItemID, chaos4109RepoID, chaos4109OrgID, "Duplicate-then-remove issue", "open", "", "", "linear", otherProjectID, t3)
	mustSeed("transition: add (1)", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
		chaos4109OrgID, chaos4109RepoID, workItemID, projectID, t1, t1, "ev-dupclose-1")
	mustSeed("transition: add (2, duplicate)", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
		chaos4109OrgID, chaos4109RepoID, workItemID, projectID, t2, t2, "ev-dupclose-2")
	mustSeed("transition: move to other project", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', ?, ?, '', '', '', ?, ?, ?)`,
		chaos4109OrgID, chaos4109RepoID, workItemID, projectID, otherProjectID, t3, t3, "ev-dupclose-3")

	source, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: chaos4109OrgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}

	projectCID := mustProjectCanonicalID(t, "linear", projectID)
	var toProject []contractsv1.ContextFabricRelationshipProjection
	for _, relationship := range batch.Relationships {
		if relationship.Type == contractsv1.ContextFabricRelationshipBelongsToProject && relationship.To.CanonicalID == projectCID {
			toProject = append(toProject, relationship)
		}
	}
	if len(toProject) != 1 {
		t.Fatalf("edges to the project = %d, want exactly 1 -- the duplicate ADD contributes nothing of its own: %+v", len(toProject), toProject)
	}
	edge := toProject[0]
	if edge.ValidFrom == nil || !edge.ValidFrom.Equal(t1) {
		t.Fatalf("ValidFrom = %v, want %v (the FIRST add, not the duplicate)", edge.ValidFrom, t1)
	}
	if edge.ValidTo == nil || !edge.ValidTo.Equal(t3) {
		t.Fatalf("ValidTo = %v, want %v (the REMOVE's own timestamp) -- the duplicate ADD must not shift where the interval closes", edge.ValidTo, t3)
	}
}

// TestDanglingRemoveIsCountedNotSilentlyDropped is the sibling proof for
// the ONE case still reserved as malformed (team-lead ruling 2026-08-25):
// a REMOVE touch with no prior ADD to close. This subject's first tracked
// touch of the project is a REMOVE (project_membership_transitions never
// saw the add -- e.g. membership predates transition tracking). The
// producer must never fabricate an interval for it (no ValidFrom is
// knowable), but it must ALSO be visible in telemetry, not merely absent
// -- "attempted nothing" and "found nothing" read identically from the
// batch alone, so the log line is what tells the two apart.
func TestDanglingRemoveIsCountedNotSilentlyDropped(t *testing.T) {
	ctx := context.Background()
	const workItemID = "linear:CHAOS-9104"
	const projectID = "40000000-0000-4000-8000-00000000000f"
	const otherProjectID = "40000000-0000-4000-8000-000000000010"
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	mustSeed("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4109RepoID, chaos4109OrgID, chaos4109RepoSlug, "linear", t1)
	mustSeed("project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		otherProjectID, chaos4109OrgID, "Project G", "", "linear", "backlog", "", uint8(1), t1)
	mustSeed("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workItemID, chaos4109RepoID, chaos4109OrgID, "Dangling-remove issue", "open", "", "", "linear", otherProjectID, t1)
	// The ONLY tracked touch of `projectID` for this subject is a REMOVE
	// (moving to otherProjectID) -- no ADD ever recorded.
	mustSeed("transition: dangling remove", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', ?, ?, '', '', '', ?, ?, ?)`,
		chaos4109OrgID, chaos4109RepoID, workItemID, projectID, otherProjectID, t1, t1, "ev-dangle-1")

	logged := &bytes.Buffer{}
	source, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	source.WithLogger(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: chaos4109OrgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available (the otherProjectID ADD, from the same row, still projects)")
	}

	projectCID := mustProjectCanonicalID(t, "linear", projectID)
	for _, relationship := range batch.Relationships {
		if relationship.Type == contractsv1.ContextFabricRelationshipBelongsToProject && relationship.To.CanonicalID == projectCID {
			t.Fatalf("a dangling REMOVE must never fabricate an edge to the project it removed from: %+v", relationship)
		}
	}
	output := logged.String()
	if !strings.Contains(output, "malformed_touch_entity=1") {
		t.Fatalf("want malformed_touch_entity=1 (the dangling remove counted, not silently dropped), got:\n%s", output)
	}
	if strings.Contains(output, "duplicate_add_entity=") {
		t.Fatalf("a dangling remove must never also be counted as a duplicate add:\n%s", output)
	}
}
