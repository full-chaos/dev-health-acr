package contextfabric

import (
	"fmt"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func groupedCohort(groups, perGroup int) Cohort {
	cohort := Cohort{Kind: contractsv1.ContextFabricSubjectKind("project")}
	for g := 0; g < groups; g++ {
		group := contractsv1.ContextFabricCohortGroup{Subject: contractsv1.ContextFabricSubjectRef{CanonicalID: fmt.Sprintf("team_%d", g)}}
		for m := 0; m < perGroup; m++ {
			id := fmt.Sprintf("project_%d_%d", g, m)
			cohort.Members = append(cohort.Members, contractsv1.ContextFabricCohortMember{Subject: contractsv1.ContextFabricSubjectRef{CanonicalID: id}})
			group.MemberCanonicalIDs = append(group.MemberCanonicalIDs, id)
		}
		cohort.Groups = append(cohort.Groups, group)
	}
	return cohort
}

// TestGroupedAllowanceFollowsBudgetTwoGroupsThreeMembers drives the real plan
// and the real stage-2 narrowing: the expected counts are literals.
func TestGroupedAllowanceFollowsBudgetTwoGroupsThreeMembers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		maxItems, headroom, allowance, served int
	}{
		{45, 20, 25, 6},
		{30, 20, 10, 6},
		{20, 10, 10, 6},
		{16, 6, 10, 6},
		{12, 2, 10, 6},
		{11, 1, 10, 6},
		{10, 1, 9, 6},
		{4, 1, 3, 3},
		{2, 1, 1, 2},
		{1, 1, 1, 2},
	}
	for _, c := range cases {
		planned := planBudget(PlanBudgetGroupedCohort, ResponseBudget{MaxItems: c.maxItems, MaxSerializedBytes: 262144}, 0, false)
		if planned.SynthesisHeadroom != c.headroom || planned.MaxMembers != c.allowance {
			t.Errorf("max_items=%d: headroom=%d allowance=%d, want %d/%d", c.maxItems, planned.SynthesisHeadroom, planned.MaxMembers, c.headroom, c.allowance)
		}
		cohort := groupedCohort(2, 3)
		served := len(cohort.Members)
		if kept, _, narrowed, _ := NarrowGroupedCohort(&cohort, planned.MaxMembers); narrowed {
			served = len(kept)
		}
		if served != c.served {
			t.Errorf("max_items=%d: served %d members, want %d", c.maxItems, served, c.served)
		}
		for _, group := range cohort.Groups {
			if len(group.MemberCanonicalIDs) == 0 {
				t.Errorf("max_items=%d: a group lost every member", c.maxItems)
			}
		}
	}
}

// TestSynthesisHeadroomIsUnchangedAtAndAboveTheProfileCapPlusTen pins the
// byte-identity: every budget at or above cap+10 plans exactly as before, and
// the table is crossed with every profile, literals only.
func TestSynthesisHeadroomIsUnchangedAtAndAboveTheProfileCapPlusTen(t *testing.T) {
	t.Parallel()
	profiles := []struct {
		profile PlanBudgetProfile
		cap     int
	}{
		{PlanBudgetGroupedCohort, 20}, {PlanBudgetFlatCohort, 16},
		{PlanBudgetSingleSubject, 12}, {PlanBudgetMatchedPair, 12}, {PlanBudgetUnbounded, 0},
	}
	var table strings.Builder
	for _, p := range profiles {
		for _, maxItems := range []int{0, 1, 2, 11, 12, 15, 20, 21, 22, 26, 29, 30, 31, 45, 50} {
			got := planBudget(p.profile, ResponseBudget{MaxItems: maxItems, MaxSerializedBytes: 262144}, 0, false).SynthesisHeadroom
			want := p.cap
			if p.cap > 0 && maxItems > 0 && maxItems < p.cap+10 {
				want = max(1, maxItems-10)
			}
			if got != want {
				t.Errorf("profile %q max_items=%d: headroom %d, want %d", p.profile, maxItems, got, want)
			}
			if maxItems >= 30 && got != p.cap {
				t.Errorf("profile %q max_items=%d: headroom %d moved off the cap %d", p.profile, maxItems, got, p.cap)
			}
			fmt.Fprintf(&table, "| %s | %d | %d |\n", p.profile, maxItems, got)
		}
	}
	t.Logf("\n| profile | max_items | headroom |\n|---|---|---|\n%s", table.String())
}
