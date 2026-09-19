package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Repository-less work items are denied to a principal restricted to a slug
// list. The real S1 reader measures them as denied, and the real Engine must
// disclose that gap instead of serving an empty project.
func TestWorkItemAuthorizationGapAgainstRealClickHouse(t *testing.T) {
	at := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	const repo = "70000000-0000-4000-8000-000000000001"
	const repoless = "00000000-0000-0000-0000-000000000000"
	for _, tc := range []struct {
		name               string
		authorized, denied int
		wantStatus         string
		wantLimitation     string
	}{
		{name: "all_denied", denied: 3, wantStatus: "degraded", wantLimitation: "3 work items were observed and none are authorized"},
		{name: "partly_denied", authorized: 2, denied: 3, wantLimitation: "2 work items are authorized and listed, and 3 more are denied"},
		{name: "none_denied", authorized: 2},
		{name: "empty_project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query, direct := freshLiveDatabase(t)
			org := sharedTestOrgID(t)
			seed := func(sql string, args ...any) {
				t.Helper()
				if err := direct.Exec(context.Background(), sql, args...); err != nil {
					t.Fatal(err)
				}
			}
			seed(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repo, org, "acme/allowed", "linear", at)
			seed(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, "P1", org, "linear", "P1", "Project Alpha", uint8(1), "active", "", at)
			insert := func(id, repoID string) {
				seed(`INSERT INTO work_items (work_item_id, repo_id, org_id, provider, project_id, title, status, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, repoID, org, "linear", "P1", "Title "+id, "open", at, at, at)
			}
			for i := 0; i < tc.authorized; i++ {
				insert(fmt.Sprintf("WI-A%d", i), repo)
			}
			for i := 0; i < tc.denied; i++ {
				insert(fmt.Sprintf("WI-D%d", i), repoless)
			}
			fixture := newFreshLiveEngine(t, query, org, "linear", "P1", at, 234)
			result, err := fixture.engine.Investigate(context.Background(), fixture.principal, fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			disclosed := ""
			for _, limitation := range result.Limitations {
				if strings.HasPrefix(limitation, "Work items exist in this project that are outside this principal's authorized scope") {
					disclosed = limitation
				}
			}
			if tc.wantLimitation == "" {
				if disclosed != "" {
					t.Fatalf("undenied project disclosed a gap: %q", disclosed)
				}
				return
			}
			if !strings.Contains(disclosed, tc.wantLimitation) {
				t.Fatalf("limitation=%q want it to contain %q (all: %q)", disclosed, tc.wantLimitation, result.Limitations)
			}
			if tc.wantStatus != "" && string(result.Status) != tc.wantStatus {
				t.Fatalf("status=%q want %q", result.Status, tc.wantStatus)
			}
			if tc.authorized == 0 && result.Cohort != nil {
				t.Fatalf("all-denied answer carried a cohort: %+v", result.Cohort)
			}
			if tc.authorized > 0 && (result.Cohort == nil || len(result.Cohort.Members) != tc.authorized) {
				t.Fatalf("cohort=%+v want %d members", result.Cohort, tc.authorized)
			}
		})
	}
}
