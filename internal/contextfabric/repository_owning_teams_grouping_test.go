package contextfabric

import (
	"os"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func repoMember(id string) CohortMember {
	return CohortMember{Subject: SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:" + id, Label: id}}
}

// owningTeamsFact is the identity fact shape the producer emits: one
// owning_teams breakdown row per owning team.
func owningTeamsFact(repoID string, teams ...[2]string) CanonicalFact {
	str := func(v string) FactValue { s := v; return FactValue{String: &s} }
	rows := make([]FactValueRow, 0, len(teams))
	for _, team := range teams {
		rows = append(rows, FactValueRow{Fields: map[string]FactValue{"team_id": str(team[0]), "team_name": str(team[1])}})
	}
	return CanonicalFact{
		Kind:    FactIdentity,
		Subject: SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:" + repoID, Label: repoID},
		Fields: map[string]FactValue{"owning_teams": TableFactValue(FactTable{
			Shape: FactTableBreakdown, Key: []string{"team_id"}, Observations: []string{"team_name"}, Rows: rows,
		})},
		SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
}

func TestRepositoryCohortGroupsUnderEveryOwningTeam(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectRepository, Complete: true, Members: []CohortMember{repoMember("r1"), repoMember("r2"), repoMember("r3")}}
	facts := []CanonicalFact{
		owningTeamsFact("r1", [2]string{"a", "Alpha"}),
		owningTeamsFact("r2", [2]string{"a", "Alpha"}, [2]string{"b", "Beta"}),
	}
	groups, ungrouped, outcome := BuildCohortGroups(AnswerPlan{GroupKind: SubjectTeam}, cohort, facts)
	if outcome.Refusal != CohortGroupingRefusalNone {
		t.Fatalf("refusal = %q, want none", outcome.Refusal)
	}
	if len(groups) != 2 || groups[0].Subject.CanonicalID != "team:a" || groups[1].Subject.CanonicalID != "team:b" {
		t.Fatalf("groups = %+v, want team:a and team:b", groups)
	}
	if ungrouped != 1 {
		t.Fatalf("ungrouped = %d, want 1 (r3 has no owning team row)", ungrouped)
	}
	coverage := CohortGroupCoverageOf(cohort, groups, facts)
	if coverage.Overlap != 1 || coverage.Ungrouped != 1 || coverage.Cut != 0 || coverage.MemberKind != SubjectRepository || coverage.GroupKind != SubjectTeam {
		t.Fatalf("coverage = %+v, want overlap 1 (r2), ungrouped 1 (r3), cut 0", coverage)
	}
	result := InvestigationResult{}
	applyGroupCoverageDisclosure(&result, coverage)
	if len(result.Limitations) != 2 {
		t.Fatalf("limitations = %q, want the ungrouped and the overlap sentence", result.Limitations)
	}
	for _, sentence := range result.Limitations {
		if !contractsv1.IsContextFabricServiceAuthoredLimitation(sentence) {
			t.Fatalf("%q is not a recognised service-authored limitation", sentence)
		}
	}
	if result.Limitations[0] != "1 repository in this answer could not be placed under any team and appear in no group." ||
		result.Limitations[1] != "1 repository in this answer appear in more than one team, so group totals overlap." || result.Coverage.Partial {
		t.Fatalf("sentences = %q partial=%v", result.Limitations, result.Coverage.Partial)
	}
}

func TestOwnersTheFactsCutShortAreDisclosedNotCalledUnowned(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectRepository, Complete: true, Members: []CohortMember{repoMember("r1")}}
	fact := owningTeamsFact("r1", [2]string{"a", "Alpha"})
	cut := int64(3)
	fact.Fields["owning_teams_omitted_count"] = FactValue{Integer: &cut}
	groups, _, _ := BuildCohortGroups(AnswerPlan{GroupKind: SubjectTeam}, cohort, []CanonicalFact{fact})
	coverage := CohortGroupCoverageOf(cohort, groups, []CanonicalFact{fact})
	if coverage.Cut != 1 || coverage.Ungrouped != 0 {
		t.Fatalf("coverage = %+v, want cut 1 and ungrouped 0: the repository is grouped under the owners listed", coverage)
	}
	result := InvestigationResult{}
	applyGroupCoverageDisclosure(&result, coverage)
	if len(result.Limitations) != 1 || !contractsv1.IsContextFabricGroupingCutLimitation(result.Limitations[0]) {
		t.Fatalf("limitations = %q, want one cut sentence", result.Limitations)
	}
}

func TestRepositoryCohortWithNoOwnerRowsIsRefusedNotInvented(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectRepository, Complete: true, Members: []CohortMember{repoMember("r1")}}
	groups, ungrouped, outcome := BuildCohortGroups(AnswerPlan{GroupKind: SubjectTeam}, cohort, nil)
	if len(groups) != 0 || ungrouped != 1 || outcome.Refusal != CohortGroupingRefusalNoMemberPlaced {
		t.Fatalf("groups=%d ungrouped=%d refusal=%q, want 0/1/no_member_placed", len(groups), ungrouped, outcome.Refusal)
	}
	if got := CohortGroupCoverageOf(cohort, groups, nil); got != (CohortGroupCoverage{}) {
		t.Fatalf("coverage = %+v with no groups, want zero (the refusal already discloses)", got)
	}
}

