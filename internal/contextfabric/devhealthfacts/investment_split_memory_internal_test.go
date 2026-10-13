package devhealthfacts

import (
	"strings"
	"testing"
)

// The organization statements split every unit of the organization and carry
// their own named limit; the repository and team reads fold per unit and run
// under the API default.
func TestInvestmentSplitStatementsCarryTheirOwnMemoryLimit(t *testing.T) {
	t.Parallel()
	bound := factTimeBound{}
	scope := membershipScope{mode: membershipScopeIDs, ids: []string{"a"}}
	limit := "max_memory_usage = 536870912"
	for name, statement := range map[string]string{
		"organization mix":   orgMixStatement(bound),
		"organization units": investmentUnitsStatementFor(bound, true, scope, true),
	} {
		if !strings.Contains(statement, limit) {
			t.Errorf("%s statement carries no investmentSplitMaxMemoryUsage limit:\n%.200s", name, statement)
		}
	}
	for name, statement := range map[string]string{
		"repository/team mix":          repoMixStatementScoped([]factTimeBound{bound}, scope, 100),
		"repository units":             investmentUnitsStatementFor(bound, false, scope, false),
		"team linked pull request mix": linkedPullRequestMixStatement(bound, scope, 100),
	} {
		if strings.Contains(statement, "max_memory_usage") {
			t.Errorf("%s statement carries a named memory limit; it runs under the API default:\n%.200s", name, statement)
		}
	}
}
