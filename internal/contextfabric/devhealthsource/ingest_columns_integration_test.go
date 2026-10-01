package devhealthsource_test

// The teams/projects source pages team_project_ownership and
// project_membership_presence on their server-side ingest columns (ops
// migrations 099 and 100), so a row whose provider/event time is older than
// the cursor but that landed after it is still projected.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const zeroUUID = "00000000-0000-0000-0000-000000000000"

// probeOverrideClient answers the source's system.columns probe with a
// fixed count, standing in for a schema before (or after) the ingest
// columns; every other statement goes to the real server.
type probeOverrideClient struct {
	inner  contextpacket.ClickHouseQueryClient
	count  int
	probes int
}

func (c *probeOverrideClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, "system.columns") {
		c.probes++
		return c.inner.Query(ctx, fmt.Sprintf("SELECT toUInt64(%d)", c.count), nil)
	}
	return c.inner.Query(ctx, statement, bindings)
}

func cursorSpace(t *testing.T, cursor string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("decode cursor %q: %v", cursor, err)
	}
	var state struct {
		Space string `json:"space"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode cursor %q: %v", cursor, err)
	}
	return state.Space
}

type ingestColumnsFixture struct {
	t                 *testing.T
	ctx               context.Context
	h                 *ingestHarness
	now, hourAgo, old time.Time
}

func newIngestColumnsFixture(t *testing.T, orgID string, client contextpacket.ClickHouseQueryClient, logger *slog.Logger) *ingestColumnsFixture {
	t.Helper()
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	createProjectMembershipPresenceView(t, ctx, direct)
	if client == nil {
		client = query
	}
	src, err := devhealthsource.NewTeamsProjectsSource(client, true)
	if err != nil {
		t.Fatal(err)
	}
	if logger != nil {
		src.WithLogger(logger)
	}
	now := time.Now().UTC().Truncate(time.Second)
	f := &ingestColumnsFixture{t: t, ctx: ctx, now: now, hourAgo: now.Add(-time.Hour), old: now.Add(-45 * 24 * time.Hour)}
	f.h = &ingestHarness{t: t, ctx: ctx, direct: direct, src: src, source: devhealthsource.TeamsProjectsSourceName, orgID: orgID, repo: zeroUUID}
	return f
}

func (f *ingestColumnsFixture) project(id, key string, updated, synced time.Time) {
	mustExec(f.t, f.ctx, f.h.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced) VALUES (?, ?, 'linear', ?, ?, 1, 'started', '', ?, ?)`,
		id, f.h.orgID, key, "project "+key, updated, synced)
}

func (f *ingestColumnsFixture) team(id string, updated, synced time.Time) {
	mustExec(f.t, f.ctx, f.h.direct, `INSERT INTO teams (id, name, description, updated_at, last_synced, org_id, provider, native_team_key, project_keys, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, "team "+id, "", updated, synced, f.h.orgID, "linear", id, []string{}, uint8(1))
}

func (f *ingestColumnsFixture) ownership(teamID, projectID, projectKey string, updated, synced time.Time) {
	mustExec(f.t, f.ctx, f.h.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at, last_synced) VALUES (?, 'linear', ?, ?, ?, 'native', ?, NULL, ?, ?)`,
		f.h.orgID, teamID, projectID, projectKey, f.hourAgo.Add(-24*time.Hour), updated, synced)
}

func (f *ingestColumnsFixture) workItem(id, projectID string, updated, ingested time.Time) {
	mustExec(f.t, f.ctx, f.h.direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced, ingested_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'linear', ?, ?, ?, ?)`,
		id, zeroUUID, f.h.orgID, "issue "+id, "open", "", "", projectID, updated, updated, ingested)
}

func (f *ingestColumnsFixture) transition(subject, toProject, toKey, event string, occurred, ingested time.Time) {
	mustExec(f.t, f.ctx, f.h.direct, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', ?, '', ?, ?, ?, ?)`,
		f.h.orgID, zeroUUID, subject, toProject, toKey, occurred, occurred, event, ingested)
}

