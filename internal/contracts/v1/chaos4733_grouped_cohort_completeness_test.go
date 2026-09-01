package v1

import "testing"

// CHAOS-4733 (independent review finding R3): ContextFabricCohort.validate
// used to require its Complete/Truncated to be EXACTLY EQUAL to
// CohortCompletenessFromGroups(c.Groups) -- a rule that made a discovery-
// level truncation (the whole cohort capped before any group existed)
// unrepresentable, because no individual, freshly built group can legally
// claim Truncated=true (Validate enforces Truncated => Total >
// len(MemberCanonicalIDs)). The rule is now "at least as conservative as":
// the cohort may be MORE conservative than its groups (Complete=false or
// Truncated=true while every group looks fine on its own), but never LESS
// (Complete=true while a group is not, or Truncated=false while a group is).

func chaos4733CohortFixture(groups []ContextFabricCohortGroup, complete, truncated bool) ContextFabricCohort {
	return ContextFabricCohort{
		Kind: ContextFabricSubjectProject,
		Members: []ContextFabricCohortMember{{
			Subject:          ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_a", Label: "a"},
			Rank:             1,
			InclusionReasons: []string{"fixture"},
		}},
		Rationale: "fixture",
		Complete:  complete,
		Truncated: truncated,
		Groups:    groups,
	}
}

func chaos4733CompleteGroup() []ContextFabricCohortGroup {
	return []ContextFabricCohortGroup{
		{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_1", Label: "team_1"}, MemberCanonicalIDs: []string{"project_a"}, Complete: true, Total: 1},
	}
}

func chaos4733TruncatedGroup() []ContextFabricCohortGroup {
	return []ContextFabricCohortGroup{
		{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_1", Label: "team_1"}, MemberCanonicalIDs: []string{"project_a"}, Truncated: true, Total: 2},
	}
}

// TestCohortMoreConservativeThanItsGroupsIsAllowed is the CHAOS-4733 fix
// itself: a cohort may declare Complete=false / Truncated=true even though
// every one of its groups individually looks complete -- the discovery-
// level truncation case this ticket exists to fix.
func TestCohortMoreConservativeThanItsGroupsIsAllowed(t *testing.T) {
	t.Parallel()
	cohort := chaos4733CohortFixture(chaos4733CompleteGroup(), false, true)
	if err := cohort.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil -- a cohort more conservative than its (complete) groups must be allowed", err)
	}
}

// TestCohortLessConservativeThanItsGroupsIsRefused pins the direction the
// original CHAOS-4636 equality check protected and that must still be
// refused: a cohort cannot claim Complete=true while a group is not, or
// Truncated=false while a group is truncated.
func TestCohortLessConservativeThanItsGroupsIsRefused(t *testing.T) {
	t.Parallel()
	t.Run("complete=true over an incomplete group", func(t *testing.T) {
		t.Parallel()
		incomplete := []ContextFabricCohortGroup{
			{Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_1", Label: "team_1"}, MemberCanonicalIDs: []string{"project_a"}, Complete: false, Total: 1},
		}
		cohort := chaos4733CohortFixture(incomplete, true, false)
		if err := cohort.Validate(); err == nil {
			t.Fatal("Validate() = nil, want an error: cohort claims complete=true while its group does not")
		}
	})
	t.Run("truncated=false while a group is truncated", func(t *testing.T) {
		t.Parallel()
		cohort := chaos4733CohortFixture(chaos4733TruncatedGroup(), false, false)
		if err := cohort.Validate(); err == nil {
			t.Fatal("Validate() = nil, want an error: cohort hides a group's own truncated=true")
		}
	})
}

// TestCohortExactlyMatchingItsGroupsIsStillAllowed is the pre-CHAOS-4733
// baseline: the original exact-equality case must still validate under the
// relaxed (at-least-as-conservative) rule, since equality is one point on
// the allowed range, not excluded by it.
func TestCohortExactlyMatchingItsGroupsIsStillAllowed(t *testing.T) {
	t.Parallel()
	if err := chaos4733CohortFixture(chaos4733CompleteGroup(), true, false).Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (exact match, both complete)", err)
	}
	if err := chaos4733CohortFixture(chaos4733TruncatedGroup(), false, true).Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (exact match, both truncated)", err)
	}
}
