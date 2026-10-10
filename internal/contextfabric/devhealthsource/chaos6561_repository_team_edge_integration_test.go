package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// chaos6561ConvergePageBound bounds every convergence loop below. The
// fixture's rows fit in a handful of pages; the bound has no early exit on
// an observed edge, and each loop asserts convergence (available=false) was
// reached, so a producer that never stops re-emitting FAILS instead of
// running out the bound (the file-level TERMINATION RULE).
const chaos6561ConvergePageBound = 50

// converge pages the source from cursor until it reports no batch, and
// returns every repository->team edge for teamID seen on the way plus the
// converged cursor.
func (f *ownershipFixture) convergeRepositoryTeamEdges(t *testing.T, ctx context.Context, cursor, teamID string) ([]contractsv1.ContextFabricRelationshipProjection, string) {
	t.Helper()
	var edges []contractsv1.ContextFabricRelationshipProjection
	for page := 0; page < chaos6561ConvergePageBound; page++ {
		batch, available, err := f.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{
			OrgID: f.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if !available {
			return edges, cursor
		}
		cursor = batch.NextCursor
		for _, relationship := range batch.Relationships {
			if relationship.Type == contractsv1.ContextFabricRelationshipOwnedByTeam &&
				relationship.From.Kind == contractsv1.ContextFabricSubjectRepository &&
				relationship.To.CanonicalID == contextfabric.TeamCanonicalID(teamID) {
				edges = append(edges, relationship)
			}
		}
	}
	t.Fatalf("source did not converge within %d pages", chaos6561ConvergePageBound)
	return nil, ""
}

// subRepositoryTeamEdgeReemittedWhenReposRowArrives is the codex P1 on
// CHAOS-6561: an ownership row whose repo_id has no repos row yet projects an
// edge scoped to the orphan sentinel. When the repos row arrives LATER, the
// ownership row itself does not change -- so a watermark over
// team_repo_ownership.updated_at alone never re-reads the group, and the edge
// keeps its fail-closed sentinel scope forever (repository-scoped graph reads
// deny it). The repos row's last_synced is part of the watermark now, so the
// arrival re-emits the edge with the real slug -- once, not on every tick.
func subRepositoryTeamEdgeReemittedWhenReposRowArrives(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	const (
		teamID = "TEAM-GITHUB"
		repoID = "6561a000-0000-4000-8000-000000000001"
		slug   = "acme/late-arriving-repo"
	)
	// Whole seconds: clickhouse-go's positional `?` binding renders a
	// time.Time at second precision (bind.go format(tz, Seconds, v)), so a
	// sub-second fixture value would not be what the table stores.
	ownedAt := time.Now().UTC().Truncate(time.Second)
	mustExec(t, ctx, fixture.direct,
		`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		fixture.orgID, "github", teamID, repoID, slug, "exact", "native", uint8(1), uint16(1), int32(1), ownedAt, nil, ownedAt)

	first, cursor := fixture.convergeRepositoryTeamEdges(t, ctx, "", teamID)
	if len(first) != 1 {
		t.Fatalf("first projection: want exactly one repository->team edge, got %d: %+v", len(first), first)
	}
	if got := first[0].Authorization.RepositorySlugs; len(got) != 1 || got[0] != "acr-context-fabric:orphaned-repository" {
		t.Fatalf("first projection (no repos row): edge scoped as %v, want the orphaned-repository sentinel", got)
	}

	// The repos row arrives after the checkpoint; team_repo_ownership is not
	// touched.
	syncedAt := ownedAt.Add(time.Minute)
	mustExec(t, ctx, fixture.direct,
		`INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`,
		repoID, slug, nil, ownedAt, nil, syncedAt, fixture.orgID, "github")

	second, cursor := fixture.convergeRepositoryTeamEdges(t, ctx, cursor, teamID)
	if len(second) != 1 {
		t.Fatalf("after the repos row arrived: want the edge re-emitted exactly once, relationships=%+v", second)
	}
	edge := second[0]
	if edge.RelationshipID != first[0].RelationshipID {
		t.Fatalf("re-emitted edge id %q, want the same edge %q", edge.RelationshipID, first[0].RelationshipID)
	}
	if got := edge.Authorization.RepositorySlugs; len(got) != 1 || got[0] != slug {
		t.Fatalf("re-emitted edge RepositorySlugs=%v, want [%s]", got, slug)
	}
	if got := edge.Authorization.TeamIDs; len(got) != 1 || got[0] != teamID {
		t.Fatalf("re-emitted edge TeamIDs=%v, want [%s]", got, teamID)
	}
	if !edge.ObservedAt.Equal(syncedAt) {
		t.Fatalf("re-emitted edge ObservedAt=%s, want the repos row's last_synced %s (the later of the two)", edge.ObservedAt, syncedAt)
	}

	// No infinite re-emit: nothing changed, so the next read from the
	// converged cursor carries no edge for this team.
	third, _ := fixture.convergeRepositoryTeamEdges(t, ctx, cursor, teamID)
	if len(third) != 0 {
		t.Fatalf("a read after convergence re-emitted the edge again: %+v", third)
	}
}

// subRepositoryTeamEdgeFollowsTheOpenRowOfAFact: a fact is current when an
// open row exists for its key. The ownership writers keep the EARLIEST open
// row and close later duplicates, but a duplicate is closed only on a run with
// proof of its end, so two open rows of one fact can coexist; the edge then
// takes its attributes from the earliest open row. A fact with no open row
// ends at its latest close.
func subRepositoryTeamEdgeFollowsTheOpenRowOfAFact(t *testing.T, ctx context.Context, fixture *ownershipFixture) {
	const teamID = "TEAM-GITHUB"
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	seedRepo := func(id, slug string) {
		mustExec(t, ctx, fixture.direct,
			`INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider) VALUES (?,?,?,?,?,?,?,?)`,
			id, slug, nil, base, nil, base, fixture.orgID, "github")
	}
	own := func(repoID, slug string, priority int32, validFrom time.Time, validTo any) {
		mustExec(t, ctx, fixture.direct,
			`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			fixture.orgID, "github", teamID, repoID, slug, "exact", "native", uint8(1), uint16(1), priority, validFrom, validTo, validFrom)
	}
	const (
		dupID    = "9021a000-0000-4000-8000-000000000001"
		closedID = "9021a000-0000-4000-8000-000000000002"
	)
	seedRepo(dupID, "acme/two-open-rows")
	own(dupID, "acme/two-open-rows", 1, base, nil)
	own(dupID, "acme/two-open-rows", 9, base.Add(time.Hour), nil)
	own(dupID, "acme/two-open-rows", 5, base.Add(2*time.Hour), base.Add(3*time.Hour))
	seedRepo(closedID, "acme/no-open-row")
	own(closedID, "acme/no-open-row", 1, base, base.Add(5*time.Hour))
	own(closedID, "acme/no-open-row", 1, base.Add(time.Hour), base.Add(3*time.Hour))

	edges, _ := fixture.convergeRepositoryTeamEdges(t, ctx, "", teamID)
	byLabel := map[string]contractsv1.ContextFabricRelationshipProjection{}
	for _, edge := range edges {
		byLabel[edge.From.Label] = edge
	}
	if len(edges) != 2 {
		t.Fatalf("edges = %d, want 2 (one per fact): %+v", len(edges), edges)
	}
	open := byLabel["acme/two-open-rows"]
	if open.ValidTo != nil {
		t.Errorf("two open rows and a later closed one: ValidTo = %v, want nil (an open row exists)", open.ValidTo)
	}
	if got := open.Properties["priority"]; got.Integer == nil || *got.Integer != 1 {
		t.Errorf("two open rows: priority = %+v, want 1 from the EARLIEST open row", got)
	}
	closed := byLabel["acme/no-open-row"]
	if closed.ValidTo == nil || !closed.ValidTo.Equal(base.Add(5*time.Hour)) {
		t.Errorf("no open row: ValidTo = %v, want the latest close %v", closed.ValidTo, base.Add(5*time.Hour))
	}
}
