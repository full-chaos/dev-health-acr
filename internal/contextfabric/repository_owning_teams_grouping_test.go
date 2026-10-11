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
	overlap := CohortGroupOverlapOf(cohort, groups)
	if overlap.Count != 1 || overlap.MemberKind != SubjectRepository || overlap.GroupKind != SubjectTeam {
		t.Fatalf("overlap = %+v, want 1 repository under more than one team (only r2 has two owners)", overlap)
	}
	result := InvestigationResult{}
	applyGroupOverlapDisclosure(&result, overlap)
	if len(result.Limitations) != 1 || !contractsv1.IsContextFabricServiceAuthoredLimitation(result.Limitations[0]) {
		t.Fatalf("limitations = %q, want one recognised service-authored overlap disclosure", result.Limitations)
	}
	if !strings.HasPrefix(result.Limitations[0], "1 repository in this answer appear") || result.Coverage.Partial {
		t.Fatalf("overlap sentence = %q partial=%v", result.Limitations[0], result.Coverage.Partial)
	}
}

func TestRepositoryCohortWithNoOwnerRowsIsRefusedNotInvented(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectRepository, Complete: true, Members: []CohortMember{repoMember("r1")}}
	groups, ungrouped, outcome := BuildCohortGroups(AnswerPlan{GroupKind: SubjectTeam}, cohort, nil)
	if len(groups) != 0 || ungrouped != 1 || outcome.Refusal != CohortGroupingRefusalNoMemberPlaced {
		t.Fatalf("groups=%d ungrouped=%d refusal=%q, want 0/1/no_member_placed", len(groups), ungrouped, outcome.Refusal)
	}
}

func TestSingleOwnerRepositoriesDiscloseNoOverlap(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectRepository, Complete: true, Members: []CohortMember{repoMember("r1")}}
	groups, _, _ := BuildCohortGroups(AnswerPlan{GroupKind: SubjectTeam}, cohort, []CanonicalFact{owningTeamsFact("r1", [2]string{"a", "Alpha"})})
	result := InvestigationResult{}
	applyGroupOverlapDisclosure(&result, CohortGroupOverlapOf(cohort, groups))
	if len(result.Limitations) != 0 {
		t.Fatalf("disclosed %q with no overlap", result.Limitations)
	}
}

func TestOverlapLimitationParseRejectsLookalikes(t *testing.T) {
	t.Parallel()
	good := contractsv1.ContextFabricGroupingOverlapLimitation(contractsv1.ContextFabricSubjectRepository, contractsv1.ContextFabricSubjectTeam, 3)
	if !contractsv1.IsContextFabricGroupingOverlapLimitation(good) {
		t.Fatalf("composer output %q not recognised", good)
	}
	for _, bad := range []string{
		strings.Replace(good, "3 ", "0 ", 1),
		strings.Replace(good, "3 ", "03 ", 1),
		strings.Replace(good, "repositories", "widgets", 1),
		"x " + good,
		good + " extra",
	} {
		if contractsv1.IsContextFabricGroupingOverlapLimitation(bad) {
			t.Fatalf("lookalike %q recognised", bad)
		}
	}
}

// A repository the answer dropped (narrowed away) that has two owners must not
// raise the overlap: the count is over the served set only.
func TestOverlapIsCountedOverTheServedSetOnly(t *testing.T) {
	t.Parallel()
	served := &Cohort{Kind: SubjectRepository, Complete: true, Members: []CohortMember{repoMember("r1")}}
	groups := []contractsv1.ContextFabricCohortGroup{
		{Subject: contractsv1.ContextFabricSubjectRef{Kind: SubjectTeam, CanonicalID: "team:a"}, MemberCanonicalIDs: []string{"repository:r1", "repository:dropped"}},
		{Subject: contractsv1.ContextFabricSubjectRef{Kind: SubjectTeam, CanonicalID: "team:b"}, MemberCanonicalIDs: []string{"repository:dropped"}},
	}
	if got := CohortGroupOverlapOf(served, groups); got.Count != 0 {
		t.Fatalf("overlap = %d, want 0: the two-owner repository is not in the served cohort", got.Count)
	}
}

func TestAssemblyCountsOverlapOverTheNarrowedCohort(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("chaos4636_synthesis_assembly.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "CohortGroupOverlapOf(cohort, cohort.Groups)") {
		t.Fatal("assembly must count overlap over the served cohort (params.Graph.Cohort), after narrowing")
	}
}
