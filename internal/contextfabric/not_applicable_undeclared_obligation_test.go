package contextfabric_test

import (
	"testing"

	contextfabric "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func seedHealthRow(t *testing.T, frame contextfabric.QuestionFrame, subject contextfabric.SubjectKind) contextfabric.RequirementOutcomeRow {
	t.Helper()
	capabilities := liveCapabilityList(t)
	rows := contextfabric.DeriveRequirements(frame, contextfabric.GenerateObligationSeed(capabilities), capabilities)
	for _, row := range contextfabric.SeedRequirementOutcomes(rows) {
		if row.Obligation == string(contextfabric.ObligationHealth) && row.Requirement == "health/member/"+string(subject) {
			return row
		}
	}
	t.Fatalf("no health/member/%s seed row; the test drove nothing", subject)
	return contextfabric.RequirementOutcomeRow{}
}

func TestUndeclaredHealthObligationSeedsNotApplicableForCohortMemberKinds(t *testing.T) {
	for name, tc := range map[string]struct {
		frame   contextfabric.QuestionFrame
		subject contextfabric.SubjectKind
	}{
		"pull requests grouped by project": {traceBuildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalAssessState}, traceGrouped(contextfabric.SubjectProject, contextfabric.SubjectPullRequest), contextfabric.TemporalIntentCurrent, nil, nil), contextfabric.SubjectPullRequest},
		"discovered incidents":             {traceBuildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalAssessState}, traceDiscovered(contextfabric.SubjectIncident), contextfabric.TemporalIntentCurrent, nil, nil), contextfabric.SubjectIncident},
	} {
		t.Run(name, func(t *testing.T) {
			row := seedHealthRow(t, tc.frame, tc.subject)
			if row.Outcome != v1.ContextFabricRequirementNotApplicable || row.Impact != v1.ContextFabricAnswerImpactNone || row.CauseCoverage != v1.ContextFabricCoverageDetailFactNoDeclaringProducer || !row.CauseObserved {
				t.Fatalf("health seed row = %+v, want lossless not_applicable naming the no-declaring-producer cause as observed", row)
			}
			if err := v1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
				t.Fatalf("row is not a legal outcome row: %v", err)
			}
		})
	}
}

// The control: where a producer declares the obligation, nothing changes; and
// every other unserved reason keeps its unavailable account and degrades.
func TestOnlyANoDeclaringProducerReadObligationIsNotApplicable(t *testing.T) {
	coordinate := contextfabric.RequirementCoordinate{Obligation: contextfabric.ObligationHealth, Role: contextfabric.SubjectRoleMember, Subject: contextfabric.SubjectTeam}
	for _, tc := range []struct {
		name       string
		obligation contextfabric.AnswerObligation
		reason     contextfabric.RequirementUnavailableReason
		want       v1.ContextFabricPlanRequirementOutcome
	}{
		{"declared and served", contextfabric.ObligationHealth, "", v1.ContextFabricRequirementSatisfied},
		{"no declaring producer", contextfabric.ObligationHealth, contextfabric.RequirementReasonNoDeclaringProducer, v1.ContextFabricRequirementNotApplicable},
		{"subject kind unsupported", contextfabric.ObligationHealth, contextfabric.RequirementReasonSubjectKindUnsupported, v1.ContextFabricRequirementUnavailable},
		{"table shape undeclared", contextfabric.ObligationHealth, contextfabric.RequirementReasonTableShapeUndeclared, v1.ContextFabricRequirementUnavailable},
		{"computed population absent", contextfabric.ObligationRanking, contextfabric.RequirementReasonComputedPopulationAbsent, v1.ContextFabricRequirementUnavailable},
		{"computed obligation with the token", contextfabric.ObligationRanking, contextfabric.RequirementReasonNoDeclaringProducer, v1.ContextFabricRequirementUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := coordinate
			c.Obligation = tc.obligation
			rows := contextfabric.SeedRequirementOutcomes([]contextfabric.DerivedRequirement{{RequirementCoordinate: c, Unavailable: tc.reason}})
			if len(rows) != 1 || rows[0].Outcome != tc.want {
				t.Fatalf("rows = %+v, want outcome %q", rows, tc.want)
			}
			degraded := v1.DeriveContextFabricAnswerCompletenessState(rows) == v1.ContextFabricAnswerCompletenessDegraded
			if degraded != (tc.want == v1.ContextFabricRequirementUnavailable) {
				t.Fatalf("degraded = %v for outcome %q", degraded, tc.want)
			}
		})
	}
}
