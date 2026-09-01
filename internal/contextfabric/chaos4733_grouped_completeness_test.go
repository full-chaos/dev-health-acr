package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4733 (independent review finding R3, fable51-independent-review-
// intent-plan-2026-09-01.md): a cohort truncated at discovery
// (Truncated=true, or Complete=false after a truncated census) was rewritten
// to Complete=true, Truncated=false the moment it was grouped, because
// BuildCohortGroups built every group Complete:true/Truncated:false
// unconditionally and ApplyGroupedCohortCompleteness then derived the
// cohort-level flags ONLY from the groups. On the rig, a 20-project org
// capped at discovery to 10 members reported a grouped answer as complete.
//
// RED on origin/main (a8441bce) / current tip: BuildCohortGroups hardcoded
// Complete:true, Truncated:false regardless of the pre-grouping cohort's own
// flags. GREEN on the fix: BuildCohortGroups seeds each group's
// Complete/Truncated from cohort.Complete/cohort.Truncated as they stand at
// call time (the pre-grouping, discovery-level values), so the existing
// group conjunction in ApplyGroupedCohortCompleteness can never regress to
// Complete=true over a cohort that was never fully discovered.
//
// TestChaos4733GroupingErasesDiscoveryTruncation_RigScenario (external test,
// chaos4733_grouped_completeness_external_test.go) reproduces the ticket's
// own §repro end to end through graphrank.DiscoveredCohort; the tests here
// pin the same invariant directly against BuildCohortGroups/
// ApplyGroupedCohortCompleteness, including the census-truncated path that
// discovery alone cannot exercise (falkorgraph/reader.go's Complete=false
// override, which never sets Truncated).

// TestGroupedCohortCompletenessNeverRegressesFromGroupsAlone is the direct
// unit-level pin of the defect: a cohort discovery capped (Complete=false,
// Truncated=true) must still read Complete=false, Truncated=true -- and
// every one of its groups must read Complete=false -- after BuildCohortGroups
// + ApplyGroupedCohortCompleteness, even though every member that WAS
// discovered placed cleanly into a group.
func TestGroupedCohortCompletenessNeverRegressesFromGroupsAlone(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("project_a", "project_b", "project_c")
	// Simulate a discovery cap: the cohort's own pre-grouping flags say it
	// is NOT the whole population, exactly as graphrank.DiscoveredCohort
	// sets them when len(members) >= MaxCohortMembers.
	cohort.Complete = false
	cohort.Truncated = true

	facts := []CanonicalFact{
		planFixtureFacts("project_a", "team_1", "Platform"),
		planFixtureFacts("project_b", "team_2", "Growth"),
		planFixtureFacts("project_c", "team_1", "Platform"),
	}
	groups, ungrouped := BuildCohortGroups(AnswerPlan{GroupKind: SubjectTeam}, cohort, facts)
	if ungrouped != 0 {
		t.Fatalf("ungrouped = %d, want 0 -- every member placed cleanly", ungrouped)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
	for _, g := range groups {
		// DEFECT ASSERTION: a group built from a discovery-truncated cohort
		// must not claim Complete=true just because every member IT SAW
		// placed successfully -- an undiscovered member could belong to it.
		if g.Complete {
			t.Fatalf("group %q Complete=true, but the pre-grouping cohort was discovery-truncated", g.Subject.CanonicalID)
		}
		// Truncated is NOT inherited from the pre-grouping cohort (option
		// (b): it would violate this group's own Total invariant, since
		// Total was just set to len(members) and Truncated requires
		// Total > len(members)). The discovery-level truncation must
		// still be visible, but at the COHORT level below, not here.
		if g.Truncated {
			t.Fatalf("group %q Truncated=true with Total==len(members) -- violates ContextFabricCohortGroup.Validate's own invariant", g.Subject.CanonicalID)
		}
	}

	cohort.Groups = groups
	ApplyGroupedCohortCompleteness(cohort)

	// DEFECT ASSERTION (the ticket's own repro shape): the cohort-level
	// flags must not regress to Complete=true, Truncated=false just because
	// the groups it built happened to look complete relative to each
	// other. This is the exact state a8441bce produced on the rig.
	if cohort.Complete {
		t.Fatal("cohort.Complete=true after grouping a discovery-truncated cohort")
	}
	if !cohort.Truncated {
		t.Fatal("cohort.Truncated=false after grouping a discovery-truncated cohort")
	}
	if err := cohort.Validate(); err != nil {
		t.Fatalf("cohort.Validate() = %v", err)
	}
}

// TestGroupedCohortCompletenessTableIsConservative is the AC4 table test:
// every legal pre-grouping (Complete, Truncated) pair -- including the
// census-truncated override (falkorgraph/reader.go:798) that sets
// Complete=false WITHOUT setting Truncated=true, a state discovery alone
// never produces -- must survive BuildCohortGroups + grouping unchanged.
// "Survive unchanged" is the conservative-completeness rule: grouping may
// never IMPROVE a cohort's declared completeness, only preserve or (via a
// group's own placement) further degrade it.
func TestGroupedCohortCompletenessTableIsConservative(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		preComplete       bool
		preTruncated      bool
		wantGroupComplete bool
	}{
		{name: "fully discovered", preComplete: true, preTruncated: false, wantGroupComplete: true},
		{name: "discovery capped", preComplete: false, preTruncated: true, wantGroupComplete: false},
		{name: "census-truncated override (Complete=false, Truncated=false)", preComplete: false, preTruncated: false, wantGroupComplete: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cohort := planFixtureCohort("project_a")
			cohort.Complete = tc.preComplete
			cohort.Truncated = tc.preTruncated
			facts := []CanonicalFact{planFixtureFacts("project_a", "team_1", "Platform")}

			groups, _ := BuildCohortGroups(AnswerPlan{GroupKind: SubjectTeam}, cohort, facts)
			if len(groups) != 1 {
				t.Fatalf("groups = %d, want 1", len(groups))
			}
			// A freshly built group is never itself Truncated (Total ==
			// len(members) always holds right after BuildCohortGroups); the
			// discovery-level truncation, if any, is a cohort-level fact.
			if groups[0].Complete != tc.wantGroupComplete || groups[0].Truncated {
				t.Fatalf("group Complete=%v Truncated=%v, want Complete=%v Truncated=false",
					groups[0].Complete, groups[0].Truncated, tc.wantGroupComplete)
			}

			cohort.Groups = groups
			ApplyGroupedCohortCompleteness(cohort)
			if cohort.Complete != tc.preComplete || cohort.Truncated != tc.preTruncated {
				t.Fatalf("post-grouping cohort Complete=%v Truncated=%v, want the pre-grouping values Complete=%v Truncated=%v (grouping must never change them when nothing else degrades)",
					cohort.Complete, cohort.Truncated, tc.preComplete, tc.preTruncated)
			}
			if err := cohort.Validate(); err != nil {
				t.Fatalf("cohort.Validate() = %v", err)
			}
		})
	}
}

