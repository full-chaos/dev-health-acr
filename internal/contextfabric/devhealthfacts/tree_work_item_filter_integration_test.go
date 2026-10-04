package devhealthfacts_test

// The tree work-item filter against a REAL ClickHouse. The fake client in
// tree_work_item_filter_test.go ignores the statement, so it cannot say
// whether the readers' SQL (FINAL, ifNull(status), isNotNull(completed_at))
// agrees with the predicates the project path pushes down in
// work_item_membership.go (`w.status = {status_filter}` and the half-open
// window on `w.completed_at`). This test seeds work items shaped like four
// providers and asserts the filter keeps exactly the rows those predicates
// keep, with the predicates evaluated by the server itself as the oracle.
//
// Written for the shared container venue (see chaos5270_shared_container_test.go);
// not run by the author.

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestTreeWorkItemFilterMatchesProjectStatementPredicatesAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	client, direct := sharedClickHouseFixture(t)
	orgID := sharedTestOrgID(t)
	at := time.Now().UTC().Truncate(time.Millisecond)
	start := at.Add(-30 * 24 * time.Hour).Truncate(time.Second)
	end := start.Add(30 * 24 * time.Hour)

	const repoID = "d3c0ffee-0000-4000-8000-0000000000f1"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repoID, orgID, "acme/tree-filter", "github", at); err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	inside := start.Add(72 * time.Hour)
	beforeStart := start.Add(-time.Second)
	type seed struct {
		id, provider, status string
		completedAt          *time.Time
	}
	seeds := []seed{
		{"gh-1", "github", "done", &inside},
		{"gh-2", "github", "in_progress", nil},
		{"gl-1", "gitlab", "done", &start},       // at window start: kept
		{"gl-2", "gitlab", "done", &end},         // at window end: excluded
		{"ln-1", "linear", "done", &beforeStart}, // just before: excluded
		{"ln-2", "linear", "canceled", &inside},  // completed_at present, other status
		{"jr-1", "jira", "todo", nil},
		{"jr-2", "jira", "done", &inside},
		{"jr-3", "jira", "unknown", nil},
	}
	var members []contextfabric.SubjectRef
	for _, s := range seeds {
		if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, created_at, updated_at, completed_at, project_id, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.id, repoID, orgID, s.provider, "t "+s.id, s.status, at, at, s.completedAt, "", at); err != nil {
			t.Fatalf("seed %s: %v", s.id, err)
		}
		members = append(members, workItemSubject(repoID, s.id))
	}
	byKey := map[string]string{}
	for i, s := range seeds {
		byKey[s.id] = members[i].CanonicalID
	}

	// The oracle: the project statement's own predicates, run by the server
	// over the same FINAL rows.
	oracle := func(status string, windowed bool) []string {
		t.Helper()
		statement := `SELECT w.work_item_id FROM work_items AS w FINAL
WHERE w.org_id = {org_id:String}
  AND ({status_filter:String} = '' OR w.status = {status_filter:String})`
		bindings := []contextpacket.ClickHouseBinding{
			{Name: "org_id", Value: orgID},
			{Name: "status_filter", Value: status},
		}
		if windowed {
			statement += ` AND w.completed_at >= {time_start:DateTime64(6, 'UTC')} AND w.completed_at < {time_end:DateTime64(6, 'UTC')}`
			bindings = append(bindings,
				contextpacket.ClickHouseBinding{Name: "time_start", Value: start},
				contextpacket.ClickHouseBinding{Name: "time_end", Value: end})
		}
		scanner, err := client.Query(ctx, statement, bindings)
		if err != nil {
			t.Fatalf("oracle query: %v", err)
		}
		defer scanner.Close()
		var out []string
		for scanner.Next() {
			var id string
			if err := scanner.Scan(&id); err != nil {
				t.Fatalf("oracle scan: %v", err)
			}
			out = append(out, byKey[id])
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		sort.Strings(out)
		return out
	}

	filter := devhealthfacts.NewTreeWorkItemFilterReader(client)
	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}
	cases := []struct {
		name     string
		status   string
		windowed bool
	}{
		{"status only", "done", false},
		{"window only", "", true},
		{"status and window", "done", true},
		{"no qualifier", "", false},
	}
	for _, tc := range cases {
		request := contextfabric.TreeWorkItemFilterRequest{Members: members, Status: tc.status}
		if tc.windowed {
			request.CompletedStart, request.CompletedEnd = start, end
		}
		kept, unread, err := filter.FilterTreeWorkItems(ctx, principal, request)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		want := oracle(tc.status, tc.windowed)
		if !reflect.DeepEqual(kept, want) || unread != 0 {
			t.Errorf("%s: kept=%v unread=%d, project-statement predicates keep %v", tc.name, kept, unread, want)
		}
	}
	// The no-qualifier arm must keep every seeded member; guards an oracle
	// that is empty because the seed failed to land.
	if kept, _, _ := filter.FilterTreeWorkItems(ctx, principal, contextfabric.TreeWorkItemFilterRequest{Members: members}); len(kept) != len(seeds) {
		t.Fatalf("no-qualifier kept %d of %d seeded members", len(kept), len(seeds))
	}
}
