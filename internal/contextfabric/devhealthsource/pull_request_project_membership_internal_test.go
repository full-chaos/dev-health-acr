package devhealthsource

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// membershipRefreshPage answers the refresh page from page, the link read with
// nothing, and the wide read through wideReadRecorder.
type membershipRefreshPage struct {
	wideReadRecorder
	page [][]any
}

func (c *membershipRefreshPage) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	switch {
	case strings.Contains(statement, pullRequestMembershipRefreshMarker):
		return &rowsScanner{rows: c.page}, nil
	case strings.Contains(statement, pullRequestMembershipLinkMarker):
		return &rowsScanner{}, nil
	}
	return c.wideReadRecorder.Query(ctx, statement, bindings)
}

// CHAOS-12308: the refresh page reports the row past its limit, keeps no more
// than the limit, and places each row at its change stamp, not at the pull
// request's own version: a page that dropped the stamp would stand behind the
// cursor and be read again forever.
func TestTheRefreshPageCutsAtItsLimitAndStandsAtTheChangeStamp(t *testing.T) {
	t.Parallel()
	const repo = "12308000-0000-4000-8000-000000000701"
	version := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	client := &membershipRefreshPage{}
	client.stored = map[string]time.Time{}
	want := map[string]time.Time{}
	for number := uint32(1); number <= 3; number++ {
		stamp := version.Add(time.Duration(number) * time.Hour)
		client.page = append(client.page, []any{repo, "acme/r01", number, version, stamp})
		client.stored[fmt.Sprintf("%s:%d", repo, number)] = version
		want[fmt.Sprintf("%s:%d", repo, number)] = stamp
	}
	items, truncated, err := queryPullRequestMembershipChanges(context.Background(), client, "org-1", cursorState{}, 2)
	if err != nil {
		t.Fatalf("refresh page: %v", err)
	}
	if !truncated {
		t.Fatal("a page with a row past its limit reported no more rows")
	}
	seen := map[string]bool{}
	for _, item := range items {
		seen[item.sortKey] = true
		if !item.cursorAt.Equal(want[item.sortKey]) {
			t.Fatalf("%s stands at %v, want the change stamp %v", item.sortKey, item.cursorAt, want[item.sortKey])
		}
	}
	if len(seen) != 2 {
		t.Fatalf("page holds %d pull requests, want the limit of 2", len(seen))
	}
}

// One link id can join several work items. The link row names no issue
// repository, so that link names no project, wherever the rows are.
func TestALinkIdIsResolvedToOneIssueOrToNone(t *testing.T) {
	t.Parallel()
	link := func(repoID, id string) pullRequestIssueLink {
		return pullRequestIssueLink{issue: workItemKey{repoID: repoID, workItemID: id}, tier: "native"}
	}
	typed := func(repoID, id string) pullRequestIssueLink {
		row := link(repoID, id)
		row.pullRequestTyped = true
		return row
	}
	for name, tc := range map[string]struct {
		rows      []pullRequestIssueLink
		want      []pullRequestIssueLink
		ambiguous int
	}{
		"one row, in any repository": {[]pullRequestIssueLink{link("repo-b", "I-1")}, []pullRequestIssueLink{link("repo-b", "I-1")}, 0},
		"two rows":                   {[]pullRequestIssueLink{link("repo-a", "I-1"), link("repo-b", "I-1")}, []pullRequestIssueLink{}, 1},
		"each id on its own":         {[]pullRequestIssueLink{link("repo-b", "I-1"), link("repo-b", "I-2"), link("repo-c", "I-2")}, []pullRequestIssueLink{link("repo-b", "I-1")}, 1},
		// A pull-request work item counts as a row, then contributes nothing:
		// an issue elsewhere never stands in for it.
		"the only row is a pull request item":    {[]pullRequestIssueLink{typed("repo-b", "I-1")}, []pullRequestIssueLink{}, 0},
		"one of two rows is a pull request item": {[]pullRequestIssueLink{typed("repo-a", "I-1"), link("repo-b", "I-1")}, []pullRequestIssueLink{}, 1},
	} {
		got, ambiguous := resolveLinkedIssues(tc.rows)
		if !reflect.DeepEqual(got, tc.want) || ambiguous != tc.ambiguous {
			t.Errorf("%s: resolved %v, %d ambiguous; want %v, %d", name, got, ambiguous, tc.want, tc.ambiguous)
		}
	}
}
