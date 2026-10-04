package contextfabric

import "testing"

func TestMemberTimeRoleBinderReadsTheNounPhraseNotJustTheClause(t *testing.T) {
	cases := []struct {
		question string
		reason   MemberTimeRoleReason
		role     MemberTimeRole
	}{
		{"Which closed issues were created in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCreated},
		{"Which new work items were closed in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCompleted},
		{"Which done tickets were updated in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleUpdated},
		{"Which updated issues were created in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCreated},
		{"List the opened bugs closed in the last 30 days", MemberTimeRoleBound, MemberTimeRoleCompleted},
		{"Which done epics were created in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCreated},
		{"Which closed stories were created in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCreated},
		// a lone modifier still binds: no predicate verb competes with it
		{"Which closed issues exist in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCompleted},
		{"Which new work items in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCreated},
		// two predicate verbs, or two modifiers with none, stay ambiguous
		{"Which closed issues were created and updated in the last 30 days?", MemberTimeRoleAmbiguous, ""},
		{"Which new closed issues exist in the last 30 days?", MemberTimeRoleAmbiguous, ""},
		{"Which issues were created and closed in the last 30 days?", MemberTimeRoleAmbiguous, ""},
		// a form not directly before a noun is a verb, even beside a noun
		{"Which issues were closed tickets in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCompleted},
	}
	for _, tc := range cases {
		outcome := BindMemberTimeRole(tc.question, windowSpanOf(t, tc.question))
		if outcome.Reason != tc.reason || outcome.Role != tc.role {
			t.Errorf("%q: reason=%s role=%q, want %s %q", tc.question, outcome.Reason, outcome.Role, tc.reason, tc.role)
		}
	}
}

func TestWorkItemWindowRoleIgnoresAModifierInTheNounPhraseOnTheEngine(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, tc := range []struct{ question, column string }{
		{"Which closed work items in Project Alpha were created in the last 30 days?", "created_at"},
		{"Which new work items in Project Alpha were closed in the last 30 days?", "completed_at"},
	} {
		run := runTupleFilterCase(t, periodTupleFrame(), WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 1}, statedPeriodRequest(tc.question))
		if run.invokedErr != nil {
			t.Fatalf("%s: %v", tc.question, run.invokedErr)
		}
		if run.reads != 1 || run.request.TimeColumn != tc.column {
			t.Errorf("%s: reads=%d column=%q, want one read on %s", tc.question, run.reads, run.request.TimeColumn, tc.column)
		}
	}
}
