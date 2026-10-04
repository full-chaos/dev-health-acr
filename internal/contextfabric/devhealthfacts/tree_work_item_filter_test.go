package devhealthfacts_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// treeFilterRow is one stored work item as the fake serves it.
type treeFilterRow struct {
	repo, id, status string
	completedAt      *time.Time
}

// treeFilterClient answers the status and completion reads from stored rows,
// honouring the ids binding (as the real statement does) and omitting rows in
// hidden, the way the authorization predicate would.
type treeFilterClient struct {
	rows    []treeFilterRow
	hidden  map[string]bool // "repo:id"
	err     error
	batches []int // ids per query
	extra   int   // rows to append (duplicates) to every read
}

func (c *treeFilterClient) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if c.err != nil {
		return nil, c.err
	}
	var ids []string
	for _, b := range bindings {
		if b.Name == "ids" {
			ids, _ = b.Value.([]string)
		}
	}
	c.batches = append(c.batches, len(ids))
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	completion := strings.Contains(statement, "isNotNull(w.completed_at)")
	var out [][]any
	for _, r := range c.rows {
		key := r.repo + ":" + r.id
		if !want[key] || c.hidden[key] {
			continue
		}
		if completion {
			done, at := uint8(0), time.Unix(0, 0).UTC()
			if r.completedAt != nil {
				done, at = 1, *r.completedAt
			}
			out = append(out, []any{r.id, done, at, r.repo})
		} else {
			out = append(out, []any{r.id, r.status, r.repo, "provider"})
		}
	}
	for i := 0; i < c.extra && len(out) > 0; i++ {
		out = append(out, out[0])
	}
	return &fakeScanner{rows: out}, nil
}

var treeFilterPrincipal = storage.Principal{OrgID: "org-1"}

func treeMembers(rows []treeFilterRow) []contextfabric.SubjectRef {
	out := make([]contextfabric.SubjectRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, workItemSubject(r.repo, r.id))
	}
	return out
}

func treeIDs(refs []contextfabric.SubjectRef, pick ...int) []string {
	out := []string{}
	for _, i := range pick {
		out = append(out, refs[i].CanonicalID)
	}
	sort.Strings(out)
	return out
}

func runTreeFilter(t *testing.T, client *treeFilterClient, members []contextfabric.SubjectRef, mutate func(*contextfabric.TreeWorkItemFilterRequest)) ([]string, int, error) {
	t.Helper()
	request := contextfabric.TreeWorkItemFilterRequest{Members: members}
	if mutate != nil {
		mutate(&request)
	}
	return devhealthfacts.NewTreeWorkItemFilterReader(client).FilterTreeWorkItems(context.Background(), treeFilterPrincipal, request)
}

func assertTreeFilter(t *testing.T, kept []string, unread int, wantKept []string, wantUnread int) {
	t.Helper()
	if len(kept) == 0 && len(wantKept) == 0 {
		kept = nil
		wantKept = nil
	}
	if !reflect.DeepEqual(kept, wantKept) || unread != wantUnread {
		t.Fatalf("kept=%v unread=%d, want kept=%v unread=%d", kept, unread, wantKept, wantUnread)
	}
}

func TestTreeWorkItemFilterStatusMatchAndMismatch(t *testing.T) {
	t.Parallel()
	rows := []treeFilterRow{{repo: "r1", id: "A", status: "in_progress"}, {repo: "r1", id: "B", status: "done"}, {repo: "r2", id: "C", status: "in_progress"}}
	members := treeMembers(rows)
	kept, unread, err := runTreeFilter(t, &treeFilterClient{rows: rows}, members, func(r *contextfabric.TreeWorkItemFilterRequest) { r.Status = "in_progress" })
	if err != nil {
		t.Fatal(err)
	}
	assertTreeFilter(t, kept, unread, treeIDs(members, 0, 2), 0)
	kept, unread, err = runTreeFilter(t, &treeFilterClient{rows: rows}, members, func(r *contextfabric.TreeWorkItemFilterRequest) { r.Status = "todo" })
	if err != nil {
		t.Fatal(err)
	}
	assertTreeFilter(t, kept, unread, nil, 0)
}

func TestTreeWorkItemFilterCompletedWindowBoundaries(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	inside := start.Add(48 * time.Hour)
	before := start.Add(-time.Nanosecond)
	rows := []treeFilterRow{
		{repo: "r1", id: "inside", completedAt: &inside},
		{repo: "r1", id: "atstart", completedAt: &start},
		{repo: "r1", id: "atend", completedAt: &end},
		{repo: "r1", id: "before", completedAt: &before},
		{repo: "r1", id: "open"},
	}
	members := treeMembers(rows)
	kept, unread, err := runTreeFilter(t, &treeFilterClient{rows: rows}, members, func(r *contextfabric.TreeWorkItemFilterRequest) {
		r.CompletedStart, r.CompletedEnd = start, end
	})
	if err != nil {
		t.Fatal(err)
	}
	// inside and at-start kept; at-end (exclusive), before-start and not
	// completed excluded.
	assertTreeFilter(t, kept, unread, treeIDs(members, 0, 1), 0)
}

