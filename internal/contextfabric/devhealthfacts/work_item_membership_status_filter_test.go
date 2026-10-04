package devhealthfacts

import (
	"context"
	"strings"
	"testing"
	"time"

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

func TestWorkItemMembershipS1AppliesTheTimeWindowOnOneClosedColumn(t *testing.T) {
	start := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	for _, column := range []string{"created_at", "completed_at", "updated_at"} {
		client := &workItemMembershipFakeClient{scanErrAt: -1, rows: [][]any{workItemMembershipTestSentinelRow()}}
		reader, _ := newWorkItemMembershipTestReader(t, client, &workItemMembershipTelemetrySpy{})
		lease, _, err := reader.BeginWorkItemMembership(context.Background(), storage.Principal{OrgID: workItemMembershipTestOrg}, contextfabric.WorkItemMembershipRequest{
			Anchor: workItemMembershipTestAnchor(t, "linear", "P1"), TimeColumn: column, TimeStart: start, TimeEnd: end,
		})
		if err != nil || lease == nil {
			t.Fatalf("%s: lease=%v err=%v", column, lease, err)
		}
		lease.Release()
		statement := client.queries[0].statement
		predicate := "AND w." + column + " >= {time_start:DateTime64(6, 'UTC')} AND w." + column + " < {time_end:DateTime64(6, 'UTC')}"
		if strings.Count(statement, predicate) != 1 {
			t.Fatalf("%s: the member read must carry the window predicate on that column exactly once", column)
		}
		for _, other := range []string{"created_at", "completed_at", "updated_at"} {
			if other != column && strings.Contains(statement, "w."+other+" >=") {
				t.Fatalf("%s: the statement also filters %s", column, other)
			}
		}
		bound := map[string]any{}
		for _, binding := range client.queries[0].bindings {
			bound[binding.Name] = binding.Value
		}
		if bound["time_start"] != start || bound["time_end"] != end {
			t.Fatalf("%s: bindings = %v", column, bound)
		}
	}
	// No window: no time predicate and no time binding.
	client := &workItemMembershipFakeClient{scanErrAt: -1, rows: [][]any{workItemMembershipTestSentinelRow()}}
	reader, _ := newWorkItemMembershipTestReader(t, client, &workItemMembershipTelemetrySpy{})
	lease, _, err := reader.BeginWorkItemMembership(context.Background(), storage.Principal{OrgID: workItemMembershipTestOrg}, contextfabric.WorkItemMembershipRequest{Anchor: workItemMembershipTestAnchor(t, "linear", "P1")})
	if err != nil || lease == nil {
		t.Fatalf("lease=%v err=%v", lease, err)
	}
	lease.Release()
	if strings.Contains(client.queries[0].statement, "time_start") {
		t.Fatal("an unwindowed read carried a time predicate")
	}
}

func TestWorkItemMembershipS1RefusesAnUnclosedOrEmptyTimeWindowBeforeQuerying(t *testing.T) {
	start := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	for name, request := range map[string]timeWindowShape{
		"unknown column":    {column: "closed_at", start: start, end: end},
		"injected column":   {column: "created_at; DROP TABLE work_items", start: start, end: end},
		"zero start":        {column: "created_at", end: end},
		"zero end":          {column: "created_at", start: start},
		"empty interval":    {column: "created_at", start: start, end: start},
		"reversed interval": {column: "created_at", start: end, end: start},
	} {
		client := &workItemMembershipFakeClient{scanErrAt: -1, rows: [][]any{workItemMembershipTestSentinelRow()}}
		reader, gate := newWorkItemMembershipTestReader(t, client, &workItemMembershipTelemetrySpy{})
		_, _, err := reader.BeginWorkItemMembership(context.Background(), storage.Principal{OrgID: workItemMembershipTestOrg}, contextfabric.WorkItemMembershipRequest{
			Anchor: workItemMembershipTestAnchor(t, "linear", "P1"), TimeColumn: request.column, TimeStart: request.start, TimeEnd: request.end,
		})
		if err == nil || len(client.queries) != 0 {
			t.Fatalf("%s: err=%v queries=%d, want a refusal before any query", name, err, len(client.queries))
		}
		if gate.Stats().InFlight != 0 {
			t.Fatalf("%s: the refused request kept its admission permit", name)
		}
	}
}

type timeWindowShape struct {
	column     string
	start, end time.Time
}
