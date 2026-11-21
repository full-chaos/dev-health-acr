package devhealthfacts_test

import (
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// TestTeamLinkCandidatesTableSendsEachIssueOnce pins the Go-side compaction of
// the team link candidates (CHAOS-10482): the statement groups on the id, and
// the table still sends an id once when a stream repeats it.
func TestTeamLinkCandidatesTableSendsEachIssueOnce(t *testing.T) {
	// Synthetic: issue-b is stored under two repo_ids, issue-a has three
	// versions, issue-c has both.
	rowsAsRead := []string{
		"issue-b", "issue-a", "issue-b", "issue-a", "issue-c",
		"issue-a", "issue-c", "issue-c", "issue-b",
	}
	want := []string{"issue-a", "issue-b", "issue-c"}

	table, err := devhealthfacts.TeamLinkCandidatesTableForTest(slices.Clone(rowsAsRead))
	if err != nil {
		t.Fatalf("table: %v", err)
	}
	block := table.Block()
	if block.Rows() != len(want) {
		t.Fatalf("table rows = %d, want %d", block.Rows(), len(want))
	}
	if len(block.Columns) != 1 {
		t.Fatalf("table columns = %d, want 1", len(block.Columns))
	}
	got := make([]string, 0, block.Rows())
	for i := 0; i < block.Rows(); i++ {
		got = append(got, block.Columns[0].Row(i, false).(string))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("table ids = %v, want %v", got, want)
	}
}
