package devhealthfacts

import "testing"

// The whole-organization split (organization mix, organization unit listing)
// keeps its text: only the repository-scoped and linked pull request reads
// moved to the folded split.
func TestWholeOrganizationSplitTextIsUnchanged(t *testing.T) {
	t.Parallel()
	for _, sentinel := range []bool{false, true} {
		for _, scope := range []membershipScope{subqueryMembershipScope, {mode: membershipScopeNone}, {mode: membershipScopeIDs, ids: []string{"a"}}} {
			for _, cols := range [][2]string{{"", ""}, {" from_ts, to_ts,", unitsCore}} {
				got := repoSplitCoreScoped([]string{"if(1, 0, -1)"}, cols[0], cols[1], sentinel, scope)
				if want := legacySplitCore([]string{"if(1, 0, -1)"}, cols[0], cols[1], sentinel, scope, false); got != want {
					t.Fatalf("split text changed (sentinel=%v scope=%v cols=%q)", sentinel, scope.mode, cols[0])
				}
			}
		}
	}
}
