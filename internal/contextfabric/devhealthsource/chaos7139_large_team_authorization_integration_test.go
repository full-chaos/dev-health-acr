package devhealthsource_test

// CHAOS-7139: a team owning more than 200 repositories must keep its team
// entity and every one of its OWNED_BY_TEAM edges. Before the fix the team's
// authorization_repositories list (one raw repo_full_name per owned
// repository) breached the generic 200 contract bound, the entity was
// quarantined, and every edge on that page was dropped with it
// (endpoint_entity_quarantined). Assertions are on what the graph RECEIVES
// from NextProjectionBatch on a real ClickHouse.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func chaos7139SeedTeam(t *testing.T, ctx context.Context, f *ownershipFixture, team, prefix string, n int, at time.Time) {
	t.Helper()
	chaos7119Team(t, ctx, f, team, at)
	mustExec(t, ctx, f.direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider)
SELECT generateUUIDv4(), concat(?, toString(number)), NULL, ?, NULL, ?, ?, 'github'
FROM numbers(?)`, prefix, at, at, f.orgID, n)
	mustExec(t, ctx, f.direct, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at)
SELECT ?, 'github', ?, NULL, concat(?, toString(number)), 'exact', 'native', 1, 100, 0, ?, NULL, ?
FROM numbers(?)`, f.orgID, team, prefix, at, at, n)
}

type chaos7139Result struct {
	entities map[string]contractsv1.ContextFabricEntityProjection
	edges    map[string]int
	// openEdgeSlugs (CHAOS-7130): per team, the repository slugs of its OPEN
	// OWNED_BY_TEAM edges, the set the team authorization list must equal.
	openEdgeSlugs map[string][]string
	logs          string
	cursor        string
}

func chaos7139Run(t *testing.T, ctx context.Context, f *ownershipFixture) chaos7139Result {
	t.Helper()
	return chaos7139RunFrom(t, ctx, f, "")
}

