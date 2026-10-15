package contextpacket_test

import (
	"context"
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
			t.Fatalf("%s: %v", source.ID, err)
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
