package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// A from-zero build on a real ClickHouse, through the production worker, with
// a backend that refuses the first apply. Both sources hold dimension tables
// over the from-zero read's cap, stamped later than every fact row (as a sync
// leaves them): every repository, team and project is applied before the
// first batch that moves the fact position, and the refused batch is applied
// first.
func TestDimensionPhaseAgainstRealClickHouse(t *testing.T) {
	query, direct := orgIsolationClickHouseFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old, synced := now.Add(-45*24*time.Hour), now.Add(-5*time.Minute)
	requireRefusedBatchAppliedFirst := func(t *testing.T, run dimensionPhaseRun) {
		t.Helper()
		if len(run.attempted) != len(run.applied)+1 || run.attempted[0] != run.attempted[1] || run.attempted[1] != run.applied[0] {
			t.Fatalf("attempted %v, applied %v; want the refused batch handed over again and applied first", run.attempted, run.applied)
		}
	}

	t.Run("repositories", func(t *testing.T) {
		const orgID, repositories, workItems = "72670000-0000-4000-8000-000000009142", 400, 600
		mustExec(t, ctx, direct, `INSERT INTO repos (id, repo, ref, created_at, tags, last_synced, org_id, provider)
SELECT toUUID(concat('91420000-0000-4000-8000-', leftPad(toString(number), 12, '0'))), concat('acme/dimension-', toString(number)), NULL, ?, NULL, ?, ?, 'github' FROM numbers(?)`,
			old, synced, orgID, uint64(repositories))
		mustExec(t, ctx, direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced)
SELECT concat('DIM-', toString(number)), toUUID('91420000-0000-4000-8000-000000000000'), ?, concat('issue ', toString(number)), 'open', '', '', 'github', '', ?, ? FROM numbers(?)`,
			orgID, old, old, uint64(workItems))
		source, err := devhealthsource.NewClickHouseProjectionSource(query)
		if err != nil {
			t.Fatal(err)
		}
		run := runDimensionPhaseThroughWorker(t, ctx, source, devhealthsource.SourceName, orgID)
		if run.before[string(contextfabric.SubjectRepository)] != repositories || run.before[string(contextfabric.SubjectWorkItem)] != 0 {
			t.Fatalf("applied before the fact position moved: %v, want %d repositories and no work item", run.before, repositories)
		}
		if run.firstFact[string(contextfabric.SubjectWorkItem)] == 0 {
			t.Fatalf("the first batch that moved the fact position holds no work item: %v", run.firstFact)
		}
		requireRefusedBatchAppliedFirst(t, run)
	})

	t.Run("teams and projects", func(t *testing.T) {
		const orgID, teams, projects = "72670000-0000-4000-8000-000000009143", 200, 450
		f := newIngestColumnsFixture(t, orgID, nil, nil)
		mustExec(t, ctx, direct, `INSERT INTO teams (id, name, description, updated_at, last_synced, org_id, provider, native_team_key, project_keys, is_active)
SELECT concat('T-', toString(number)), concat('team ', toString(number)), '', ?, ?, ?, 'linear', concat('T-', toString(number)), emptyArrayString(), 1 FROM numbers(?)`,
			old, synced, orgID, uint64(teams))
		mustExec(t, ctx, direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at, last_synced)
SELECT concat('P-', toString(number)), ?, 'linear', concat('K', toString(number)), concat('project ', toString(number)), 1, 'started', '', ?, ? FROM numbers(?)`,
			orgID, old, synced, uint64(projects))
		mustExec(t, ctx, direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at, last_synced)
SELECT ?, 'linear', 'T-0', concat('P-', toString(number)), concat('K', toString(number)), 'native', ?, NULL, ?, ? FROM numbers(?)`,
			orgID, old, old, old, uint64(projects))
		run := runDimensionPhaseThroughWorker(t, ctx, f.h.src, devhealthsource.TeamsProjectsSourceName, orgID)
		if run.before[string(contextfabric.SubjectTeam)] != teams || run.before[string(contextfabric.SubjectProject)] != projects {
			t.Fatalf("applied before the fact position moved: %v, want %d teams and %d projects", run.before, teams, projects)
		}
		requireRefusedBatchAppliedFirst(t, run)
	})
}
