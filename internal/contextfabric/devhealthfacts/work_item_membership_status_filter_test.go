package devhealthfacts

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestWorkItemMembershipS1BindsTheStatusFilterAndAppliesItInTheMemberRead(t *testing.T) {
	for _, status := range []string{"", "blocked"} {
		client := &workItemMembershipFakeClient{scanErrAt: -1, rows: [][]any{workItemMembershipTestSentinelRow()}}
		reader, _ := newWorkItemMembershipTestReader(t, client, &workItemMembershipTelemetrySpy{})
		lease, _, err := reader.BeginWorkItemMembership(context.Background(), storage.Principal{OrgID: workItemMembershipTestOrg}, contextfabric.WorkItemMembershipRequest{
			Anchor: workItemMembershipTestAnchor(t, "linear", "P1"), Status: status,
		})
		if err != nil || lease == nil {
			t.Fatalf("status %q: lease=%v err=%v", status, lease, err)
		}
		lease.Release()
		if len(client.queries) != 1 {
			t.Fatalf("status %q: queries = %d", status, len(client.queries))
		}
		query := client.queries[0]
		const predicate = "({status_filter:String} = '' OR w.status = {status_filter:String})"
		if strings.Count(query.statement, predicate) != 1 {
			t.Fatalf("status %q: the member read must carry the status predicate exactly once", status)
		}
		found := false
		for _, binding := range query.bindings {
			if binding.Name == "status_filter" {
				found = true
				if binding.Value != status {
					t.Fatalf("status_filter bound %v, want %q", binding.Value, status)
				}
			}
		}
		if !found {
			t.Fatalf("status %q: no status_filter binding", status)
		}
	}
}