// TestGroupedCohortCompletenessMutationProof pins the fix against the exact
// regression the ticket names: restoring the unconditional
// Complete:true/Truncated:false literal (or dropping either clause) in
// BuildCohortGroups. This test cannot execute that mutation itself -- it
// documents the expected observable difference so a manual mutation run
// (required by cf-common's mutation-proof rule) has a single, unambiguous
// assertion to watch flip. The mutation-proof step for this fix is: revert
// BuildCohortGroups's Complete/Truncated fields to literal true/false, rerun
// this file, and confirm TestGroupedCohortCompletenessNeverRegressesFromGroupsAlone
// and TestGroupedCohortCompletenessTableIsConservative both fail; restore
// the fix and confirm both pass again (sha256 digests of the mutated and
// restored file recorded in the PR body).
func TestGroupedCohortCompletenessMutationProof(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("only_member")
	cohort.Complete = false
	cohort.Truncated = true
	groups, _ := BuildCohortGroups(AnswerPlan{GroupKind: SubjectTeam}, cohort,
		[]CanonicalFact{planFixtureFacts("only_member", "team_1", "Platform")})
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	// A literal `Complete: true` (the pre-fix code) makes this fail; the
	// fix (`Complete: cohort.Complete`) makes it pass. One clause, one
	// mutation site, one assertion -- the shape cf-common's "mutate clauses,
	// not conditions" rule asks for.
	if groups[0].Complete {
		t.Fatal("MUTATION-PROOF FAILURE: group.Complete=true from a discovery-truncated cohort -- BuildCohortGroups is back to the unconditional literal")
	}

	// Second mutation site: ApplyGroupedCohortCompleteness reverted to an
	// unconditional overwrite (`cohort.Complete, cohort.Truncated =
	// CohortCompletenessFromGroups(cohort.Groups)` instead of AND/OR-ing
	// onto the existing values) would lose the discovery-level Truncated
	// signal even with the BuildCohortGroups fix in place, because a
	// freshly built group never carries Truncated=true itself.
	cohort.Groups = groups
	ApplyGroupedCohortCompleteness(cohort)
	if !cohort.Truncated {
		t.Fatal("MUTATION-PROOF FAILURE: cohort.Truncated=false after grouping a discovery-truncated cohort -- ApplyGroupedCohortCompleteness is back to overwriting instead of folding")
	}
	if cohort.Complete {
		t.Fatal("MUTATION-PROOF FAILURE: cohort.Complete=true after grouping a discovery-truncated cohort")
	}
}

