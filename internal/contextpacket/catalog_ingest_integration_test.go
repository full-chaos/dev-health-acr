package contextpacket_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// A repository that is fully synced but quiet, on a real ClickHouse: every
// event (commit, PR, review, CI run, deploy) is nine days old, the rows were
// written by a sync two hours ago and the repository row by a sync one hour
// ago. The packet path must serve those sources fresh at their row write time
// (never the nine-day event time), with the event time apart, and every sync-time statement must run on
// the ops schema (a statement that fails reads as an unknown source).
func TestQuietSyncedRepositoryReadsFreshOnARealStore(t *testing.T) {
	ctx := context.Background()
	query, direct := startScopeClickHouse(t, ctx)
	for _, statement := range devhealthschema.DDL() {
		scopeExec(t, ctx, direct, statement)
	}
	event := "now64(3) - toIntervalDay(9)"
	rowsSynced := "now64(3) - toIntervalHour(2)"
	repoSynced := "now64(3) - toIntervalHour(1)"
	for _, statement := range []string{
		fmt.Sprintf(`INSERT INTO repos (id, repo, created_at, last_synced, org_id, provider) VALUES ('%s', '%s', %s, %s, '%s', 'github')`, scopeRepoID, scopeRepo, event, repoSynced, scopeOrgA),
		fmt.Sprintf(`INSERT INTO git_pull_requests (repo_id, number, title, state, created_at, last_synced, org_id) VALUES ('%s', 7, 'quiet PR', 'merged', %s, %s, '%s')`, scopeRepoID, event, rowsSynced, scopeOrgA),
		fmt.Sprintf(`INSERT INTO git_pull_request_reviews (repo_id, number, review_id, state, submitted_at, last_synced, org_id) VALUES ('%s', 7, 'rev-1', 'APPROVED', %s, %s, '%s')`, scopeRepoID, event, rowsSynced, scopeOrgA),
		fmt.Sprintf(`INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) VALUES ('%s', 'run-1', 'success', %s, %s, '%s')`, scopeRepoID, event, rowsSynced, scopeOrgA),
		fmt.Sprintf(`INSERT INTO deployments (repo_id, deployment_id, status, environment, started_at, deployed_at, release_ref, release_ref_confidence, last_synced, org_id) VALUES ('%s', 'dep-1', 'success', 'prod', %s, %s, 'v1', 1.0, %s, '%s')`, scopeRepoID, event, event, rowsSynced, scopeOrgA),
		fmt.Sprintf(`INSERT INTO git_commits (org_id, repo_id, hash, message, author_when, committer_when, parents, last_synced) VALUES ('%s', '%s', 'c0ffee', 'quiet commit', %s, %s, 1, %s)`, scopeOrgA, scopeRepoID, event, event, rowsSynced),
		fmt.Sprintf(`INSERT INTO git_commit_stats (org_id, repo_id, commit_hash, file_path, additions, deletions, last_synced) VALUES ('%s', '%s', 'c0ffee', 'a.go', 1, 2, %s)`, scopeOrgA, scopeRepoID, rowsSynced),
	} {
		scopeExec(t, ctx, direct, statement)
	}
	plan := contextpacket.ReadPlan{OrgID: scopeOrgA, RepoID: scopeRepoID, RepoSlug: scopeRepo}
	_, watermarks, _, err := contextpacket.NewCatalogClickHouseRows(query).EvidenceRows(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, source := range []string{"git_commits.v1", "git_commit_files.v1", "pull_requests.v1", "pull_request_reviews.v1", "ci_pipeline_runs.v1", "deployments.v1", "repository_freshness.v1"} {
		w := watermarkOf(t, watermarks, source)
		if w.Status != "fresh" || w.LastIngestedAt == nil {
			t.Errorf("%s = %+v, want fresh", source, w)
			continue
		}
		want := 2 * time.Hour
		if source == "repository_freshness.v1" {
			want = time.Hour
		}
		if age := now.Sub(*w.LastIngestedAt); age < want-30*time.Minute || age > want+30*time.Minute {
			t.Errorf("%s last_ingested_at is %v old, want about %v (the stored sync time), not the nine-day event", source, age, want)
		}
		if source == "repository_freshness.v1" {
			continue
		}
		// pull_requests and commit files take their observed_at from the row's
		// own sync time: no event time exists for them.
		if source == "pull_requests.v1" || source == "git_commit_files.v1" {
			continue
		}
		if w.LastEventAt == nil || now.Sub(*w.LastEventAt) < 8*24*time.Hour {
			t.Errorf("%s last_event_at = %v, want the nine-day-old event", source, w.LastEventAt)
		}
	}
	executor := contextpacket.NewClickHouseSourceExecutor(query)
	for _, source := range []string{"repository_freshness.v1", "git_commits.v1", "git_commit_files.v1", "pull_requests.v1", "pull_request_reviews.v1", "ci_pipeline_runs.v1", "deployments.v1", "work_items.v1", "work_item_dependencies.v1", "work_graph.v1", "incidents.v1"} {
		var catalogQuery contextpacket.SourceQuery
		for _, candidate := range contextpacket.SourceQueryCatalogV1 {
			if candidate.ID == source {
				catalogQuery = candidate
			}
		}
		if catalogQuery.ID == "" {
			t.Fatalf("%s is not in the catalog", source)
		}
		if _, _, err := executor.QueryLatestIngested(ctx, catalogQuery, plan.Bindings()); err != nil {
			t.Errorf("%s sync-time statement failed on the ops schema: %v", source, err)
		}
	}
}
