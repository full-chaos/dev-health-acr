package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
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
func TestOwnershipCollapseDoesNotMergeAcrossProviders(t *testing.T) {
	ctx := context.Background()
	fixture := newOwnershipFixture(t, ctx)

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
func TestOwnershipWindowTakesTheLatestAssertion(t *testing.T) {
	ctx := context.Background()
	fixture := newOwnershipFixture(t, ctx)

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
}

func newOwnershipFixture(t *testing.T, ctx context.Context) *ownershipFixture {
	t.Helper()
	const orgID = "30000000-0000-4000-8000-000000000003"
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
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

	source, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatalf("new teams/projects source: %v", err)
	}
	return &ownershipFixture{orgID: orgID, source: source}
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
