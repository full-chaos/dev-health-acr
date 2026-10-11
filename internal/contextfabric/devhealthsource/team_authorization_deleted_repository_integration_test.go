package devhealthsource_test

// A team's authorization_repositories list is built from resolved ownership.
// When a repos row is hard-deleted (a ClickHouse mutation; repos has no
// soft-delete column) its slug no longer resolves, so an incremental resume
// must re-project the team WITHOUT that slug. Asserted on what
// NextProjectionBatch hands the graph, on a real ClickHouse.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

func subTeamAuthorizationDropsAHardDeletedRepository(t *testing.T, ctx context.Context, f *ownershipFixture) {
	now := time.Now().UTC().Truncate(time.Second)
	f.source.SetClockForTest(func() time.Time { return now })
	ownedAt := now.Add(-3 * time.Hour)
	// The deleted repository carries the newest repos.last_synced, so the
	// team's watermark falls behind the saved cursor when its row disappears.
	chaos7130Repo(t, ctx, f, "72460000-0000-4000-8000-00000000000a", "acme/kept", "github", ownedAt)
	chaos7130Repo(t, ctx, f, "72460000-0000-4000-8000-00000000000b", "acme/deleted", "github", ownedAt.Add(time.Hour))
	chaos7119Team(t, ctx, f, "team-del", ownedAt)
	chaos7119Own(t, ctx, f, "team-del", "github", "acme/kept", nil, ownedAt)
	chaos7119Own(t, ctx, f, "team-del", "github", "acme/deleted", nil, ownedAt)

	first := chaos7139Run(t, ctx, f)
	if got := chaos7130List(first, "team-del"); !reflect.DeepEqual(got, []string{"acme/deleted", "acme/kept"}) {
		t.Fatalf("before the delete: %v, want [acme/deleted acme/kept]", got)
	}

	mustExec(t, ctx, f.direct, `ALTER TABLE repos DELETE WHERE org_id = ? AND id = toUUID(?) SETTINGS mutations_sync = 2`,
		f.orgID, "72460000-0000-4000-8000-00000000000b")

	now = now.Add(devhealthsource.TeamOwnershipReconcilePeriodForTest + time.Minute)
	second := chaos7139RunFrom(t, ctx, f, first.cursor)
	if _, ok := second.entities["team-del"]; !ok {
		t.Fatalf("team-del was not re-projected after its repos row was deleted (watermark fell behind the cursor)")
	}
	if got := chaos7130List(second, "team-del"); !reflect.DeepEqual(got, []string{"acme/kept"}) {
		t.Errorf("after the delete: %v, want [acme/kept]", got)
	}
}

// A hard delete of a repos row that is NOT the team's newest leaves every
// watermark unchanged, so only the periodic reconcile can see it: nothing is
// re-projected before the period elapses, and the team is re-projected
// without the slug once it has.
func subTeamAuthorizationDropsANonNewestHardDeletedRepository(t *testing.T, ctx context.Context, f *ownershipFixture) {
	now := time.Now().UTC().Truncate(time.Second)
	f.source.SetClockForTest(func() time.Time { return now })
	ownedAt := now.Add(-3 * time.Hour)
	chaos7130Repo(t, ctx, f, "72460000-0000-4000-8000-00000000001a", "acme/old", "github", ownedAt)
	chaos7130Repo(t, ctx, f, "72460000-0000-4000-8000-00000000001b", "acme/new", "github", ownedAt.Add(time.Hour))
	chaos7119Team(t, ctx, f, "team-nn", ownedAt)
	chaos7119Own(t, ctx, f, "team-nn", "github", "acme/old", nil, ownedAt)
	chaos7119Own(t, ctx, f, "team-nn", "github", "acme/new", nil, ownedAt)

	first := chaos7139Run(t, ctx, f)
	if got := chaos7130List(first, "team-nn"); !reflect.DeepEqual(got, []string{"acme/new", "acme/old"}) {
		t.Fatalf("before the delete: %v", got)
	}
	mustExec(t, ctx, f.direct, `ALTER TABLE repos DELETE WHERE org_id = ? AND id = toUUID(?) SETTINGS mutations_sync = 2`,
		f.orgID, "72460000-0000-4000-8000-00000000001a")

	early := chaos7139RunFrom(t, ctx, f, first.cursor)
	if len(early.entities) != 0 {
		t.Fatalf("a call inside the reconcile period re-read teams: %d entities", len(early.entities))
	}
	if early.cursor != first.cursor {
		t.Errorf("the cursor moved while no reconcile was due")
	}

	now = now.Add(devhealthsource.TeamOwnershipReconcilePeriodForTest - time.Nanosecond)
	if justBefore := chaos7139RunFrom(t, ctx, f, early.cursor); len(justBefore.entities) != 0 {
		t.Fatalf("a reconcile ran one nanosecond before its period elapsed")
	}
	now = now.Add(time.Nanosecond)
	late := chaos7139RunFrom(t, ctx, f, early.cursor)
	if _, ok := late.entities["team-nn"]; !ok {
		t.Fatalf("team-nn was not re-projected by the periodic reconcile")
	}
	if got := chaos7130List(late, "team-nn"); !reflect.DeepEqual(got, []string{"acme/new"}) {
		t.Errorf("after the reconcile: %v, want [acme/new]", got)
	}
	// One pass per period: the pass ended, so the next call reads nothing.
	again := chaos7139RunFrom(t, ctx, f, late.cursor)
	if len(again.entities) != 0 {
		t.Errorf("a finished reconcile pass ran again inside its period: %d entities", len(again.entities))
	}
}

