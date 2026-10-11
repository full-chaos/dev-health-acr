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

	now = now.Add(devhealthsource.TeamOwnershipReconcilePeriodForTest + time.Minute)
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
