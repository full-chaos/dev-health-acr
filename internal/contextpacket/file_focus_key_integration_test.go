package contextpacket_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// The hotspot, complexity and commit-file facts of one file must carry the same
// focus key so the packet gives the file one slot first.
func TestFileFactsShareOneFocusKeyAcrossTheThreeFileSources(t *testing.T) {
	ctx := context.Background()
	query, direct := startScopeClickHouse(t, ctx)
	for _, statement := range devhealthschema.DDL() {
		scopeExec(t, ctx, direct, statement)
	}
	for _, statement := range devhealthschema.ProjectionDDL("file_hotspot_daily") {
		scopeExec(t, ctx, direct, statement)
	}
	// The complexity run projection of ops migration 069 is not declared in
	// devhealthschema.Projections; the complexity statement forces it.
	scopeExec(t, ctx, direct, `ALTER TABLE file_complexity_snapshots ADD PROJECTION IF NOT EXISTS prj_acr_file_complexity_runs (SELECT org_id, repo_id, ref, as_of_day, computed_at GROUP BY org_id, repo_id, ref, as_of_day, computed_at)`)
	// devhealthschema does not declare the commit tables: the ops definition with
	// the org_id column and sorting key of ops migration 027.
	scopeExec(t, ctx, direct, `CREATE TABLE git_commit_stats (org_id String, repo_id UUID, commit_hash String, file_path String, additions Int32, deletions Int32, old_file_mode String, new_file_mode String, last_synced DateTime64(3, 'UTC')) ENGINE = ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id, commit_hash, file_path)`)
	now := "'2026-09-01 12:00:00.000'"
	day := "'2026-09-01'"
	for _, statement := range []string{
		fmt.Sprintf(`INSERT INTO repos (id, repo, created_at, last_synced, org_id, provider) VALUES ('%s', '%s', %s, %s, '%s', 'github')`, injRepoID, injRepo, now, now, injOrg),
		fmt.Sprintf(`INSERT INTO file_hotspot_daily (org_id, repo_id, day, file_path, computed_at) VALUES ('%s', '%s', %s, 'src/checkout.go', %s)`, injOrg, injRepoID, day, now),
		fmt.Sprintf(`INSERT INTO file_complexity_snapshots (org_id, repo_id, as_of_day, file_path, computed_at) VALUES ('%s', '%s', %s, 'src/checkout.go', %s)`, injOrg, injRepoID, day, now),
		fmt.Sprintf(`INSERT INTO git_commit_stats (org_id, repo_id, commit_hash, file_path, additions, deletions, last_synced) VALUES ('%s', '%s', 'c0ffee', 'src/checkout.go', 1, 2, %s)`, injOrg, injRepoID, now),
		fmt.Sprintf(`INSERT INTO git_commit_stats (org_id, repo_id, commit_hash, file_path, additions, deletions, last_synced) VALUES ('%s', '%s', 'c0ffee', '__AGGREGATE__', 5, 6, %s)`, injOrg, injRepoID, now),
	} {
		scopeExec(t, ctx, direct, statement)
	}
	plan := contextpacket.ReadPlan{OrgID: injOrg, RepoID: injRepoID, RepoSlug: injRepo}
	executor := contextpacket.NewClickHouseSourceExecutor(query)
	want := "file:" + injRepoID + ":src/checkout.go"
	seen := 0
	for _, source := range contextpacket.SourceQueryCatalogV1 {
		if source.ID != "file_hotspots.v1" && source.ID != "file_complexity.v1" && source.ID != "git_commit_files.v1" {
			continue
		}
		refs, err := executor.QueryEvidence(ctx, source, plan.Bindings())
		if err != nil {
			t.Fatalf("%s: %v (cause: %v)", source.ID, err, errors.Unwrap(err))
		}
		for _, ref := range refs {
			got, _ := ref.Metadata["focus_key"].(string)
			if ref.Source.EntityID == "c0ffee:__AGGREGATE__" {
				if got != "" {
					t.Errorf("aggregate sentinel focus_key = %q, want none", got)
				}
				continue
			}
			if got != want {
				t.Errorf("%s focus_key = %q, want %q", source.ID, got, want)
			}
			seen++
		}
	}
	if seen != 3 {
		t.Fatalf("read %d of the 3 file sources", seen)
	}
}
