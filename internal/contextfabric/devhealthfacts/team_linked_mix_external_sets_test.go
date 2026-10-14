package devhealthfacts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// The team linked pull request mix takes its linked set and its membership
// scope ids as external tables, and nothing else about it changed: with both
// put back into its text it is the statement that inlined them.
func TestTeamLinkedMixSendsItsLinkedSetAndScopeAsExternalTables(t *testing.T) {
	t.Parallel()
	sent, inlined := devhealthfacts.TeamLinkedMixStatementsForTest()
	for _, inline := range []string{"work_graph_issue_pr", "team_project_ownership", "{scope_json:String}"} {
		if strings.Contains(sent, inline) {
			t.Errorf("the team linked mix statement still inlines %s", inline)
		}
	}
	for table, want := range map[string]int{"_team_linked_pull_requests": 4, "_membership_scope": 1} {
		if got := strings.Count(sent, table); got != want {
			t.Errorf("the team linked mix statement reads %s %d times, want %d", table, got, want)
		}
	}
	before, err := os.ReadFile(filepath.Join("testdata", "team_linked_mix_inline_sets.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if inlined != string(before) {
		t.Errorf("with its external tables inlined the statement is not the statement that inlined them (%d vs %d bytes)", len(inlined), len(before))
	}
}
