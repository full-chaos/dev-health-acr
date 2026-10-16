package contextpacket_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// The packet path labels a CI run by the same rule as the fact path: the
// stored pipeline_name when there is one, the bare run id otherwise, on a
// real ClickHouse.
func TestCIRunLabelOnARealStore(t *testing.T) {
	ctx := context.Background()
	query, direct := startScopeClickHouse(t, ctx)
	for _, statement := range devhealthschema.DDL() {
		scopeExec(t, ctx, direct, statement)
	}
	scopeExec(t, ctx, direct, `CREATE TABLE git_commits (org_id String, repo_id UUID, hash String, message Nullable(String), author_name Nullable(String), author_email Nullable(String), author_when DateTime64(3, 'UTC'), committer_name Nullable(String), committer_email Nullable(String), committer_when DateTime64(3, 'UTC'), parents UInt32, last_synced DateTime64(3, 'UTC')) ENGINE = ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id, hash)`)
	scopeExec(t, ctx, direct, `CREATE TABLE git_commit_stats (org_id String, repo_id UUID, commit_hash String, file_path String, additions Int32, deletions Int32, old_file_mode String, new_file_mode String, last_synced DateTime64(3, 'UTC')) ENGINE = ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id, commit_hash, file_path)`)
	scopeExec(t, ctx, direct, `CREATE TABLE work_graph_edges (org_id String, repo_id UUID, last_synced DateTime64(3, 'UTC')) ENGINE = ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id)`)
	event := "now64(3) - toIntervalDay(1)"
	synced := "now64(3) - toIntervalHour(2)"
	for _, statement := range []string{
		fmt.Sprintf(`INSERT INTO repos (id, repo, created_at, last_synced, org_id, provider) VALUES ('%s', '%s', %s, %s, '%s', 'github')`, scopeRepoID, scopeRepo, event, synced, scopeOrgA),
		fmt.Sprintf(`INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id, pipeline_name) VALUES ('%s', 'run-named', 'success', %s, %s, '%s', 'build and test')`, scopeRepoID, event, synced, scopeOrgA),
		fmt.Sprintf(`INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) VALUES ('%s', 'run-bare', 'success', %s, %s, '%s')`, scopeRepoID, event, synced, scopeOrgA),
	} {
		scopeExec(t, ctx, direct, statement)
	}
	edgeNames := map[string]string{
		"edge-blank":   "   ",
		"edge-padded":  "\t pad name \n",
		"edge-long":    strings.Repeat("a", 81),
		"edge-unicode": strings.Repeat("é", 81),
		"edge-inner":   "build  and\ttest",
		"edge-exact":   strings.Repeat("b", 80),
	}
	for id, name := range edgeNames {
		scopeExec(t, ctx, direct, fmt.Sprintf(`INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id, pipeline_name) VALUES ('%s', '%s', 'success', %s, %s, '%s', '%s')`, scopeRepoID, id, event, synced, scopeOrgA, name))
	}
	plan := contextpacket.ReadPlan{OrgID: scopeOrgA, RepoID: scopeRepoID, RepoSlug: scopeRepo}
	evidence, _, _, err := contextpacket.NewCatalogClickHouseRows(query).EvidenceRows(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, ref := range evidence {
		if ref.Source.EntityType == "ci_pipeline_run" {
			got[ref.Source.EntityID] = ref.Source.DisplayLabel
		}
	}
	for id, name := range edgeNames {
		if want := contextpacket.CIRunLabel(name, id); got[id] != want {
			t.Errorf("run %s display_label = %q, want the shared rule's %q", id, got[id], want)
		}
	}
	want := map[string]string{"run-named": "CI build and test #run-named", "run-bare": "CI run-bare"}
	for id, label := range want {
		if got[id] != label {
			t.Errorf("run %s display_label = %q, want %q (all: %v)", id, got[id], label, got)
		}
	}
}
