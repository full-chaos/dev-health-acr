package answerprojection

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4636: the projection clamp stops being group-blind.
//
// RED ON origin/main (f9d9688c) by symbol absence -- ContextFabricCohortGroup
// and ContextFabricProjectedCohortGroup do not exist there, so this file does
// not compile. TestGroupAwareProjectionIsNotALeadingPrefix additionally
// carries a MUTATION red, named in its own comment, because the defect it
// pins is a one-token change away and would otherwise pass every other test
// in this package.

func groupedCohortFixture(perGroup map[string][]string) *contractsv1.ContextFabricCohort {
	cohort := &contractsv1.ContextFabricCohort{
		Kind:      contractsv1.ContextFabricSubjectProject,
		Rationale: "grouped fixture",
		Complete:  true,
	}
	// Members are laid out GROUP-MAJOR, which is both the realistic layout
	// and the discriminating one. Realistic because BuildCohortGroups
	// preserves the cohort's own member order and discovery order is
	// canonical-id-lexical, so a team's projects arrive together.
	// Discriminating because it puts SKIPPED members BEFORE admitted ones:
	// with a1..a4 then b1..b2 and a 3-member budget, the allowance admits
	// a1, b1, a2 -- so a3 and a4 are skipped while b1 is still to come. An
	// interleaved fixture never reaches a skip before the budget fills, and
	// would pass under the very defect this file exists to pin. (Found by
	// EXECUTING the mutation, not by reading the code: the first version of
	// this fixture interleaved, and the `break` mutation passed it.)
	order := []string{"team_a", "team_b"}
	rank := 0
	for _, group := range order {
		for _, id := range perGroup[group] {
			rank++
			cohort.Members = append(cohort.Members, contractsv1.ContextFabricCohortMember{
				Subject:          subject(contractsv1.ContextFabricSubjectProject, id, id),
				Rank:             rank,
				InclusionReasons: []string{"Graph retrieval associated this subject with the requested condition."},
			})
		}
	}
	for _, group := range order {
		ids := perGroup[group]
		cohort.Groups = append(cohort.Groups, contractsv1.ContextFabricCohortGroup{
			Subject:            subject(contractsv1.ContextFabricSubjectTeam, group, group),
			MemberCanonicalIDs: ids,
			Complete:           true,
			Total:              len(ids),
		})
	}
	return cohort
}

// TestGroupAwareProjectionIsNotALeadingPrefix is the defect this slice
// removes, pinned.
//
// MUTATION RED: change the group-allowance skip in projectCohort from
// `continue` back to `break` and this test fails -- that single token is the
// difference between "allocate across groups" and the leading-prefix cut that
// could return every project of team A and none of team B.
func TestGroupAwareProjectionIsNotALeadingPrefix(t *testing.T) {
	t.Parallel()
	result := richResult()
	result.Cohort = groupedCohortFixture(map[string][]string{
		"team_a": {"a1", "a2", "a3", "a4"},
		"team_b": {"b1", "b2"},
	})
	bounds := DefaultBudget
	bounds.MaxCohortMembers = 3

	projection := Project(result, bounds)
	if projection.Cohort == nil {
		t.Fatal("projection dropped the cohort entirely")
	}
	if len(projection.Cohort.Members) != 3 {
		t.Fatalf("projected %d members, want the 3 the budget admits", len(projection.Cohort.Members))
	}
	// EVERY group survives. This is the assertion that fails under the
	// leading-prefix cut.
	if len(projection.Cohort.Groups) != 2 {
		t.Fatalf("projected %d groups, want both -- a group-blind clamp is worse than a refusal because it looks like an answer", len(projection.Cohort.Groups))
	}
	for _, group := range projection.Cohort.Groups {
		if len(group.MemberCanonicalIDs) == 0 {
			t.Fatalf("group %q survived with no members", group.Subject.CanonicalID)
		}
	}
	if projection.ProjectionBudget.CohortGroupsOmitted != 0 {
		t.Fatalf("CohortGroupsOmitted = %d, want 0 -- member-first narrowing keeps every group", projection.ProjectionBudget.CohortGroupsOmitted)
	}
	// Total still reports the true size, so the caller sees what was cut.
	if projection.Cohort.Total != 6 {
		t.Fatalf("Total = %d, want the canonical 6", projection.Cohort.Total)
	}
	// A truncated group says so against its ORIGINAL total.
	truncated := 0
	for _, group := range projection.Cohort.Groups {
		if group.Truncated {
			truncated++
		}
	}
	if truncated == 0 {
		t.Fatal("no projected group disclosed truncation, but 3 of 6 members were cut")
	}
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection.Validate() = %v", err)
	}
}

// TestFlatCohortProjectionIsUnchanged is the discriminating control. Every
// cohort this projection carried before this slice has no group axis, and
// must be projected byte-identically -- a build that over-applies the
// group-aware path flips exactly this.
func TestFlatCohortProjectionIsUnchanged(t *testing.T) {
	t.Parallel()
	result := richResult()
	bounds := DefaultBudget
	bounds.MaxCohortMembers = 1

	projection := Project(result, bounds)
	if projection.Cohort == nil {
		t.Fatal("projection dropped the cohort")
	}
	if len(projection.Cohort.Groups) != 0 {
		t.Fatalf("a flat cohort gained %d groups", len(projection.Cohort.Groups))
	}
	if projection.ProjectionBudget.CohortGroupsOmitted != 0 {
		t.Fatalf("CohortGroupsOmitted = %d on a flat cohort", projection.ProjectionBudget.CohortGroupsOmitted)
	}
	if len(projection.Cohort.Members) != 1 {
		t.Fatalf("flat clamp kept %d members, want the leading 1", len(projection.Cohort.Members))
	}
}

// TestAGroupThatLosesEveryMemberIsCountedNotEmitted: an empty group reads as
// "this team has no projects", which is a different and false answer.
func TestAGroupThatLosesEveryMemberIsCountedNotEmitted(t *testing.T) {
	t.Parallel()
	result := richResult()
	result.Cohort = groupedCohortFixture(map[string][]string{
		"team_a": {"a1"},
		"team_b": {"b1"},
	})
	// One member of budget across two groups: one group necessarily loses
	// everything. Member-first narrowing makes this rare, which is exactly
	// why it must be counted rather than expected.
	bounds := DefaultBudget
	bounds.MaxCohortMembers = 1

	projection := Project(result, bounds)
	if len(projection.Cohort.Groups) != 1 {
		t.Fatalf("projected %d groups, want 1 surviving", len(projection.Cohort.Groups))
	}
	if projection.ProjectionBudget.CohortGroupsOmitted != 1 {
		t.Fatalf("CohortGroupsOmitted = %d, want 1", projection.ProjectionBudget.CohortGroupsOmitted)
	}
	if !projection.ProjectionBudget.Truncated {
		t.Fatal("a projection that lost a whole group did not declare itself truncated")
	}
}
