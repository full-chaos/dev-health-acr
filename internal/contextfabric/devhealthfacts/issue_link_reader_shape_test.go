package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"
)

// Every statement the scope expander sends that reads the link of record
// reads it with one admission rule: the named pull request exists and the work
// item is an issue. The sweep covers the current-axis project and team hops,
// the as-of project hop, the unlinked-issue counts and the historical drop
// count.
func TestIssueLinkReaders_EveryLinkReadAdmitsOnlyExistingPullRequestsOfIssues(t *testing.T) {
	queries := sweepScopeExpander(context.Background(), "org-issue-link-shape")
	reading := 0
	for _, query := range queries {
		if !strings.Contains(query.statement, "work_graph_issue_pr") {
			continue
		}
		reading++
		if !strings.Contains(query.statement, "INNER JOIN git_pull_requests AS pr FINAL ON pr.org_id = l.org_id AND pr.repo_id = l.repo_id AND pr.number = l.pr_number") {
			t.Errorf("statement reads work_graph_issue_pr without joining the pull request it names:\n%s", query.statement)
		}
		if !strings.Contains(query.statement, "NOT IN ('pr', 'merge_request')") {
			t.Errorf("statement reads work_graph_issue_pr without excluding pull-request-typed work items:\n%s", query.statement)
		}
	}
	// The exact count of expander statements that read the link: a reader that
	// disappears or a new one that arrives must change this number on purpose.
	// The blocker rollup's own read (blockerEdgeRowsSQL) is pinned by
	// blocker_rollup_entity_tree_integration_test.go.
	const wantLinkReads = 21
	if reading != wantLinkReads {
		t.Fatalf("%d statements read work_graph_issue_pr, want %d: the sweep no longer reaches every link reader, or a reader was added", reading, wantLinkReads)
	}
}
