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
)

func subTeamAuthorizationDropsAHardDeletedRepository(t *testing.T, ctx context.Context, f *ownershipFixture) {
	ownedAt := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
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

	second := chaos7139RunFrom(t, ctx, f, first.cursor)
	if _, ok := second.entities["team-del"]; !ok {
		t.Fatalf("team-del was not re-projected after its repos row was deleted (watermark fell behind the cursor)")
	}
	if got := chaos7130List(second, "team-del"); !reflect.DeepEqual(got, []string{"acme/kept"}) {
		t.Errorf("after the delete: %v, want [acme/kept]", got)
	}
}
