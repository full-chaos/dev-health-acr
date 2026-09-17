package contextfabric_test

import (
	"context"
	"fmt"
	"testing"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	v1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// emptyRowsClient answers every query with zero rows -- the honest "the
// GROUP BY produced no row for this project" shape, which
// memberStateClient/memberStateScanner cannot express (their Next()
// always yields exactly one row).
type emptyRowsClient struct{ reads int }

func (c *emptyRowsClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.reads++
	return &emptyRowsScanner{}, nil
}

type emptyRowsScanner struct{}

func (*emptyRowsScanner) Next() bool        { return false }
func (*emptyRowsScanner) Scan(...any) error { return nil }
func (*emptyRowsScanner) Err() error        { return nil }
func (*emptyRowsScanner) Close() error      { return nil }

// completionProjectSubject mints a "project.v2:<provider>:<id>" subject,
// mirroring devhealthfacts_test's projectSubject helper (unexported to
// that package, so restated here rather than imported).
func completionProjectSubject(provider, id string) cf.SubjectRef {
	canonicalID, omitted, err := identity.Derive(identity.KindProject, []string{provider, id}, nil)
	if err != nil || omitted {
		panic(fmt.Sprintf("completionProjectSubject(%q, %q): identity.Derive failed: omitted=%v err=%v", provider, id, omitted, err))
	}
	return cf.SubjectRef{Kind: cf.SubjectProject, CanonicalID: canonicalID, Label: id}
}

// completionFrame builds a discovered/grouped-member frame requiring the
// `completion` obligation the same way a real request would: via the
// Dimensions axis (frame_obligations.go's dimensionObligations table --
// HealthDimensionExecutionCompletion -> ObligationCompletion), not the
// goal axis. GoalAssessState is along for the ride only to keep Goals
// non-empty (§13.2.3 I15); it contributes `state`, never `completion`.
func completionFrame() cf.QuestionFrame {
	return cf.DeriveFrameObligations(cf.QuestionFrame{
		Goals:             []cf.InvestigationGoal{cf.GoalAssessState},
		Dimensions:        []cf.HealthDimension{cf.HealthDimensionExecutionCompletion},
		SubjectExpression: cf.SubjectExpression{Kind: cf.SubjectExpressionGroupedMembers, Grouped: &cf.GroupedSetExpression{MemberKind: cf.SubjectProject, GroupKind: cf.SubjectTeam}},
		Temporal:          cf.TemporalIntentCurrent,
		Version:           cf.QuestionFrameVersion,
	}, nil)
}

// TestCompletionMemberRequirementServedForProject drives the REAL
// ActualCompletionProvider (registry.ReadFacts, never a struct-literal
// CanonicalFact/outcome) with a project subject that DOES resolve
// completion data, and proves shared.go's SubjectProject declaration is
// actually wired end to end: a project's completion/member/project
// requirement reaches the assembled_result stage with real served data,
// never staying stuck at fact_no_declaring_producer.
func TestCompletionMemberRequirementServedForProject(t *testing.T) {
	subject := completionProjectSubject("linear", "proj-1")
	// One row of workItemProjectCompletionStatement's own SELECT shape:
	// (project key, work_item_count, cancelled_count, unknown_status_count,
	// completed_count).
	client := &memberStateClient{values: []any{"linear:proj-1", uint64(10), uint64(2), uint64(0), uint64(4)}}
	registry, err := cf.NewFactCapabilityRegistry(devhealthfacts.NewProviders(client), cf.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	frame := completionFrame()
	facts, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, cf.CanonicalFactRequest{
		Subjects:     []cf.SubjectRef{subject},
		Question:     cf.InterpretedQuestion{TimeContext: cf.TimeContext{Axis: cf.TemporalCurrent}},
		Requirements: []cf.FactRequirement{{Kind: cf.FactActualCompletion}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.reads != 1 || len(facts.Facts) != 1 {
		t.Fatalf("reads=%d facts=%+v -- the real producer must be driven exactly once and report exactly one fact", client.reads, facts.Facts)
	}
	// The served basis: the SAME field names Ask Dev part 2 will render.
	fact := facts.Facts[0]
	if got := fact.Fields["rollup_basis"].String; got == nil || *got != "project_work_item_completion" {
		t.Fatalf("rollup_basis = %v, want project_work_item_completion", got)
	}
	if got := fact.Fields["counted_work_items"].Integer; got == nil || *got != 8 {
		t.Fatalf("counted_work_items = %v, want 8 (10 - 2 cancelled)", got)
	}

	result := cf.FinalizeStateRankingForTest(frame, registry, &cf.Cohort{Kind: cf.SubjectProject, Complete: true, Members: []cf.CohortMember{{Subject: subject}}}, facts)
	var completionRows int
	for _, row := range result.Completeness.Outcomes {
		// Only the FINAL stage reflects whether the read actually served
		// data -- planning's own row is always "satisfied" once a producer
		// merely EXISTS (TestStateMemberRequirementKeepsHealthIndependent's
		// own pattern keys on this same stage for the same reason).
		if row.Requirement != "completion/member/project" || row.Stage != v1.ContextFabricOutcomeStageAssembledResult {
			continue
		}
		completionRows++
		// The planning-time gap this ticket closes: it must NEVER be
		// fact_no_declaring_producer again for this requirement, now that
		// shared.go declares SubjectProject: {ObligationCompletion}.
		if row.CauseCoverage == v1.ContextFabricCoverageDetailFactNoDeclaringProducer {
			t.Fatalf("outcome row = %+v, want CauseCoverage != fact_no_declaring_producer -- the registry declaration exists now, the planning-time gap must not recur", row)
		}
		if row.Outcome != v1.ContextFabricRequirementSatisfied || row.Served != 1 || row.Declared != 1 {
			t.Fatalf("outcome row = %+v, want Outcome=satisfied Served=1 Declared=1 -- the roll-up actually served data", row)
		}
	}
	if completionRows != 1 {
		t.Fatalf("completion/member/project assembled_result outcome rows = %d, want exactly 1: %+v", completionRows, result.Completeness.Outcomes)
	}
}

// TestCompletionMemberRequirementHonestlyUnavailableForEmptyProject proves
// the OTHER honesty invariant on the same real path: a project with
// nothing to count reports fact_provider_reported (the producer itself
// answered no_data), never fact_no_declaring_producer (a planning gap) and
// never a defaulted 0%/100%.
func TestCompletionMemberRequirementHonestlyUnavailableForEmptyProject(t *testing.T) {
	subject := completionProjectSubject("linear", "proj-empty")
	client := &emptyRowsClient{}
	registry, err := cf.NewFactCapabilityRegistry(devhealthfacts.NewProviders(client), cf.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	frame := completionFrame()
	facts, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, cf.CanonicalFactRequest{
		Subjects:     []cf.SubjectRef{subject},
		Question:     cf.InterpretedQuestion{TimeContext: cf.TimeContext{Axis: cf.TemporalCurrent}},
		Requirements: []cf.FactRequirement{{Kind: cf.FactActualCompletion}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Facts) != 0 {
		t.Fatalf("facts = %+v, want none -- an empty project must not carry a fabricated fact", facts.Facts)
	}

	result := cf.FinalizeStateRankingForTest(frame, registry, &cf.Cohort{Kind: cf.SubjectProject, Complete: true, Members: []cf.CohortMember{{Subject: subject}}}, facts)
	var completionRows int
	for _, row := range result.Completeness.Outcomes {
		if row.Requirement != "completion/member/project" || row.Stage != v1.ContextFabricOutcomeStageAssembledResult {
			continue
		}
		completionRows++
		if row.CauseCoverage != v1.ContextFabricCoverageDetailFactProviderReported {
			t.Fatalf("outcome row = %+v, want CauseCoverage = fact_provider_reported (the producer answered no_data honestly), not %v", row, row.CauseCoverage)
		}
		if row.CauseCoverage == v1.ContextFabricCoverageDetailFactNoDeclaringProducer {
			t.Fatalf("outcome row = %+v, want CauseCoverage != fact_no_declaring_producer -- this is a data gap (no work items), never a planning gap", row)
		}
	}
	if completionRows != 1 {
		t.Fatalf("completion/member/project assembled_result outcome rows = %d, want exactly 1: %+v", completionRows, result.Completeness.Outcomes)
	}
}
