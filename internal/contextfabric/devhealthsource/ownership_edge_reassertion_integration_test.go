package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// A key shared by two projects suppresses the ownership edge; once the
// colliding project is re-keyed the key is unambiguous again and the edge must
// come back on an incremental tick. The re-keyed project has left the key's
// partition, so nothing in the group's own rows moved: the cursor has to see
// the project change on its own.
func TestOwnershipEdgeReturnsWhenAnAmbiguousKeyResolves(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	fixture := newOwnershipFixture(t, ctx, query, direct, "46280000-0000-4000-8000-000000000001")
	t0 := ownershipLaterAssertion.Add(96 * time.Hour)
	project := func(id, key string, at time.Time) {
		mustExec(t, ctx, fixture.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced) VALUES (?, ?, 'github', ?, ?, 1, 'started', '', ?, ?)`,
			id, fixture.orgID, key, id, at, at)
	}
	project("REASSERT-P", "REASSERT-KEY", t0)
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at, last_synced) VALUES (?, 'github', 'TEAM-GITHUB', ?, ?, 'native', ?, NULL, ?, ?)`,
		fixture.orgID, "REASSERT-KEY", "REASSERT-KEY", ownershipFirstSeen, t0, t0)
	edge := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "REASSERT-P", "TEAM-GITHUB", "native")
	cursor, ok := drainUntil(t, ctx, fixture, "", func(b contextfabric.ProjectionBatch) bool { return hasRelationship(b, edge) })
	if !ok {
		t.Fatalf("%q was never projected", edge)
	}

	project("REASSERT-Q", "REASSERT-KEY", t0.Add(time.Hour))
	// Every batch of the ambiguous window is inspected, the tombstone's own
	// batch included: the edge is never asserted in any of them.
	retracted, reasserted := false, false
	cursor, _ = drainUntil(t, ctx, fixture, cursor, func(b contextfabric.ProjectionBatch) bool {
		retracted = retracted || hasTombstone(b, edge)
		reasserted = reasserted || hasRelationship(b, edge)
		return false
	})
	if !retracted {
		t.Fatalf("%q was not retracted while the key is ambiguous", edge)
	}
	if reasserted {
		t.Fatalf("%q was asserted in a batch while the key is still ambiguous", edge)
	}

	project("REASSERT-Q", "REASSERT-OTHER", t0.Add(2*time.Hour))
	if _, back := drainUntil(t, ctx, fixture, cursor, func(b contextfabric.ProjectionBatch) bool { return hasRelationship(b, edge) }); !back {
		t.Fatalf("%q did not come back after the ambiguity resolved", edge)
	}
}

// An unrelated project's write must not restamp the edges of its provider.
// The ownership edges page the stored groups: the refresh rebuilds every group
// and stores only a group whose content changed, and a write that changes no
// group's resolution changes no group. So the edge is not read again, and its
// ObservedAt stays the later of the ownership row's and its reachable projects'
// stamps. (The group statement the stored groups replaced keyed the group on
// the provider-wide newest project, so every write of the provider re-read
// it.) A write that does change a group's resolution is stored and read: see
// TestOwnershipEdgeReturnsWhenAnAmbiguousKeyResolves.
func TestOwnershipEdgeObservedAtIgnoresAnUnrelatedProjectWrite(t *testing.T) {
	ctx := context.Background()
	query, direct := orgIsolationClickHouseFixture(t)
	fixture := newOwnershipFixture(t, ctx, query, direct, "46280000-0000-4000-8000-000000000002")
	t0 := ownershipLaterAssertion.Add(120 * time.Hour)
	project := func(id, key string, at time.Time) {
		mustExec(t, ctx, fixture.direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced) VALUES (?, ?, 'github', ?, ?, 1, 'started', '', ?, ?)`,
			id, fixture.orgID, key, id, at, at)
	}
	project("OBS-P", "OBS-KEY", t0)
	mustExec(t, ctx, fixture.direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at, last_synced) VALUES (?, 'github', 'TEAM-GITHUB', ?, ?, 'native', ?, NULL, ?, ?)`,
		fixture.orgID, "OBS-KEY", "OBS-KEY", ownershipFirstSeen, t0, t0)
	edge := devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", "OBS-P", "TEAM-GITHUB", "native")
	var observed []time.Time
	collect := func(b contextfabric.ProjectionBatch) bool {
		for _, r := range b.Relationships {
			if r.RelationshipID == edge {
				observed = append(observed, r.ObservedAt)
			}
		}
		return false
	}
	cursor, _ := drainUntil(t, ctx, fixture, "", collect)
	if len(observed) != 1 || !observed[0].Equal(t0) {
		t.Fatalf("first projection ObservedAt = %v, want exactly the seeded stamp %v", observed, t0)
	}

	project("OBS-UNRELATED", "OBS-OTHER", t0.Add(3*time.Hour))
	unrelatedID, _, err := identity.Derive(identity.KindProject, []string{"github", "OBS-UNRELATED"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	unrelated := false
	drainUntil(t, ctx, fixture, cursor, func(b contextfabric.ProjectionBatch) bool {
		for _, e := range b.Entities {
			unrelated = unrelated || e.Subject.CanonicalID == unrelatedID
		}
		return collect(b)
	})
	if !unrelated {
		t.Fatal("premise: the unrelated project was never projected, so the walk did not pass its write")
	}
	var stored uint64
	if err := fixture.direct.QueryRow(ctx, `SELECT count() FROM project_team_ownership_groups_log WHERE org_id = ? AND row_key = ?`,
		fixture.orgID, identity.JoinSegments("github", "OBS-P", "TEAM-GITHUB", "native")).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Errorf("the unrelated write stored the group again: %d log rows, want 1", stored)
	}
	for _, at := range observed {
		if !at.Equal(t0) {
			t.Fatalf("an unrelated project write moved the edge's ObservedAt to %v, want %v", at, t0)
		}
	}
	if len(observed) != 1 {
		t.Errorf("the edge was read again after an unrelated project write (%d times); its stored group did not change", len(observed))
	}
}