// TestGroupedCohortCompletenessNarrowingStillAndsIn confirms
// NarrowGroupedCohort's existing conjunction (narrowedGroup.Complete =
// !narrowedGroup.Truncated && group.Complete) composes correctly with an
// already-dishonest-looking incoming group: a group that entered narrowing
// with Complete=false (because BuildCohortGroups correctly inherited a
// discovery-level truncation, per this ticket's fix) must not come OUT of
// narrowing Complete=true merely because narrowing itself did not need to
// trim that particular group's own members.
func TestGroupedCohortCompletenessNarrowingStillAndsIn(t *testing.T) {
	t.Parallel()
	cohort := planFixtureCohort("a1", "b1", "b2")
	cohort.Groups = []contractsv1.ContextFabricCohortGroup{
		// ta already carries the discovery-truncation this ticket fixes:
		// Complete=false even though it lists all 1 member it was built
		// from (Total==len(members)). Truncated stays false, matching the
		// invariant ContextFabricCohortGroup.Validate enforces (Truncated
		// implies Total > len(MemberCanonicalIDs)) -- the same legal
		// Complete=false/Truncated=false state BuildCohortGroups now
		// produces for a freshly built group under a discovery-truncated
		// cohort (CHAOS-4733 option (b)).
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "ta", Label: "ta"}, MemberCanonicalIDs: []string{"a1"}, Complete: false, Truncated: false, Total: 1},
		{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "tb", Label: "tb"}, MemberCanonicalIDs: []string{"b1", "b2"}, Complete: true, Truncated: false, Total: 2},
	}
	// Budget forces a trim (distinct members = 3 > 2), but decision D2
	// (member-first) keeps at least one member per group -- ta's single
	// member is not droppable without dropping ta entirely, so ta keeps
	// ALL of its own members while tb is thinned.
	kept, groups, narrowed, _ := NarrowGroupedCohort(cohort, 2)
	if !narrowed {
		t.Fatalf("narrowed = false, want true: kept=%v groups=%#v", kept, groups)
	}
	var ta, tb *contractsv1.ContextFabricCohortGroup
	for i := range groups {
		switch groups[i].Subject.CanonicalID {
		case "ta":
			ta = &groups[i]
		case "tb":
			tb = &groups[i]
		}
	}
	if ta == nil || tb == nil {
		t.Fatalf("expected both groups to survive narrowing (D2): %#v", groups)
	}
	if len(ta.MemberCanonicalIDs) != 1 {
		t.Fatalf("ta kept %d members, want its only member to survive (D2)", len(ta.MemberCanonicalIDs))
	}
	// DEFECT-SHAPE ASSERTION: ta's own narrowing left it un-truncated
	// (it kept everything it had), but its INCOMING Complete was already
	// false. The AND must keep it false, not treat "narrowing didn't touch
	// me" as "I am complete".
	if ta.Complete || ta.Truncated {
		t.Fatalf("ta.Complete=%v Truncated=%v after narrowing, want Complete=false Truncated=false -- it entered narrowing already discovery-incomplete and narrowing did not trim it", ta.Complete, ta.Truncated)
	}
	for _, g := range groups {
		if err := g.Validate(); err != nil {
			t.Fatalf("group %q Validate() = %v", g.Subject.CanonicalID, err)
		}
	}
}
