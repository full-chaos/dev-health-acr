package contextpacket_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// The hotspot, complexity and commit-file facts of one file must carry the same
// focus key so the packet gives the file one slot first. It runs the three real
// catalog statements against a ClickHouse migrated by the ops checkout (the
// live schema parity job), never a hand-built schema.
func TestLiveFileFactsShareOneFocusKeyAcrossTheThreeFileSources(t *testing.T) {
	dsn := os.Getenv("ACR_CLICKHOUSE_INTEGRATION_DSN")
	if dsn == "" {
		if os.Getenv("ACR_CLICKHOUSE_INTEGRATION_REQUIRED") == "1" {
			t.Fatal("ACR_CLICKHOUSE_INTEGRATION_DSN is required when native ClickHouse integration is mandatory")
		}
		t.Skip("ACR_CLICKHOUSE_INTEGRATION_DSN is required to run the file statements against an ops-migrated ClickHouse")
	}
	ctx := context.Background()
	options, err := clickhousedriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	direct, err := clickhousedriver.Open(options)
	if err != nil {
		t.Fatalf("open native connection: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	query, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{DSN: dsn, DialTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("open query client: %v", err)
	}
	t.Cleanup(func() { _ = query.Close() })

	const (
		orgID  = "10000000-0000-4000-8000-0000000000f1"
		repoID = "30000000-0000-4000-8000-0000000000f1"
		repo   = "acme/focus"
	)
	now, day := "'2026-09-01 12:00:00.000'", "'2026-09-01'"
	for _, statement := range []string{
		fmt.Sprintf(`INSERT INTO repos (id, repo, last_synced, org_id, provider) VALUES ('%s', '%s', %s, '%s', 'github')`, repoID, repo, now, orgID),
		fmt.Sprintf(`INSERT INTO file_hotspot_daily (org_id, repo_id, day, file_path, computed_at) VALUES ('%s', '%s', %s, 'src/checkout.go', %s)`, orgID, repoID, day, now),
		fmt.Sprintf(`INSERT INTO file_complexity_snapshots (org_id, repo_id, ref, as_of_day, file_path, computed_at) VALUES ('%s', '%s', 'main', %s, 'src/checkout.go', %s)`, orgID, repoID, day, now),
		fmt.Sprintf(`INSERT INTO git_commit_stats (org_id, repo_id, commit_hash, file_path, additions, deletions, last_synced) VALUES ('%s', '%s', 'c0ffee', 'src/checkout.go', 1, 2, %s)`, orgID, repoID, now),
		fmt.Sprintf(`INSERT INTO git_commit_stats (org_id, repo_id, commit_hash, file_path, additions, deletions, last_synced) VALUES ('%s', '%s', 'c0ffee', '__AGGREGATE__', 5, 6, %s)`, orgID, repoID, now),
	} {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}

	plan := contextpacket.ReadPlan{OrgID: orgID, RepoID: repoID, RepoSlug: repo}
	executor := contextpacket.NewClickHouseSourceExecutor(query)
	want := "file:" + repoID + ":src/checkout.go"
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
