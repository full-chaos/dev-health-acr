package devhealthfacts

import (
	"strings"
	"testing"
)

// The statements built on repoSplitCore peak above the 64 MiB API default, so
// each carries its own named limit.
func TestInvestmentSplitStatementsCarryTheirOwnMemoryLimit(t *testing.T) {
	t.Parallel()
	bound := factTimeBound{}
	scope := membershipScope{mode: membershipScopeIDs, ids: []string{"a"}}
	statements := map[string]string{
		"repository/team mix": repoMixStatementScoped([]factTimeBound{bound}, scope, 100),
		"organization mix":    orgMixStatement(bound),
		"repository units":    investmentUnitsStatementFor(bound, false, scope, false),
		"organization units":  investmentUnitsStatementFor(bound, true, scope, true),
	}
	for name, statement := range statements {
		if !strings.Contains(statement, "max_memory_usage = 536870912") {
			t.Errorf("%s statement carries no investmentSplitMaxMemoryUsage limit:\n%.200s", name, statement)
		}
	}
}
