package devhealthfacts

import (
	"testing"
	"time"
)

// The whole-organization reads (organization mix, organization unit listing)
// keep their full text, split and outer statement, named limit included,
// except the bugfix share, read as one map key of the latest version: only
// the repository-scoped and linked pull request reads moved to the folded
// split.
func TestWholeOrganizationSplitTextChangesOnlyTheBugfixRead(t *testing.T) {
	t.Parallel()
	for _, sentinel := range []bool{false, true} {
		for _, scope := range []membershipScope{subqueryMembershipScope, {mode: membershipScopeNone}, {mode: membershipScopeIDs, ids: []string{"a"}}} {
			for _, cols := range [][2]string{{"", ""}, {" from_ts, to_ts,", unitsCore}} {
				got := repoSplitCoreScoped([]string{"if(1, 0, -1)"}, cols[0], cols[1], sentinel, scope)
				if want := bugfixKeyRead(legacySplitCore([]string{"if(1, 0, -1)"}, cols[0], cols[1], sentinel, scope, false)); got != want {
					t.Fatalf("split text changed (sentinel=%v scope=%v cols=%q)", sentinel, scope.mode, cols[0])
				}
			}
		}
	}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for name, bound := range map[string]factTimeBound{
		"current axis":  {},
		"range":         {active: true, hasStart: true, start: start, end: end},
		"point in time": {active: true, end: end},
	} {
		if got, want := orgMixStatement(bound), bugfixKeyRead(legacyOrganizationMixStatement(bound)); got != want {
			t.Errorf("%s: organization mix text changed", name)
		}
		for _, scope := range []membershipScope{subqueryMembershipScope, {mode: membershipScopeNone}, {mode: membershipScopeIDs, ids: []string{"a"}}} {
			for _, cursor := range []bool{false, true} {
				if got, want := investmentUnitsStatementFor(bound, cursor, scope, true), bugfixKeyRead(legacyOrganizationUnitsStatement(bound, cursor, scope)); got != want {
					t.Errorf("%s/scope %v/cursor %v: organization unit listing text changed", name, scope.mode, cursor)
				}
			}
		}
	}
}
