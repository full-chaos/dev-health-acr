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
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
	logs     string
}

func chaos7139Run(t *testing.T, ctx context.Context, f *ownershipFixture) chaos7139Result {
	t.Helper()
	logged := &bytes.Buffer{}
	f.source.WithLogger(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	res := chaos7139Result{entities: map[string]contractsv1.ContextFabricEntityProjection{}, edges: map[string]int{}}
	cursor := ""
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
				res.edges[strings.TrimPrefix(r.To.CanonicalID, "team:")]++
			}
		}
	}
	res.logs = logged.String()
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
	if strings.Contains(res.logs, "team_id=team-small") {
		t.Errorf("small team must not be warned about:\n%s", res.logs)
	}
}

// A team above the widened bound (5000) fails closed with a DISTINCT reason;
// only its own entity and edges drop -- valid teams sharing the pages keep
// theirs (a mixed page does not lose its valid rows).
func subCHAOS7139TeamAboveEntityBoundFailsClosedAlone(t *testing.T, ctx context.Context, f *ownershipFixture) {
	at := time.Now().UTC().Truncate(time.Second)
	over := contractsv1.ContextFabricEntityAuthorizationRepositoryMax + 1
	chaos7139SeedTeam(t, ctx, f, "team-huge", "acme/huge-", over, at)
	chaos7139SeedTeam(t, ctx, f, "team-w450", "acme/w450-", 450, at)
	chaos7139SeedTeam(t, ctx, f, "team-small", "acme/small-", 3, at)
	res := chaos7139Run(t, ctx, f)
	if _, ok := res.entities["team-huge"]; ok {
		t.Fatalf("team-huge (%d repos) must fail closed, but its entity was projected", over)
	}
	if !strings.Contains(res.logs, "quarantine_reason=authorization_repositories_exceeded") {
		t.Errorf("missing distinct quarantine reason:\n%s", res.logs)
	}
	if !strings.Contains(res.logs, fmt.Sprintf("owned_repositories=%d", over)) || !strings.Contains(res.logs, "quarantined_fail_closed=true") {
		t.Errorf("missing fail-closed Warn with team id and count:\n%s", res.logs)
	}
	for team, want := range map[string]int{"team-w450": 450, "team-small": 3} {
		if _, ok := res.entities[team]; !ok {
			t.Errorf("%s: valid team entity lost because of another team's quarantine", team)
		}
		if got := res.edges[team]; got != want {
			t.Errorf("%s: %d edges, want %d", team, got, want)
		}
	}
	// NOT asserted: team-huge's edges. Quarantine drops only the edges that
	// share the entity's page (endpoint_entity_quarantined); edges on other
	// pages still reach the graph against a never-written team node. That
	// residual pre-dates this change and only affects a team above 5000
	// repositories (recorded in the PR RISK-NOTES).
}