// A reconcile pass walks every page of the organization's teams, keeps the
// cursor position, and re-serves a page whose batch was never applied.
func subTeamReconcileWalksEveryPageAndRetriesAnUnappliedPage(t *testing.T, ctx context.Context, f *ownershipFixture) {
	now := time.Now().UTC().Truncate(time.Second)
	f.source.SetClockForTest(func() time.Time { return now })
	mustExec(t, ctx, f.direct, `INSERT INTO teams (id, name, description, updated_at, last_synced, org_id, provider, native_team_key, project_keys, is_active)
SELECT concat('T-', leftPad(toString(number), 4, '0')), concat('team ', toString(number)), '', ?, ?, ?, 'linear', concat('T-', toString(number)), [], 1 FROM numbers(250)`,
		now.Add(-24*time.Hour), now.Add(-3*time.Hour), f.orgID)
	first := chaos7139Run(t, ctx, f)
	if len(first.entities) < 252 {
		t.Fatalf("%d of 252 teams projected", len(first.entities))
	}
	now = now.Add(devhealthsource.TeamOwnershipReconcilePeriodForTest)
	next := func(cursor string) (contextfabric.ProjectionBatch, bool) {
		t.Helper()
		batch, ok, err := f.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: f.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		return batch, ok
	}
	ids := func(b contextfabric.ProjectionBatch) []string {
		var out []string
		for _, e := range b.Entities {
			out = append(out, e.Subject.CanonicalID)
		}
		return out
	}
	one, ok := next(first.cursor)
	if !ok || len(one.Entities) != 200 {
		t.Fatalf("first reconcile page: available=%v entities=%d, want 200", ok, len(one.Entities))
	}
	if keysetPosition(t, one.NextCursor) != keysetPosition(t, first.cursor) || one.NextCursor == first.cursor {
		t.Fatalf("a reconcile batch must keep the cursor position and carry its own ack")
	}
	// A checkpoint that carries some OTHER ack than the page's own never
	// applied it either: the same page again, not the next one.
	wrong, ok := next(chaos7246WithAck(t, first.cursor, "00ff00ff00ff00ff"))
	if !ok || !reflect.DeepEqual(ids(wrong), ids(one)) {
		t.Fatalf("a foreign ack advanced the reconcile pass")
	}
	again, ok := next(first.cursor)
	if !ok || again.BatchID != one.BatchID || !reflect.DeepEqual(ids(again), ids(one)) {
		t.Fatalf("an unapplied reconcile page was not served again unchanged")
	}
	two, ok := next(again.NextCursor)
	if !ok || len(two.Entities) != 52 {
		t.Fatalf("second reconcile page: available=%v entities=%d, want 52", ok, len(two.Entities))
	}
	seen := map[string]bool{}
	for _, id := range append(ids(one), ids(two)...) {
		if seen[id] {
			t.Fatalf("team %s served twice in one pass", id)
		}
		seen[id] = true
	}
	if len(seen) != 252 {
		t.Fatalf("pass served %d distinct teams, want 252", len(seen))
	}
	for call := 0; call < 3; call++ {
		if _, ok := next(two.NextCursor); ok {
			t.Fatalf("call %d: a finished reconcile pass served another batch", call)
		}
	}
}

func chaos7246WithAck(t *testing.T, cursor, ack string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["ack"] = ack
	out, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(out)
}

// A reconcile batch never moves the checkpoint, even when the same call
// skipped a page of rows that emit nothing before it caught up.
func subTeamReconcileKeepsTheCheckpointAfterASkippedPage(t *testing.T, ctx context.Context, f *ownershipFixture) {
	now := time.Now().UTC().Truncate(time.Second)
	f.source.SetClockForTest(func() time.Time { return now })
	chaos7119Team(t, ctx, f, "team-skip", now.Add(-3*time.Hour))
	// The project itself is consumed by the first run; only the ownership row
	// that names it by an overlong id arrives later, and it is omitted: its
	// page publishes nothing and the walk moves on in-process.
	const skippedProject = "P-SKIP-A-" + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" + "xxxxxxxxxxxxxxxx" + "0"
	mustExec(t, ctx, f.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, 'github', 'SKIP-0', 'skipped', 1, 'started', '', ?)`,
		skippedProject, f.orgID, now.Add(-3*time.Hour))
	first := chaos7139Run(t, ctx, f)
	late := time.Now().UTC().Add(240 * time.Hour)
	mustExec(t, ctx, f.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?, 'github', 'team-skip', ?, 'SKIP-0', 'native', ?, NULL, ?)`,
		f.orgID, skippedProject, late, late)
	now = now.Add(devhealthsource.TeamOwnershipReconcilePeriodForTest)
	batch, ok, err := f.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: f.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: first.cursor})
	if err != nil || !ok {
		t.Fatalf("reconcile batch: available=%v err=%v", ok, err)
	}
	if len(batch.Entities) == 0 {
		t.Fatalf("expected the reconcile batch, got entities=%d relationships=%d tombstones=%d", len(batch.Entities), len(batch.Relationships), len(batch.Tombstones))
	}
	if got, want := keysetPosition(t, batch.NextCursor), keysetPosition(t, first.cursor); got != want {
		t.Fatalf("the reconcile batch moved the checkpoint: %s, want %s", got, want)
	}
}