func TestSingleOwnerCompleteGroupingDisclosesNothing(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectRepository, Complete: true, Members: []CohortMember{repoMember("r1")}}
	facts := []CanonicalFact{owningTeamsFact("r1", [2]string{"a", "Alpha"})}
	groups, _, _ := BuildCohortGroups(AnswerPlan{GroupKind: SubjectTeam}, cohort, facts)
	result := InvestigationResult{}
	applyGroupCoverageDisclosure(&result, CohortGroupCoverageOf(cohort, groups, facts))
	if len(result.Limitations) != 0 {
		t.Fatalf("disclosed %q with nothing to disclose", result.Limitations)
	}
}

// Members the answer dropped (narrowed away) never count: an unseen two-owner
// or unplaced repository is not disclosed.
func TestCoverageIsCountedOverTheServedSetOnly(t *testing.T) {
	t.Parallel()
	served := &Cohort{Kind: SubjectRepository, Complete: true, Members: []CohortMember{repoMember("r1")}}
	groups := []contractsv1.ContextFabricCohortGroup{
		{Subject: contractsv1.ContextFabricSubjectRef{Kind: SubjectTeam, CanonicalID: "team:a"}, MemberCanonicalIDs: []string{"repository:r1", "repository:dropped"}},
		{Subject: contractsv1.ContextFabricSubjectRef{Kind: SubjectTeam, CanonicalID: "team:b"}, MemberCanonicalIDs: []string{"repository:dropped"}},
	}
	cutFact := owningTeamsFact("dropped", [2]string{"a", "Alpha"})
	n := int64(2)
	cutFact.Fields["owning_teams_omitted_count"] = FactValue{Integer: &n}
	if got := CohortGroupCoverageOf(served, groups, []CanonicalFact{cutFact}); got.Overlap != 0 || got.Ungrouped != 0 || got.Cut != 0 {
		t.Fatalf("coverage = %+v, want zero: the two-owner and cut repository is not in the served cohort", got)
	}
}

func TestAssemblyCountsCoverageOverTheNarrowedCohort(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("chaos4636_synthesis_assembly.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "CohortGroupCoverageOf(cohort, cohort.Groups, params.Facts.Facts)") {
		t.Fatal("assembly must count coverage over the served cohort (params.Graph.Cohort), after narrowing")
	}
}

func TestGroupingCoverageSentencesParseAndRejectLookalikes(t *testing.T) {
	t.Parallel()
	repo, team := contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectTeam
	for name, tc := range map[string]struct {
		good string
		is   func(string) bool
	}{
		"overlap":   {contractsv1.ContextFabricGroupingOverlapLimitation(repo, team, 3), contractsv1.IsContextFabricGroupingOverlapLimitation},
		"ungrouped": {contractsv1.ContextFabricGroupingUngroupedLimitation(repo, team, 3), contractsv1.IsContextFabricGroupingUngroupedLimitation},
		"cut":       {contractsv1.ContextFabricGroupingCutLimitation(repo, team, 3), contractsv1.IsContextFabricGroupingCutLimitation},
	} {
		if !tc.is(tc.good) || !contractsv1.IsContextFabricServiceAuthoredLimitation(tc.good) {
			t.Fatalf("%s: composer output %q not recognised", name, tc.good)
		}
		for _, bad := range []string{
			strings.Replace(tc.good, "3 ", "0 ", 1),
			strings.Replace(tc.good, "3 ", "03 ", 1),
			strings.Replace(tc.good, "repositories", "widgets", 1),
			"x " + tc.good,
			tc.good + " extra",
		} {
			if tc.is(bad) {
				t.Fatalf("%s: lookalike %q recognised", name, bad)
			}
		}
	}
	if contractsv1.IsContextFabricGroupingOverlapLimitation(contractsv1.ContextFabricGroupingUngroupedLimitation(repo, team, 3)) {
		t.Fatal("the three families must stay disjoint")
	}
}