func TestTreeWorkItemFilterBothQualifiers(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	in := start.Add(time.Hour)
	out := end.Add(time.Hour)
	rows := []treeFilterRow{
		{repo: "r1", id: "both", status: "done", completedAt: &in},
		{repo: "r1", id: "wrongstatus", status: "todo", completedAt: &in},
		{repo: "r1", id: "wrongwindow", status: "done", completedAt: &out},
		{repo: "r1", id: "neither", status: "todo"},
	}
	members := treeMembers(rows)
	kept, unread, err := runTreeFilter(t, &treeFilterClient{rows: rows}, members, func(r *contextfabric.TreeWorkItemFilterRequest) {
		r.Status, r.CompletedStart, r.CompletedEnd = "done", start, end
	})
	if err != nil {
		t.Fatal(err)
	}
	assertTreeFilter(t, kept, unread, treeIDs(members, 0), 0)
}

func TestTreeWorkItemFilterNoQualifierKeepsEveryReadableMember(t *testing.T) {
	t.Parallel()
	rows := []treeFilterRow{{repo: "r1", id: "B", status: "x"}, {repo: "r1", id: "A", status: "y"}}
	members := treeMembers(rows)
	kept, unread, err := runTreeFilter(t, &treeFilterClient{rows: rows}, members, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertTreeFilter(t, kept, unread, treeIDs(members, 0, 1), 0)
}

func TestTreeWorkItemFilterBatchesPastTheRowBound(t *testing.T) {
	t.Parallel()
	const n = 450
	rows := make([]treeFilterRow, n)
	for i := range rows {
		status := "open"
		if i%2 == 1 {
			status = "closed"
		}
		rows[i] = treeFilterRow{repo: "r1", id: "WI-" + strconv.Itoa(i), status: status}
	}
	members := treeMembers(rows)
	client := &treeFilterClient{rows: rows}
	kept, unread, err := runTreeFilter(t, client, members, func(r *contextfabric.TreeWorkItemFilterRequest) { r.Status = "open" })
	if err != nil {
		t.Fatal(err)
	}
	var even []int
	for i := 0; i < n; i += 2 {
		even = append(even, i)
	}
	assertTreeFilter(t, kept, unread, treeIDs(members, even...), 0)
	if !reflect.DeepEqual(client.batches, []int{200, 200, 50}) {
		t.Fatalf("batches = %v, want [200 200 50]", client.batches)
	}
}

func TestTreeWorkItemFilterUndecodableIDCountsUnread(t *testing.T) {
	t.Parallel()
	rows := []treeFilterRow{{repo: "r1", id: "A", status: "open"}}
	members := append(treeMembers(rows), contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "not-a-canonical-id"})
	kept, unread, err := runTreeFilter(t, &treeFilterClient{rows: rows}, members, func(r *contextfabric.TreeWorkItemFilterRequest) { r.Status = "open" })
	if err != nil {
		t.Fatal(err)
	}
	assertTreeFilter(t, kept, unread, treeIDs(members, 0), 1)
}

func TestTreeWorkItemFilterHiddenMemberCountsUnreadNotKept(t *testing.T) {
	t.Parallel()
	rows := []treeFilterRow{{repo: "r1", id: "A", status: "open"}, {repo: "r2", id: "B", status: "open"}}
	members := treeMembers(rows)
	client := &treeFilterClient{rows: rows, hidden: map[string]bool{"r2:B": true}}
	for _, qualifier := range []func(*contextfabric.TreeWorkItemFilterRequest){
		nil,
		func(r *contextfabric.TreeWorkItemFilterRequest) { r.Status = "open" },
		func(r *contextfabric.TreeWorkItemFilterRequest) {
			r.CompletedStart, r.CompletedEnd = time.Unix(0, 0).UTC(), time.Now().UTC()
		},
	} {
		kept, unread, err := runTreeFilter(t, client, members, qualifier)
		if err != nil {
			t.Fatal(err)
		}
		if qualifier != nil && len(kept) == 0 {
			// window arm: neither row is completed, so nothing is kept
			assertTreeFilter(t, kept, unread, nil, 1)
			continue
		}
		assertTreeFilter(t, kept, unread, treeIDs(members, 0), 1)
	}
}

func TestTreeWorkItemFilterTruncatedBatchCountsUnread(t *testing.T) {
	t.Parallel()
	rows := []treeFilterRow{{repo: "r1", id: "A", status: "open"}, {repo: "r1", id: "B", status: "open"}}
	members := treeMembers(rows)
	// 2 rows + 200 duplicates crosses the 200-row output bound.
	kept, unread, err := runTreeFilter(t, &treeFilterClient{rows: rows, extra: 200}, members, func(r *contextfabric.TreeWorkItemFilterRequest) { r.Status = "open" })
	if err != nil {
		t.Fatal(err)
	}
	assertTreeFilter(t, kept, unread, nil, 2)
}

func TestTreeWorkItemFilterReaderErrorPropagates(t *testing.T) {
	t.Parallel()
	rows := []treeFilterRow{{repo: "r1", id: "A", status: "open"}}
	kept, unread, err := runTreeFilter(t, &treeFilterClient{rows: rows, err: errors.New("boom")}, treeMembers(rows), func(r *contextfabric.TreeWorkItemFilterRequest) { r.Status = "open" })
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v, want FactReadFailure/unavailable", err)
	}
	if kept != nil || unread != 0 {
		t.Fatalf("kept=%v unread=%d on error, want none", kept, unread)
	}
}