func chaos7139RunFrom(t *testing.T, ctx context.Context, f *ownershipFixture, cursor string) chaos7139Result {
	t.Helper()
	logged := &bytes.Buffer{}
	f.source.WithLogger(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	res := chaos7139Result{entities: map[string]contractsv1.ContextFabricEntityProjection{}, edges: map[string]int{}, openEdgeSlugs: map[string][]string{}}
	for page := 0; ; page++ {
		if page > 200 {
			t.Fatalf("source did not converge within 200 pages")
		}
		batch, available, err := f.source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{
			OrgID: f.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if !available {
			break
		}
		assertUniqueRelationshipIDs(t, batch)
		cursor = batch.NextCursor
		for _, e := range batch.Entities {
			if e.Subject.Kind == contractsv1.ContextFabricSubjectTeam {
				res.entities[strings.TrimPrefix(e.Subject.CanonicalID, "team:")] = e
			}
		}
		for _, r := range batch.Relationships {
			if r.Type == contractsv1.ContextFabricRelationshipOwnedByTeam && r.From.Kind == contractsv1.ContextFabricSubjectRepository {
				team := strings.TrimPrefix(r.To.CanonicalID, "team:")
				res.edges[team]++
				// Open = currently valid (valid_from not in the future) and not
				// closed; the orphaned-repository sentinel edge names no
				// repository and is not an authorization repository.
				now := time.Now().UTC()
				if r.ValidTo == nil && (r.ValidFrom == nil || !r.ValidFrom.After(now)) && len(r.Authorization.RepositorySlugs) == 1 &&
					r.Authorization.RepositorySlugs[0] != "acr-context-fabric:orphaned-repository" {
					res.openEdgeSlugs[team] = append(res.openEdgeSlugs[team], r.Authorization.RepositorySlugs[0])
				}
			}
		}
	}
	res.logs = logged.String()
	res.cursor = cursor
	return res
}

// Teams of 201 and 450 repositories keep their entity (full authorization
// list) and every edge, across pages; a small team beside them is unaffected.
func subCHAOS7139LargeTeamsKeepEntityAndAllEdges(t *testing.T, ctx context.Context, f *ownershipFixture) {
	at := time.Now().UTC().Truncate(time.Second)
	chaos7139SeedTeam(t, ctx, f, "team-w201", "acme/w201-", 201, at)
	chaos7139SeedTeam(t, ctx, f, "team-w450", "acme/w450-", 450, at)
	chaos7139SeedTeam(t, ctx, f, "team-small", "acme/small-", 3, at)
	res := chaos7139Run(t, ctx, f)
	for team, want := range map[string]int{"team-w201": 201, "team-w450": 450, "team-small": 3} {
		e, ok := res.entities[team]
		if !ok {
			t.Fatalf("%s: team entity was not projected (quarantined?)\n%s", team, res.logs)
		}
		if got := len(e.Authorization.RepositorySlugs); got != want {
			t.Errorf("%s: authorization repositories %d, want %d (fail-closed list must be complete, never truncated)", team, got, want)
		}
		if got := res.edges[team]; got != want {
			t.Errorf("%s: %d OWNED_BY_TEAM edges projected, want %d", team, got, want)
		}
	}
	if strings.Contains(res.logs, "endpoint_entity_quarantined") || strings.Contains(res.logs, "contract_bound_violation") {
		t.Errorf("unexpected quarantine:\n%s", res.logs)
	}
	for _, want := range []string{"team_id=team-w201", "owned_repositories=201", "team_id=team-w450", "owned_repositories=450"} {
		if !strings.Contains(res.logs, want) {
			t.Errorf("missing large-team Warn field %q:\n%s", want, res.logs)
		}
	}
	// Once per run: a multi-page catch-up must not repeat the Warn per page.
	if n := strings.Count(res.logs, "owned_repositories=450"); n != 1 {
		t.Errorf("large-team Warn for team-w450 emitted %d times, want exactly once per run:\n%s", n, res.logs)
	}
	if strings.Contains(res.logs, "team_id=team-small") {
		t.Errorf("small team must not be warned about:\n%s", res.logs)
	}
}

// A team above the entity bound (5000) is NOT quarantined: its entity is
// projected with the dedicated fail-closed sentinel (so no edge dangles and no
// cursor state is involved), every edge is emitted, the Warn names the team,
// and a repository-restricted principal whose repository has an OWNED_BY_TEAM
// edge to it is still denied the team node. Valid teams beside it are intact.
func subCHAOS7139TeamAboveEntityBoundIsProjectedFailClosed(t *testing.T, ctx context.Context, f *ownershipFixture) {
	at := time.Now().UTC().Truncate(time.Second)
	over := contractsv1.ContextFabricEntityAuthorizationRepositoryMax + 1
	chaos7139SeedTeam(t, ctx, f, "team-huge", "acme/huge-", over, at)
	chaos7139SeedTeam(t, ctx, f, "team-w450", "acme/w450-", 450, at)
	res := chaos7139Run(t, ctx, f)
	huge, ok := res.entities["team-huge"]
	if !ok {
		t.Fatalf("team-huge (%d repos) entity must be projected (fail closed via sentinel), not quarantined\n%s", over, res.logs)
	}
	if got := huge.Authorization.RepositorySlugs; len(got) != 1 || got[0] != devhealthsource.OverBoundTeamOwnershipSentinelForTest() {
		t.Fatalf("team-huge authorization repositories = %d entries, want exactly the over-bound sentinel", len(got))
	}
	if got := res.edges["team-huge"]; got != over {
		t.Errorf("team-huge: %d edges, want all %d (a projected node, edges must not be withheld)", got, over)
	}
	if strings.Contains(res.logs, "quarantine_reason=") {
		t.Errorf("no item may be quarantined:\n%s", res.logs)
	}
	for _, want := range []string{"team_id=team-huge", fmt.Sprintf("owned_repositories=%d", over), "fail_closed_sentinel=true", "reason=authorization_repositories_exceeded"} {
		if !strings.Contains(res.logs, want) {
			t.Errorf("missing loud Warn field %q:\n%s", want, res.logs)
		}
	}
	if n := strings.Count(res.logs, fmt.Sprintf("owned_repositories=%d", over)); n != 1 {
		t.Errorf("over-bound Warn emitted %d times, want once per run", n)
	}
	// A repository-restricted principal scoped to one of team-huge's own
	// repositories must be denied the team node (fail closed) -- and would be
	// admitted by the real list, which is the red plant.
	restricted := storage.Principal{OrgID: f.orgID, RepositoryScopes: []string{"acme/huge-7"}}
	attrs := map[string]interface{}{"authorization_repositories": huge.Authorization.RepositorySlugs}
	if graphrank.AuthorizedAttributes(restricted, contextfabric.RequestedScope{}, attrs) {
		t.Errorf("a restricted principal owning acme/huge-7 was admitted to the over-bound team node")
	}
	if w := res.entities["team-w450"]; len(w.Authorization.RepositorySlugs) != 450 || res.edges["team-w450"] != 450 {
		t.Errorf("team-w450 beside the over-bound team lost data: %d slugs, %d edges", len(w.Authorization.RepositorySlugs), res.edges["team-w450"])
	}
}

// Cross-back: a team at 5001 (sentinel) that drops to 5000 by an ownership
// close is re-projected with its REAL list, edges intact. Proves the team
// watermark advances on an ownership-only change (no team row touched).
func subCHAOS7139TeamCrossesBackUnderTheBound(t *testing.T, ctx context.Context, f *ownershipFixture) {
	seedAt := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	over := contractsv1.ContextFabricEntityAuthorizationRepositoryMax + 1
	chaos7139SeedTeam(t, ctx, f, "team-cross", "acme/cross-", over, seedAt)
	first := chaos7139Run(t, ctx, f)
	if got := first.entities["team-cross"].Authorization.RepositorySlugs; len(got) != 1 || got[0] != devhealthsource.OverBoundTeamOwnershipSentinelForTest() {
		t.Fatalf("precondition: team-cross must start on the sentinel, got %d entries", len(got))
	}
	if first.edges["team-cross"] != over {
		t.Fatalf("precondition: %d edges, want %d", first.edges["team-cross"], over)
	}
	// Close ONE ownership: the same row (same valid_from, so the newer
	// updated_at replaces it) now carries a valid_to in the past, leaving no
	// open row for the fact; the team row itself is untouched.
	closedFrom, closedTo, updated := seedAt, seedAt.Add(2*time.Minute), time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	mustExec(t, ctx, f.direct, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.orgID, "github", "team-cross", nil, "acme/cross-0", "exact", "native", uint8(1), uint16(100), int32(0), closedFrom, closedTo, updated)
	second := chaos7139RunFrom(t, ctx, f, first.cursor)
	e, ok := second.entities["team-cross"]
	if !ok {
		t.Fatalf("team-cross was not re-projected after the ownership close (team watermark did not advance)\n%s", second.logs)
	}
	if got := len(e.Authorization.RepositorySlugs); got != contractsv1.ContextFabricEntityAuthorizationRepositoryMax {
		t.Errorf("after crossing back: %d authorization repositories, want the real list of %d", got, contractsv1.ContextFabricEntityAuthorizationRepositoryMax)
	}
	for _, s := range e.Authorization.RepositorySlugs {
		if s == devhealthsource.OverBoundTeamOwnershipSentinelForTest() {
			t.Fatal("the over-bound sentinel survived crossing back under the bound")
		}
	}
	// Edges were never withheld: the first run already emitted all of them.
	if first.edges["team-cross"] != over {
		t.Errorf("edges lost")
	}
}

// A team at EXACTLY the entity bound keeps its entity and every edge; one
// repository more (the case above) loses both. Pins both sides of the edge
// suppression's threshold on real ClickHouse.
func subCHAOS7139TeamAtEntityBoundKeepsEveryEdge(t *testing.T, ctx context.Context, f *ownershipFixture) {
	at := time.Now().UTC().Truncate(time.Second)
	n := contractsv1.ContextFabricEntityAuthorizationRepositoryMax
	chaos7139SeedTeam(t, ctx, f, "team-max", "acme/max-", n, at)
	res := chaos7139Run(t, ctx, f)
	e, ok := res.entities["team-max"]
	if !ok {
		t.Fatalf("team-max (%d repos, exactly the bound) entity not projected\n%s", n, res.logs)
	}
	if got := len(e.Authorization.RepositorySlugs); got != n {
		t.Errorf("authorization repositories %d, want %d", got, n)
	}
	if got := res.edges["team-max"]; got != n {
		t.Errorf("%d edges, want %d", got, n)
	}
	if strings.Contains(res.logs, devhealthsource.OverBoundTeamOwnershipSentinelForTest()) || strings.Contains(res.logs, "fail_closed_sentinel=true") {
		t.Errorf("a team AT the bound must not be sentinel-scoped:\n%s", res.logs)
	}
}
