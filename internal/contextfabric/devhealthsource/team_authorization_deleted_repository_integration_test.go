package devhealthsource_test

// A team's authorization_repositories list is built from resolved ownership.
// When a repos row is hard-deleted (a ClickHouse mutation; repos has no
// soft-delete column) its slug no longer resolves, so an incremental resume
// must re-project the team WITHOUT that slug. Asserted on what
// NextProjectionBatch hands the graph, on a real ClickHouse.

import (
	"context"
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
	if _, ok := next(two.NextCursor); ok {
		t.Fatalf("a finished reconcile pass served another batch")
	}
}