func relationshipsOfType(d drained, typ contractsv1.ContextFabricRelationshipType) int {
	return len(d.relationships[typ])
}

// anchor seeds the rows that put the cursor an hour back, and drains them.
func (f *ingestColumnsFixture) anchor() drained {
	f.t.Helper()
	f.project("P-anchor", "ANCHOR", f.hourAgo, f.hourAgo)
	// Older than the overlap window behind the cursor, so only a row's own
	// ingest stamp can bring anything that touches it back.
	f.project("P-late", "LATE", f.old, f.old)
	f.team("T-anchor", f.hourAgo, f.hourAgo)
	first := f.h.drain("")
	if _, ok := first.entityOfKind(f.t, contractsv1.ContextFabricSubjectTeam); !ok {
		f.t.Fatal("the anchor drain did not project the team")
	}
	return first
}

func TestIngestColumnsLateRowsAreProjected(t *testing.T) {
	t.Run("team_project_ownership: a late ownership row", func(t *testing.T) {
		f := newIngestColumnsFixture(t, "72670000-0000-4000-8000-000000000001", nil, nil)
		first := f.anchor()
		f.ownership("T-anchor", "P-late", "LATE", f.old, f.now)
		second := f.h.drain(first.cursor)
		if n := relationshipsOfType(second, contractsv1.ContextFabricRelationshipOwnedByTeam); n != 1 {
			t.Fatalf("the late ownership row was SKIPPED: %d OWNED_BY_TEAM edges", n)
		}
		if got := cursorSpace(t, first.cursor); got != "ingest.v2" {
			t.Fatalf("cursor space = %q, want ingest.v2 against a schema with the ingest columns", got)
		}
	})

	t.Run("project_membership_presence: a late work_item_column row", func(t *testing.T) {
		f := newIngestColumnsFixture(t, "72670000-0000-4000-8000-000000000002", nil, nil)
		first := f.anchor()
		f.workItem("WI-late", "P-late", f.old, f.now)
		second := f.h.drain(first.cursor)
		if n := relationshipsOfType(second, contractsv1.ContextFabricRelationshipBelongsToProject); n != 1 {
			t.Fatalf("the late work_item_column membership was SKIPPED: %d BELONGS_TO_PROJECT edges", n)
		}
	})

	t.Run("project_membership_presence: a late transition", func(t *testing.T) {
		f := newIngestColumnsFixture(t, "72670000-0000-4000-8000-000000000003", nil, nil)
		first := f.anchor()
		f.transition("WI-moved", "P-late", "LATE", "evt-late", f.old, f.now)
		second := f.h.drain(first.cursor)
		if n := relationshipsOfType(second, contractsv1.ContextFabricRelationshipBelongsToProject); n != 1 {
			t.Fatalf("the late transition was SKIPPED: %d BELONGS_TO_PROJECT edges", n)
		}
	})

	t.Run("project_membership_presence: a late touch closes an interval that was already projected", func(t *testing.T) {
		f := newIngestColumnsFixture(t, "72670000-0000-4000-8000-000000000004", nil, nil)
		f.project("P-anchor", "ANCHOR", f.hourAgo, f.hourAgo)
		f.project("P-late", "LATE", f.hourAgo, f.hourAgo)
		f.team("T-anchor", f.hourAgo, f.hourAgo)
		// The ADD landed (and was projected) long ago; the REMOVE that closes
		// it carries an earlier-than-cursor event time but lands now.
		f.transition("WI-closed", "P-late", "LATE", "evt-add", f.old, f.hourAgo)
		first := f.h.drain("")
		if n := relationshipsOfType(first, contractsv1.ContextFabricRelationshipBelongsToProject); n != 1 {
			t.Fatalf("first drain: %d BELONGS_TO_PROJECT edges, want 1", n)
		}
		mustExec(t, f.ctx, f.h.direct, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) VALUES (?, NULL, ?, 'work_item', 'WI-closed', 'linear', 'P-late', '', 'LATE', '', '', ?, ?, 'evt-remove', ?)`,
			f.h.orgID, zeroUUID, f.old.Add(time.Hour), f.old.Add(time.Hour), f.now)
		second := f.h.drain(first.cursor)
		edges := second.relationships[contractsv1.ContextFabricRelationshipBelongsToProject]
		if len(edges) != 1 || edges[0].ValidTo == nil {
			t.Fatalf("the late REMOVE did not re-project the interval with an end: %+v", edges)
		}
	})
}

// Before ops migrations 099/100 the columns do not exist: the source keeps the
// provider/event-time cursor, says so, and switches once the columns appear.
func TestIngestColumnsAbsentKeepsTheLegacyCursorAndWarns(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	query, _ := orgIsolationClickHouseFixture(t)
	client := &probeOverrideClient{inner: query, count: 3}
	f := newIngestColumnsFixture(t, "72670000-0000-4000-8000-000000000005", client, logger)
	first := f.anchor()
	if got := cursorSpace(t, first.cursor); got != "ingest.v1" {
		t.Fatalf("cursor space = %q, want ingest.v1 while the ingest columns are missing", got)
	}
	if !strings.Contains(logs.String(), "ingest_cursor_unavailable") {
		t.Fatalf("no ingest_cursor_unavailable warning: %q", logs.String())
	}
	probes := client.probes
	f.h.drain(first.cursor)
	if client.probes != probes {
		t.Fatalf("a negative probe was not cached: %d probes after %d", client.probes, probes)
	}
}

// The first call after the columns appear re-reads from the start (the cursor
// position is reset, once), so no row between the two clocks is skipped.
func TestIngestColumnsSwitchResetsTheCursorOnce(t *testing.T) {
	query, _ := orgIsolationClickHouseFixture(t)
	legacy := &probeOverrideClient{inner: query, count: 3}
	f := newIngestColumnsFixture(t, "72670000-0000-4000-8000-000000000006", legacy, nil)
	f.project("P-a", "A", f.hourAgo, f.hourAgo)
	f.project("P-b", "B", f.hourAgo, f.hourAgo)
	f.team("T-a", f.hourAgo, f.hourAgo)
	// Provider clock ahead of the ingest clock: under the legacy cursor the
	// position lands in the future, past every ingest stamp.
	f.ownership("T-a", "P-a", "A", f.now.Add(2*time.Hour), f.hourAgo)
	f.ownership("T-a", "P-b", "B", f.hourAgo.Add(-2*time.Hour), f.hourAgo.Add(-2*time.Hour))
	first := f.h.drain("")
	if got := relationshipsOfType(first, contractsv1.ContextFabricRelationshipOwnedByTeam); got != 2 {
		t.Fatalf("legacy drain: %d OWNED_BY_TEAM edges, want 2", got)
	}
	if got := cursorSpace(t, first.cursor); got != "ingest.v1" {
		t.Fatalf("legacy cursor space = %q", got)
	}

	upgraded, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatal(err)
	}
	b, ok, err := upgraded.NextProjectionBatch(f.ctx, contextfabric.ProjectionCheckpoint{OrgID: f.h.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: first.cursor})
	if err != nil || !ok {
		t.Fatalf("first call after the switch: ok=%v err=%v", ok, err)
	}
	if b.Cursor != first.cursor {
		t.Fatalf("the batch must carry the checkpoint's own cursor")
	}
	if got := cursorSpace(t, b.NextCursor); got != "ingest.v2" {
		t.Fatalf("post-switch cursor space = %q, want ingest.v2", got)
	}
	f.h.src = upgraded
	after := f.h.drain(first.cursor)
	if got := relationshipsOfType(after, contractsv1.ContextFabricRelationshipOwnedByTeam); got != 2 {
		t.Fatalf("after the switch %d OWNED_BY_TEAM edges were re-read, want 2: the reset skipped rows", got)
	}
	again := f.h.drain(after.cursor)
	if len(again.batches) != 0 {
		t.Fatalf("a second drain from the new cursor emitted %d batches; the reset must happen once", len(again.batches))
	}
}

// Rows that existed before the migration all carry the migration time: one
// bulk window of identical stamps must page without loss and in bounded
// statements.
func TestIngestColumnsMigrationTimeBulkWindow(t *testing.T) {
	const projects = 450
	query, _ := orgIsolationClickHouseFixture(t)
	counting := &countingQueryClient{inner: query}
	f := newIngestColumnsFixture(t, "72670000-0000-4000-8000-000000000007", counting, nil)
	f.team("T-bulk", f.hourAgo, f.hourAgo)
	migration := f.now.Add(-10 * time.Minute)
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced)
SELECT concat('P-', toString(number)), ?, 'linear', concat('K', toString(number)), concat('project ', toString(number)), 1, 'started', '', ?, ? FROM numbers(?)`,
		f.h.orgID, f.old, migration, uint64(projects))
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at, last_synced)
SELECT ?, 'linear', 'T-bulk', concat('P-', toString(number)), concat('K', toString(number)), 'native', ?, NULL, ?, ? FROM numbers(?)`,
		f.h.orgID, f.old, f.old, migration, uint64(projects))
	drain := f.h.drain("")
	if got := relationshipsOfType(drain, contractsv1.ContextFabricRelationshipOwnedByTeam); got != projects {
		t.Fatalf("%d OWNED_BY_TEAM edges projected, want %d: rows were lost at the shared migration stamp", got, projects)
	}
	if counting.statements > 80 {
		t.Fatalf("%d statements for %d rows: the bulk window is not paging in bounded steps", counting.statements, projects)
	}
}

// The ownership edge is re-read when its PROJECT is re-ingested, even though
// the ownership row itself did not change.
func TestIngestColumnsProjectReingestRereadsItsOwnershipEdge(t *testing.T) {
	f := newIngestColumnsFixture(t, "72670000-0000-4000-8000-000000000008", nil, nil)
	recent := f.now.Add(-10 * time.Minute)
	f.project("P-own", "OWN", f.old, f.old)
	f.team("T-own", f.old, f.old)
	f.ownership("T-own", "P-own", "OWN", f.old, f.old)
	f.project("P-anchor", "ANCHOR", recent, recent)
	first := f.h.drain("")
	if n := relationshipsOfType(first, contractsv1.ContextFabricRelationshipOwnedByTeam); n != 1 {
		t.Fatalf("first drain: %d OWNED_BY_TEAM edges, want 1", n)
	}
	// A newer version of the project, stamped old by its provider, lands now.
	f.project("P-own", "OWN", f.old.Add(time.Second), f.now)
	second := f.h.drain(first.cursor)
	if n := relationshipsOfType(second, contractsv1.ContextFabricRelationshipOwnedByTeam); n != 1 {
		t.Fatalf("the project re-ingest did not re-read its ownership edge: %d OWNED_BY_TEAM edges", n)
	}
}

// A late REMOVE closes an interval whose next non-duplicate touch is not its
// immediate neighbour (a duplicate ADD sits between).
func TestIngestColumnsLateRemoveBehindADuplicateAddClosesTheInterval(t *testing.T) {
	f := newIngestColumnsFixture(t, "72670000-0000-4000-8000-000000000009", nil, nil)
	recent := f.now.Add(-10 * time.Minute)
	f.project("P-late", "LATE", recent, recent)
	f.team("T-anchor", recent, recent)
	f.transition("WI-dup", "P-late", "LATE", "evt-add-1", f.old, recent)
	f.transition("WI-dup", "P-late", "LATE", "evt-add-2", f.old.Add(time.Hour), recent)
	first := f.h.drain("")
	if n := relationshipsOfType(first, contractsv1.ContextFabricRelationshipBelongsToProject); n != 1 {
		t.Fatalf("first drain: %d BELONGS_TO_PROJECT edges, want 1", n)
	}
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) VALUES (?, NULL, ?, 'work_item', 'WI-dup', 'linear', 'P-late', '', 'LATE', '', '', ?, ?, 'evt-remove', ?)`,
		f.h.orgID, zeroUUID, f.old.Add(2*time.Hour), f.old.Add(2*time.Hour), f.now)
	second := f.h.drain(first.cursor)
	edges := second.relationships[contractsv1.ContextFabricRelationshipBelongsToProject]
	if len(edges) != 1 || edges[0].ValidTo == nil {
		t.Fatalf("the late REMOVE behind a duplicate ADD did not re-project the interval with an end: %+v", edges)
	}
}

