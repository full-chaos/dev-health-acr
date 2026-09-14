package contextfabric_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Query returns typed database rows; the actual registered provider, registry,
// derivation and finalizer must produce the fact and its matching outcome.
type memberStateClient struct {
	values []any
	reads  int
}

func (c *memberStateClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.reads++
	return &memberStateScanner{values: c.values}, nil
}

type memberStateScanner struct {
	values []any
	done   bool
}

func (s *memberStateScanner) Next() bool { return !s.done }
func (s *memberStateScanner) Scan(dest ...any) error {
	if len(dest) != len(s.values) {
		return fmt.Errorf("scan fields %d, row fields %d", len(dest), len(s.values))
	}
	for i, d := range dest {
		target, value := reflect.ValueOf(d).Elem(), reflect.ValueOf(s.values[i])
		if !value.Type().AssignableTo(target.Type()) {
			return fmt.Errorf("column %d: cannot scan %s into %s", i, value.Type(), target.Type())
		}
		target.Set(value)
	}
	s.done = true
	return nil
}
func (*memberStateScanner) Err() error   { return nil }
func (*memberStateScanner) Close() error { return nil }

func TestStateMemberRequirementKeepsHealthIndependent(t *testing.T) {
	for _, tc := range []struct {
		kind      cf.SubjectKind
		fact      cf.FactKind
		id, field string
		values    []any
	}{
		{cf.SubjectPullRequest, cf.FactPullRequests, "pull_request:repo-1:1042", "state", []any{"repo-1", uint32(1042), "open"}},
		{cf.SubjectIncident, cf.FactIncidents, "incident:incident-1", "status", []any{"incident-1", "open", "high"}},
	} {
		for _, goal := range []cf.InvestigationGoal{cf.GoalAssessState, cf.GoalExplainDrivers, cf.GoalRankOrSurvey} {
			t.Run(string(tc.kind)+"/"+string(goal), func(t *testing.T) {
				client := &memberStateClient{values: tc.values}
				registry, err := cf.NewFactCapabilityRegistry(devhealthfacts.NewProviders(client), cf.FactRegistryOptions{})
				if err != nil {
					t.Fatal(err)
				}
				frame := cf.DeriveFrameObligations(cf.QuestionFrame{Goals: []cf.InvestigationGoal{goal}, SubjectExpression: cf.SubjectExpression{Kind: cf.SubjectExpressionGroupedMembers, Grouped: &cf.GroupedSetExpression{MemberKind: tc.kind, GroupKind: cf.SubjectProject}}, Temporal: cf.TemporalIntentCurrent, Version: cf.QuestionFrameVersion}, nil)
				subject := cf.SubjectRef{Kind: tc.kind, CanonicalID: tc.id, Label: tc.id}
				facts, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, cf.CanonicalFactRequest{Subjects: []cf.SubjectRef{subject}, Question: cf.InterpretedQuestion{TimeContext: cf.TimeContext{Axis: cf.TemporalCurrent}}, Requirements: []cf.FactRequirement{{Kind: tc.fact}}})
				if err != nil {
					t.Fatal(err)
				}
				if client.reads != 1 || len(facts.Facts) != 1 {
					t.Fatalf("reads=%d facts=%+v", client.reads, facts.Facts)
				}
				value := facts.Facts[0].Fields[tc.field].String
				if value == nil || *value != "open" {
					t.Fatalf("canonical %s=%v", tc.field, value)
				}
				result := cf.FinalizeStateRankingForTest(frame, registry, &cf.Cohort{Kind: tc.kind, Complete: true, Members: []cf.CohortMember{{Subject: subject}}}, facts)
				stateRows, healthRows := 0, 0
				for _, row := range result.Completeness.Outcomes {
					if row.Requirement == "state/member/"+string(tc.kind) && row.Stage == v1.ContextFabricOutcomeStageAssembledResult {
						stateRows++
						if row.Outcome != v1.ContextFabricRequirementNarrowed || row.Impact != v1.ContextFabricAnswerImpactDepth || row.Served != 1 || row.Declared != 2 {
							t.Errorf("served state=%+v", row)
						}
					}
					if row.Requirement == "health/member/"+string(tc.kind) {
						healthRows++
						if row.Outcome != v1.ContextFabricRequirementUnavailable || row.Stage != v1.ContextFabricOutcomeStagePlanning || row.CauseCoverage != v1.ContextFabricCoverageDetailFactUnconfigured {
							t.Errorf("required health=%+v", row)
						}
					}
				}
				if result.Completeness.State != v1.ContextFabricAnswerCompletenessDegraded {
					t.Fatalf("unsupported required health must retain degraded completeness: %+v", result.Completeness)
				}
				if stateRows != 1 || healthRows != 1 {
					t.Fatalf("state rows=%d health rows=%d outcomes=%+v", stateRows, healthRows, result.Completeness.Outcomes)
				}
			})
		}
	}
}
