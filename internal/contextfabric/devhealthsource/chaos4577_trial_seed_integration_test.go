package devhealthsource_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// TestTrialDataSeedTeamRepoOwnershipSatisfiesCurrentPredicate is CHAOS-4577's
// proof (c): the EXACT SQL text `deploy/local/templates/team-repo-ownership-seed.sql`
// renders (the same file `trial-data.sh seed-team-repo-ownership` sed-substitutes
// and pipes into clickhouse-client) executed against a real, production-schema
// ClickHouse -- proving both that the statement is syntactically valid for
// this server version and that its rows are readable by queryTeams'
// CURRENT predicate (teams_projects.go's ownedRepositoriesJoinSQL:
// `valid_from <= now64(3)` and the latest version per (team_id,
// repo_full_name) has `valid_to IS NULL`), through the REAL producer
// (devhealthsource.NewTeamsProjectsSource -> NextProjectionBatch), not a
// hand-rolled query. Run against a THROWAWAY container database (dropped
// automatically at test cleanup) -- never the shared trial/kiac plane.
func TestTrialDataSeedTeamRepoOwnershipSatisfiesCurrentPredicate(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)

	const orgID = "40000000-0000-4000-8000-000000000001"
	seedTeam := func(id string) {
		if err := direct.Exec(ctx, `INSERT INTO teams VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, id+" name", "", time.Now().UTC(), orgID, "github", id, []string{}, uint8(1)); err != nil {
			t.Fatalf("seed team %s: %v", id, err)
		}
	}
	// The three real team_id values the seed SQL file assigns ownership to
	// (org 70d529e0's actual teams, per the CHAOS-4577 investigation) -- a
	// team row must exist for queryTeams' FROM teams AS tm FINAL to emit a
	// candidate at all; team_repo_ownership alone projects nothing.
	seedTeam("CHAOS")
	seedTeam("gl:full.chaos")
	seedTeam("gh:ops-team")

	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	seedSQLPath := filepath.Join(repoRoot, "deploy", "local", "templates", "team-repo-ownership-seed.sql")
	raw, err := os.ReadFile(seedSQLPath)
	if err != nil {
		t.Fatalf("read %s: %v (this test's own repo-root resolution may be stale)", seedSQLPath, err)
	}
	// Exactly what trial-data.sh's cmd_seed_team_repo_ownership does:
	// sed "s|__ORG_ID__|$org_id|g".
	rendered := strings.ReplaceAll(string(raw), "__ORG_ID__", orgID)
	if strings.Contains(rendered, "__ORG_ID__") {
		t.Fatal("rendered seed SQL still contains __ORG_ID__ -- substitution did not cover every occurrence")
	}
	// One statement (a single multi-row INSERT), executed verbatim including
	// its leading comment block -- ClickHouse's own SQL comment syntax, no
	// stripping needed. clickhouse-go's Exec, unlike the CLI's --multiquery,
	// expects one statement per call; the seed file is written as exactly one.
	if err := direct.Exec(ctx, rendered); err != nil {
		t.Fatalf("execute rendered team-repo-ownership-seed.sql: %v\n%s", err, rendered)
	}

	source, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatalf("new teams/projects source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("NextProjectionBatch: %v", err)
	}
	if !available {
		t.Fatal("NextProjectionBatch reported no batch available, want the three seeded teams")
	}

	want := map[string][]string{
		"team:CHAOS": {
			"full-chaos/cloudymccloudflare", "full-chaos/dev-health-acr", "full-chaos/dev-health-deploy",
			"full-chaos/dev-health-ops", "full-chaos/dev-health-web", "full-chaos/script-manifest",
		},
		"team:gl:full.chaos": {"full.chaos/chaos-ops", "full.chaos/dev-health-ops"},
		"team:gh:ops-team":   {"full-chaos/dev-health-acr"},
	}
	seen := map[string]bool{}
	for _, entity := range batch.Entities {
		canonicalID := entity.Subject.CanonicalID
		wantSlugs, ok := want[canonicalID]
		if !ok {
			continue
		}
		seen[canonicalID] = true
		got := append([]string(nil), entity.Authorization.RepositorySlugs...)
		sort.Strings(got)
		sortedWant := append([]string(nil), wantSlugs...)
		sort.Strings(sortedWant)
		if len(got) != len(sortedWant) {
			t.Errorf("%s RepositorySlugs = %v, want %v", canonicalID, got, sortedWant)
			continue
		}
		for i := range got {
			if got[i] != sortedWant[i] {
				t.Errorf("%s RepositorySlugs = %v, want %v", canonicalID, got, sortedWant)
				break
			}
		}
		for _, slug := range got {
			if slug == "acr-context-fabric:no-team-repository-ownership" {
				t.Errorf("%s RepositorySlugs still carries the CHAOS-4390 sentinel -- the seed did not satisfy the CURRENT predicate", canonicalID)
			}
		}
	}
	for canonicalID := range want {
		if !seen[canonicalID] {
			t.Errorf("batch never projected %s -- want it among the %d entities: %#v", canonicalID, len(batch.Entities), batch.Entities)
		}
	}
}
