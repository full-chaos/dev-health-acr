package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// groupFact is one fact rooted on a GROUP identity -- what the second read
// brings back, as opposed to the member facts the first read brings.
func groupFact(rawTeamKey string) CanonicalFact {
	return CanonicalFact{
		Kind:        FactHealth,
		Subject:     SubjectRef{Kind: SubjectTeam, CanonicalID: TeamCanonicalID(rawTeamKey), Label: rawTeamKey},
		Fields:      map[string]FactValue{"severity": StringFactValue("elevated")},
		SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
}

// TestFactsForAGroupTheAnswerNoLongerCarriesAreDropped is CHAOS-5285
// test-table row 14.
//
// Retention was written when every fact in the bundle was rooted on a cohort
// MEMBER: it drops facts whose subject is a removed member, and a group fact's
// subject is a team, which is never in that list. So a group dropped from the
// answer keeps its evidence, and synthesis is handed facts about a population
// the served document does not contain.
//
// That is not a cosmetic leak. The engine's own retention comment states the
// consequence for the member case -- "a claim minted about a subject the
// answer no longer contains is an UNGROUNDED claim, and the evidence-closure
// validator would reject the whole result" -- and a group subject reaches
// synthesis by the same door. A narrowed grouped answer would fail closure for
// a reason nothing in the trace explains.
//
// RED before the group-aware retention exists: the dropped group's fact
// survives.
func TestFactsForAGroupTheAnswerNoLongerCarriesAreDropped(t *testing.T) {
	t.Parallel()

	cohort := &Cohort{
		Kind: SubjectProject, Rationale: "retention pin", Complete: true,
		Members: []CohortMember{
			{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
		},
		Groups: []contractsv1.ContextFabricCohortGroup{
			{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: TeamCanonicalID("team_security"), Label: "Security"},
				MemberCanonicalIDs: []string{"project_a"}, Total: 1, Complete: true},
		},
	}
	// team_platform is GONE from the answer: its member was narrowed away and
	// the group went with it.
	removed := []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}
	facts := []CanonicalFact{
		teamScopedFact("project_a", "team_security", "Security"),
		teamScopedFact("project_b", "team_platform", "Platform"),
		groupFact("team_security"),
		groupFact("team_platform"),
	}

	retained := RetainFactsForCohort(facts, cohort, removed, nil)
	kept := map[string]bool{}
	for _, fact := range retained {
		kept[fact.Subject.CanonicalID] = true
		t.Logf("retained %s/%s", fact.Subject.Kind, fact.Subject.CanonicalID)
	}

	// The surviving group keeps its evidence.
	if !kept[TeamCanonicalID("team_security")] {
		t.Errorf("the surviving group %q lost its own evidence -- retention must not cost a group that is still in the answer its second read",
			TeamCanonicalID("team_security"))
	}
	// The removed member keeps nothing, which is the behaviour that already
	// worked and must keep working.
	if kept["project_b"] {
		t.Errorf("the removed member project_b kept its facts")
	}
	// AND THE DROPPED GROUP KEEPS NOTHING.
	if kept[TeamCanonicalID("team_platform")] {
		t.Errorf("the group %q is not in the answer and its second-read evidence survived -- synthesis is being handed facts about a population the served document does not contain, and a claim minted from them fails evidence closure for a reason nothing in the trace explains",
			TeamCanonicalID("team_platform"))
	}
}

