package devhealthfacts

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The status predicate runs inside the S1 statement, so the census, the
// authorized/denied split and the member list are all over the filtered
// population. Against the shared authorization fixture project P holds four
// "done" items (one in a denied repository); one is re-written as "blocked".
func TestWorkItemMembershipStatusFilterAgainstRealClickHouse(t *testing.T) {
	fixture := newAuthzPathsFixture(t)
	ctx := context.Background()
	if err := fixture.direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id) VALUES (?, ?, ?, ?, 'blocked', '', ?, '', 'linear', ?)`,
		"linear:p-project", zeroRepositoryID, authzPathsOrg, "title linear:p-project", fixture.at.Add(time.Hour), authzPathsProjectP); err != nil {
		t.Fatalf("rewrite status: %v", err)
	}
	gate, err := contextfabric.NewWorkItemMembershipGate(1, 1)
	if err != nil {
		t.Fatalf("create gate: %v", err)
	}
	reader, err := NewWorkItemMembershipReader(fixture.query, WorkItemMembershipReaderOptions{Gate: gate, Telemetry: &workItemMembershipTelemetrySpy{}})
	if err != nil {
		t.Fatalf("create membership reader: %v", err)
	}
	scoped := authzPathsPrincipal(authzPathsSlugG)
	for _, tc := range []struct {
		name               string
		status             string
		authorized, denied int
		members            string
	}{
		{"no filter reads every member", "", 3, 1, "linear:direct-granted,linear:p-both,linear:p-project"},
		{"blocked", "blocked", 1, 0, "linear:p-project"},
		{"done", "done", 2, 1, "linear:direct-granted,linear:p-both"},
		{"no item has the status", "canceled", 0, 0, ""},
	} {
		lease, result, err := reader.BeginWorkItemMembership(ctx, scoped, contextfabric.WorkItemMembershipRequest{
			Anchor: workItemMembershipTestAnchor(t, "linear", authzPathsProjectP), S1Instant: fixture.at.Add(2 * time.Hour), Status: tc.status,
		})
		if err != nil || lease == nil {
			t.Fatalf("%s: lease=%v err=%v", tc.name, lease, err)
		}
		lease.Release()
		census := result.Census
		if census.State != contextfabric.WorkItemMembershipCensusExact || census.AuthorizedPopulation != tc.authorized || census.DeniedPopulation != tc.denied {
			t.Fatalf("%s: census = %+v, want exact authorized=%d denied=%d", tc.name, census, tc.authorized, tc.denied)
		}
		var members []string
		for _, member := range result.Members {
			members = append(members, member.WorkItemID)
		}
		sort.Strings(members)
		if strings.Join(members, ",") != tc.members {
			t.Fatalf("%s: members = %v, want %q", tc.name, members, tc.members)
		}
	}
}

// The window predicate reads one named column, half-open, and a null
// completed_at never matches. Window [2026-09-10, 2026-10-10) over project P:
//
//	direct-granted: created 08-01, completed 08-05, updated 09-05  (outside)
//	p-both:         created 09-20, completed null,  updated 09-21
//	p-project:      created 08-15, completed 09-25, updated 09-25
func TestWorkItemMembershipTimeWindowAgainstRealClickHouse(t *testing.T) {
	fixture := newAuthzPathsFixture(t)
	ctx := context.Background()
	day := func(month time.Month, d int) time.Time { return time.Date(2026, month, d, 12, 0, 0, 0, time.UTC) }
	rewrite := func(id, repo string, created time.Time, completed any, updated time.Time) {
		t.Helper()
		if err := fixture.direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, created_at, updated_at, parent_id, provider, project_id, completed_at) VALUES (?, ?, ?, ?, 'done', '', ?, ?, '', 'linear', ?, ?)`,
			id, repo, authzPathsOrg, "title "+id, created, updated, authzPathsProjectP, completed); err != nil {
			t.Fatalf("rewrite %s: %v", id, err)
		}
	}
	rewrite("linear:direct-granted", authzPathsRepoG, day(time.August, 1), day(time.August, 5), day(time.September, 5))
	rewrite("linear:p-both", zeroRepositoryID, day(time.September, 20), nil, day(time.September, 21))
	rewrite("linear:p-project", zeroRepositoryID, day(time.August, 15), day(time.September, 25), day(time.September, 25))
	gate, err := contextfabric.NewWorkItemMembershipGate(1, 1)
	if err != nil {
		t.Fatalf("create gate: %v", err)
	}
	reader, err := NewWorkItemMembershipReader(fixture.query, WorkItemMembershipReaderOptions{Gate: gate, Telemetry: &workItemMembershipTelemetrySpy{}})
	if err != nil {
		t.Fatalf("create membership reader: %v", err)
	}
	start, end := day(time.September, 10), day(time.October, 10)
	for _, tc := range []struct {
		column  string
		members string
	}{
		{"created_at", "linear:p-both"},
		{"completed_at", "linear:p-project"},
		{"updated_at", "linear:p-both,linear:p-project"},
		{"", "linear:direct-granted,linear:p-both,linear:p-project"},
	} {
		lease, result, err := reader.BeginWorkItemMembership(ctx, authzPathsPrincipal(authzPathsSlugG), contextfabric.WorkItemMembershipRequest{
			Anchor: workItemMembershipTestAnchor(t, "linear", authzPathsProjectP), S1Instant: end,
			TimeColumn: tc.column, TimeStart: start, TimeEnd: end,
		})
		if err != nil || lease == nil {
			t.Fatalf("%q: lease=%v err=%v", tc.column, lease, err)
		}
		lease.Release()
		var members []string
		for _, member := range result.Members {
			members = append(members, member.WorkItemID)
		}
		sort.Strings(members)
		if strings.Join(members, ",") != tc.members || result.Census.State != contextfabric.WorkItemMembershipCensusExact {
			t.Fatalf("%q: members = %v census = %+v, want %q", tc.column, members, result.Census, tc.members)
		}
		// The window and the denied count stay on the filtered population.
		if tc.column != "" && int(result.Census.AuthorizedPopulation) != len(strings.Split(tc.members, ",")) {
			t.Fatalf("%q: authorized population %d over the filtered members", tc.column, result.Census.AuthorizedPopulation)
		}
	}
}
