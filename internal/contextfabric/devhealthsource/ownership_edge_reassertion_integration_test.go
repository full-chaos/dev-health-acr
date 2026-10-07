package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
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

// The wider cursor re-reads an ownership group when any project of its
// provider changes, but the edge's own ObservedAt stays what it was: the later
// of the ownership row's and its reachable projects' stamps. An unrelated
// project's write must not restamp every edge of the provider.
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
	drainUntil(t, ctx, fixture, cursor, collect)
	if len(observed) < 2 {
		t.Fatal("the edge was not re-read after a project of its provider changed; the wider cursor is not doing its job")
	}
	for _, at := range observed {
		if !at.Equal(t0) {
			t.Fatalf("an unrelated project write moved the edge's ObservedAt to %v, want %v", at, t0)
		}
	}
}
