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