// TestRetentionLeavesAnUnnarrowedGroupedBundleAlone is the DISCRIMINATING
// CONTROL, and it must pass before and after.
//
// Without it, "drop the facts of groups not in the answer" is satisfied by an
// implementation that drops every group fact, which would delete the entire
// second read and quietly restore the defect this ticket exists to close --
// while the pin above went green.
func TestRetentionLeavesAnUnnarrowedGroupedBundleAlone(t *testing.T) {
	t.Parallel()

	cohort := &Cohort{
		Kind: SubjectProject, Rationale: "retention control", Complete: true,
		Members: []CohortMember{
			{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
			{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
		},
		Groups: []contractsv1.ContextFabricCohortGroup{
			{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: TeamCanonicalID("team_security"), Label: "Security"},
				MemberCanonicalIDs: []string{"project_a"}, Total: 1, Complete: true},
			{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: TeamCanonicalID("team_platform"), Label: "Platform"},
				MemberCanonicalIDs: []string{"project_b"}, Total: 1, Complete: true},
		},
	}
	facts := []CanonicalFact{
		teamScopedFact("project_a", "team_security", "Security"),
		teamScopedFact("project_b", "team_platform", "Platform"),
		groupFact("team_security"),
		groupFact("team_platform"),
	}

	// One member removed, but BOTH groups still in the answer.
	removed := []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_c", Label: "project_c"}, Rank: 3, InclusionReasons: []string{"matched"}},
	}
	retained := RetainFactsForCohort(facts, cohort, removed, nil)
	kept := map[string]bool{}
	for _, fact := range retained {
		kept[fact.Subject.CanonicalID] = true
	}
	t.Logf("control retained %d of %d facts", len(retained), len(facts))
	for _, group := range cohort.Groups {
		if !kept[group.Subject.CanonicalID] {
			t.Fatalf("CONTROL BROKEN: group %q is still in the answer and lost its evidence -- an implementation that drops every group fact would satisfy the pin above while deleting the whole second read",
				group.Subject.CanonicalID)
		}
	}
}

// TestRetentionSaysWhenTheGroupRuleDidNotRun closes the nil/empty-cohort cell
// of the retention rule's input domain.
//
// The group half of the rule cannot run without a group list: there is nothing
// to admit against, and dropping every non-member fact would delete the
// subject-resolution evidence of every FLAT answer -- a worse failure than the
// one being prevented. So those two shapes keep the pre-group behaviour, and
// that is correct.
//
// What was NOT correct is that they kept it SILENTLY. A third call site
// reaching this function with group facts and no group list would restore the
// row-14 defect exactly, with nothing anywhere saying the rule had been
// skipped. The decision now reports whether the rule ran, so the restoration
// is visible on the trace instead of invisible in the diff.
func TestRetentionSaysWhenTheGroupRuleDidNotRun(t *testing.T) {
	t.Parallel()

	member := SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "a"}
	removed := []CohortMember{{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "b"}, Rank: 2, InclusionReasons: []string{"m"}}}
	facts := []CanonicalFact{
		{Kind: FactMetrics, Subject: member, Fields: map[string]FactValue{}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1"},
		groupFact("team_a"),
	}
	flat := &Cohort{Kind: SubjectProject, Rationale: "r", Complete: true,
		Members: []CohortMember{{Subject: member, Rank: 1, InclusionReasons: []string{"m"}}}}
	grouped := &Cohort{Kind: SubjectProject, Rationale: "r", Complete: true,
		Members: flat.Members,
		Groups: []contractsv1.ContextFabricCohortGroup{
			{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: TeamCanonicalID("team_a"), Label: "A"}, MemberCanonicalIDs: []string{"project_a"}, Total: 1, Complete: true},
		}}

	for _, testCase := range []struct {
		name       string
		cohort     *Cohort
		wantRuleOn bool
	}{
		{"nil cohort", nil, false},
		{"cohort with no groups", flat, false},
		{"grouped cohort", grouped, true},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			retained, decision := RetainFactsForCohortWithDecision(facts, testCase.cohort, removed, nil)
			t.Logf("%s -> kept=%d group_rule_applied=%v retained_groups=%d dropped_groups=%d",
				testCase.name, len(retained), decision.GroupRuleApplied, decision.RetainedGroups, decision.DroppedGroups)
			if decision.GroupRuleApplied != testCase.wantRuleOn {
				t.Errorf("group_rule_applied = %v, want %v -- a caller that reaches retention holding group facts and no group list gets the pre-group behaviour, and the only thing standing between that and a silent restoration of the defect is this flag",
					decision.GroupRuleApplied, testCase.wantRuleOn)
			}
			if !testCase.wantRuleOn && decision.DroppedGroups != 0 {
				t.Errorf("dropped_groups = %d with the rule not applied -- the counter must not report work the rule did not do", decision.DroppedGroups)
			}
		})
	}
}
