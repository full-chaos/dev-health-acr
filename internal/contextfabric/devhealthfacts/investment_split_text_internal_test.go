package devhealthfacts

import "testing"

// The whole-organization split (organization mix, organization unit listing,
// team linked pull request mix) keeps its text: only the repository-scoped
// reads moved to the requested split.
func TestWholeOrganizationSplitTextIsUnchanged(t *testing.T) {
	t.Parallel()
	for _, perRef := range []bool{false, true} {
		for _, sentinel := range []bool{false, true} {
			for _, scope := range []membershipScope{subqueryMembershipScope, {mode: membershipScopeNone}, {mode: membershipScopeIDs, ids: []string{"a"}}} {
				for _, cols := range [][2]string{{"", ""}, {" from_ts, to_ts,", unitsCore}} {
					got := repoSplitCoreGrouped([]string{"if(1, 0, -1)"}, cols[0], cols[1], sentinel, scope, perRef)
					if want := legacySplitCore([]string{"if(1, 0, -1)"}, cols[0], cols[1], sentinel, scope, perRef); got != want {
						t.Fatalf("split text changed (perRef=%v sentinel=%v scope=%v cols=%q)", perRef, sentinel, scope.mode, cols[0])
					}
				}
			}
		}
	}
}
