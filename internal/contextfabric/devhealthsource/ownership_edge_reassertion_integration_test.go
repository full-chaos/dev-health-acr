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
	cursor, ok = drainUntil(t, ctx, fixture, cursor, func(b contextfabric.ProjectionBatch) bool { return hasTombstone(b, edge) })
	if !ok {
		t.Fatalf("%q was not retracted while the key is ambiguous", edge)
	}
	if _, back := drainUntil(t, ctx, fixture, cursor, func(b contextfabric.ProjectionBatch) bool { return hasRelationship(b, edge) }); back {
		t.Fatalf("%q was re-asserted while the key is still ambiguous", edge)
	}

	project("REASSERT-Q", "REASSERT-OTHER", t0.Add(2*time.Hour))
	if _, back := drainUntil(t, ctx, fixture, cursor, func(b contextfabric.ProjectionBatch) bool { return hasRelationship(b, edge) }); !back {
		t.Fatalf("%q did not come back after the ambiguity resolved", edge)
	}
}