// The one-shot re-read after the columns appear is bounded: a prod-shaped
// organization (hundreds of projects, ownership rows and memberships) is read
// again in full, in pages, and then goes quiet.
func TestIngestColumnsSwitchRereadIsBounded(t *testing.T) {
	const rows = 450
	query, _ := orgIsolationClickHouseFixture(t)
	legacy := &probeOverrideClient{inner: query, count: 3}
	f := newIngestColumnsFixture(t, "72670000-0000-4000-8000-00000000000a", legacy, nil)
	f.team("T-shape", f.hourAgo, f.hourAgo)
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced)
SELECT concat('P-', toString(number)), ?, 'linear', concat('K', toString(number)), concat('project ', toString(number)), 1, 'started', '', ?, ? FROM numbers(?)`,
		f.h.orgID, f.old, f.old, uint64(rows))
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at, last_synced)
SELECT ?, 'linear', 'T-shape', concat('P-', toString(number)), concat('K', toString(number)), 'native', ?, NULL, ?, ? FROM numbers(?)`,
		f.h.orgID, f.old, f.old, f.old, uint64(rows))
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced, ingested_at)
SELECT concat('WI-', toString(number)), ?, ?, 'issue', 'open', '', '', 'linear', concat('P-', toString(number)), ?, ?, ? FROM numbers(?)`,
		zeroUUID, f.h.orgID, f.old, f.old, f.old, uint64(rows))
	first := f.h.drain("")
	if got := relationshipsOfType(first, contractsv1.ContextFabricRelationshipOwnedByTeam); got != rows {
		t.Fatalf("legacy drain: %d OWNED_BY_TEAM edges, want %d", got, rows)
	}

	counting := &countingQueryClient{inner: query}
	upgraded, err := devhealthsource.NewTeamsProjectsSource(counting, true)
	if err != nil {
		t.Fatal(err)
	}
	f.h.src = upgraded
	after := f.h.drain(first.cursor)
	if got := relationshipsOfType(after, contractsv1.ContextFabricRelationshipOwnedByTeam); got != rows {
		t.Fatalf("re-read %d OWNED_BY_TEAM edges, want %d", got, rows)
	}
	if got := relationshipsOfType(after, contractsv1.ContextFabricRelationshipBelongsToProject); got != rows {
		t.Fatalf("re-read %d BELONGS_TO_PROJECT edges, want %d", got, rows)
	}
	t.Logf("switch re-read: %d statements, %d batches for %d projects", counting.statements, len(after.batches), rows)
	if counting.statements > 150 {
		t.Fatalf("%d statements for the one-shot re-read of %d rows", counting.statements, rows)
	}
	quiet := counting.statements
	f.h.drain(after.cursor)
	if counting.statements-quiet > 40 {
		t.Fatalf("a caught-up drain after the re-read sent %d statements", counting.statements-quiet)
	}
}
