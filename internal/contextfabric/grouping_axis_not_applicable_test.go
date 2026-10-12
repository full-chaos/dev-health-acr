package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func projectScopedFact(memberID, projectID, label string) CanonicalFact {
	str := func(v string) FactValue { s := v; return FactValue{String: &s} }
	return CanonicalFact{
		Kind:    FactHealth,
		Subject: SubjectRef{Kind: SubjectPullRequest, CanonicalID: memberID, Label: memberID},
		Fields: map[string]FactValue{
			"risk_breakdown": {Rows: []FactValueRow{{Fields: map[string]FactValue{
				"scope": str("project"), "scope_id": str(projectID), "scope_name": str(label),
			}}}},
		},
		SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
}

func prTeamFact(memberID, teamID, label string) CanonicalFact {
	fact := projectScopedFact(memberID, teamID, label)
	row := fact.Fields["risk_breakdown"].Rows[0]
	row.Fields["scope"] = FactValue{String: strPtr("team")}
	return fact
}

func prCohort(ids ...string) *Cohort {
	cohort := &Cohort{Kind: SubjectPullRequest, Complete: true}
	for _, id := range ids {
		cohort.Members = append(cohort.Members, CohortMember{Subject: SubjectRef{Kind: SubjectPullRequest, CanonicalID: id, Label: id}})
	}
	return cohort
}

func TestAProjectQuestionOverTeamOnlyRowsRefusesAsAKindMismatch(t *testing.T) {
	t.Parallel()
	groups, _, outcome := BuildCohortGroups(AnswerPlan{GroupKind: SubjectProject}, prCohort("pull_request:a"),
		[]CanonicalFact{prTeamFact("pull_request:a", "team_1", "Platform")})
	if len(groups) != 0 || outcome.Refusal != CohortGroupingRefusalGroupKindSourceMismatch || outcome.PlannedKind != SubjectProject || outcome.SourceKind != SubjectTeam {
		t.Fatalf("groups=%d outcome=%+v", len(groups), outcome)
	}
}

func TestARefusedGroupAxisIsStatedNotApplicableWithItsCause(t *testing.T) {
	t.Parallel()
	published := []contractsv1.ContextFabricPlanRequirement{{
		Requirement: "state/group/project", Obligation: "state", Role: "group", Subject: contractsv1.ContextFabricSubjectKind(SubjectProject), Kind: string(ObligationKindRead),
	}}
	for _, tc := range []struct {
		name    string
		outcome CohortGroupingOutcome
		want    bool
	}{
		{"project mismatch", CohortGroupingOutcome{Refusal: CohortGroupingRefusalGroupKindSourceMismatch, PlannedKind: SubjectProject, SourceKind: SubjectTeam}, true},
		{"project no member placed", CohortGroupingOutcome{Refusal: CohortGroupingRefusalNoMemberPlaced, PlannedKind: SubjectProject}, true},
		{"no refusal", CohortGroupingOutcome{}, false},
		{"other kind refused", CohortGroupingOutcome{Refusal: CohortGroupingRefusalGroupKindSourceMismatch, PlannedKind: SubjectRepository, SourceKind: SubjectTeam}, false},
	} {
		rows := appendGroupAxisNotApplicableRows(nil, published, tc.outcome)
		if (len(rows) == 1) != tc.want {
			t.Fatalf("%s: rows = %+v", tc.name, rows)
		}
		if tc.want {
			row := rows[0]
			if row.Outcome != contractsv1.ContextFabricRequirementNotApplicable || row.CauseCoverage != contractsv1.ContextFabricCoverageDetailFactNoDeclaringProducer || !row.CauseObserved || row.Stage != contractsv1.ContextFabricOutcomeStageAssembledResult {
				t.Fatalf("%s: row = %+v", tc.name, row)
			}
			if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		}
	}
	// the team gap keeps its unattempted account
	if rows := appendGroupAxisNotApplicableRows(nil, []contractsv1.ContextFabricPlanRequirement{{
		Requirement: "state/group/team", Obligation: "state", Role: "group", Subject: contractsv1.ContextFabricSubjectKind(SubjectTeam), Kind: string(ObligationKindRead),
	}}, CohortGroupingOutcome{Refusal: CohortGroupingRefusalNoMemberPlaced, PlannedKind: SubjectTeam}); len(rows) != 0 {
		t.Fatalf("team no-placement rows = %+v", rows)
	}
}
