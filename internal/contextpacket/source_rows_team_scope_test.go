package contextpacket

import (
	"strings"
	"testing"
)

// The work item team evidence ref resolves a co-owner row as well as the
// primary one, because the edge projector mints a ref for each.
func TestWorkItemTeamSourceRowQueryReadsCoOwnerRows(t *testing.T) {
	t.Parallel()
	var statement string
	for _, query := range SourceRowOnlyQueriesV2 {
		if query.ID == "work_item_teams.v2" {
			statement = query.Statement
		}
	}
	if statement == "" {
		t.Fatal("no work_item_teams.v2 source row query")
	}
	if !strings.Contains(statement, "a.is_primary IN (1, 2)") || strings.Contains(statement, "is_primary = 1") {
		t.Errorf("the team evidence query must read primary and co-owner rows:\n%s", statement)
	}
}
