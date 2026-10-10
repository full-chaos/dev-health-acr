package devhealthfacts

import (
	"strings"
	"testing"
)

// The organization listing and the repository listing are different statements:
// only the organization's selects rows that reach no resolved repository, and it
// carries no repository set.
func TestOrganizationUnitsStatementKeepsUnattributedRowsAndNoRepositoryFilter(t *testing.T) {
	t.Parallel()
	organization := investmentOrgUnitsStatement(factTimeBound{}, false, subqueryMembershipScope)
	repository := investmentUnitsStatementScoped(factTimeBound{}, false, subqueryMembershipScope)
	if strings.Contains(organization, "repo_uuid IN {ids:Array(String)}") || strings.Contains(organization, "WHERE repo_uuid != ''") {
		t.Error("the organization statement filters out unattributed rows or by a repository set")
	}
	if !strings.Contains(organization, "unit:") {
		t.Error("the organization statement gives a unit with no reference and no repository no unattributed row")
	}
	if strings.Contains(repository, "unit:") || !strings.Contains(repository, "WHERE repo_uuid != '' AND repo_uuid IN {ids:Array(String)}") {
		t.Error("the repository and team listing changed: it must keep selecting only resolved repositories of its set")
	}
}
