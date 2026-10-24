package devhealthsource_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// TestLiveNodeCountsEqualTheEntitiesTheProducerEmits seeds a work item, a
// deployment and a CI run whose canonical id fits beside one whose id exceeds
// the natural-key bound (the producer omits it), drains the real producer, and
// requires each source node count to equal the entities emitted.
func TestLiveNodeCountsEqualTheEntitiesTheProducerEmits(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	seed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	long := strings.Repeat("X", 300)
	seed("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, 'acme/nodes', 'github', ?)`, edgeParityRepo, edgeParityOrg, base)
	for _, id := range []string{"W-1", "W-" + long} {
		seed("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced) VALUES (?, toUUID(?), ?, 't', 'open', '', '', 'github', '', ?, ?)`,
			id, edgeParityRepo, edgeParityOrg, base, base)
	}
	for _, id := range []string{"D-1", "D-" + long} {
		seed("deployment", `INSERT INTO deployments (repo_id, org_id, deployment_id, status, environment, deployed_at, started_at, last_synced) VALUES (toUUID(?), ?, ?, 'success', 'prod', ?, ?, ?)`,
			edgeParityRepo, edgeParityOrg, id, base, base, base)
	}
	for _, id := range []string{"R-1", "R-" + long} {
		seed("ci run", `INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) VALUES (toUUID(?), ?, 'success', ?, ?, ?)`,
			edgeParityRepo, id, base, base, edgeParityOrg)
	}

	for _, id := range []string{"P-1", "P-" + long} {
		seed("project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, 'p', '', 'github', 'backlog', '', 1, ?)`, id, edgeParityOrg, base)
	}

	teams, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatal(err)
	}
	projectCounts, err := teams.ProjectionSourceCounts(ctx, edgeParityOrg)
	if err != nil {
		t.Fatalf("ProjectionSourceCounts (teams/projects): %v", err)
	}
	projectsEmitted := int64(0)
	cursor := ""
	for page := 0; ; page++ {
		if page > 500 {
			t.Fatal("the teams/projects source did not drain")
		}
		batch, ok, err := teams.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: edgeParityOrg, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("read teams/projects page %d: %v", page, err)
		}
		if !ok {
			break
		}
		for _, e := range batch.Entities {
			if string(e.Subject.Kind) == identity.KindProject {
				projectsEmitted++
			}
		}
		cursor = batch.NextCursor
	}
	if projectsEmitted != 1 {
		t.Fatalf("project: the producer emitted %d entities, want 1 (the seed would be vacuous)", projectsEmitted)
	}
	if got := projectCounts[contextfabric.SubjectKind(identity.KindProject)]; got != projectsEmitted {
		t.Errorf("project: the source node count is %d, the producer emitted %d entities", got, projectsEmitted)
	}

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := source.ProjectionSourceCounts(ctx, edgeParityOrg)
	if err != nil {
		t.Fatalf("ProjectionSourceCounts: %v", err)
	}
	emitted := map[string]int64{}
	cursor = ""
	for page := 0; ; page++ {
		if page > 500 {
			t.Fatal("the source did not drain")
		}
		batch, ok, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: edgeParityOrg, Source: devhealthsource.SourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("read page %d: %v", page, err)
		}
		if !ok {
			break
		}
		for _, e := range batch.Entities {
			emitted[string(e.Subject.Kind)]++
		}
		cursor = batch.NextCursor
	}
	for _, kind := range []string{identity.KindWorkItem, identity.KindDeployment, identity.KindCIPipelineRun} {
		if emitted[kind] != 1 {
			t.Fatalf("%s: the producer emitted %d entities, want 1 (the seed would be vacuous)", kind, emitted[kind])
		}
		if got := counts[contextfabric.SubjectKind(kind)]; got != emitted[kind] {
			t.Errorf("%s: the source node count is %d, the producer emitted %d entities", kind, got, emitted[kind])
		}
	}
}
