package contextfabric_test

import (
	"bytes"
	"log/slog"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// CHAOS-12596, production capabilities: the reach turn-1 shape (two named
// repositories, compare, the model naming pull_requests and flow) plans
// metrics and identity, the kinds the operand row names for a repository; a
// team pair and a project pair plan what their rows name; a cohort frame
// plans nothing new (TestOnACohortFamilyTheInputConsumerAddsNothing).
func TestRepositoryComparisonPlansTheOperandRowKinds(t *testing.T) {
	capabilities := liveCapabilityList(t)
	seed := contextfabric.GenerateObligationSeed(capabilities)
	pair := func(kind contextfabric.SubjectKind) contextfabric.QuestionFrame {
		operand := func(term string) contextfabric.SubjectOperand {
			return contextfabric.SubjectOperand{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{Terms: []string{term}, ExpectedKind: traceKindPointer(kind)}}
		}
		return traceBuildFrame([]contextfabric.InvestigationGoal{contextfabric.GoalCompare}, contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionExplicitSet, Explicit: &contextfabric.ExplicitSetExpression{Operands: []contextfabric.SubjectOperand{operand("a"), operand("b")}},
		}, contextfabric.TemporalIntentCurrent, nil, nil)
	}
	model := contextfabric.InterpretedQuestion{FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactPullRequests}, {Kind: contextfabric.FactFlow}}}
	for _, kind := range []contextfabric.SubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectTeam, contextfabric.SubjectProject} {
		rows := contextfabric.DeriveRequirements(pair(kind), seed, capabilities)
		named := contextfabric.OperandReadRowServingKinds(rows)
		if len(named) == 0 {
			t.Fatalf("%s pair: no operand read row names a kind; this test measures nothing", kind)
		}
		plan := contextfabric.PlanAnswer(contextfabric.PlanAnswerInput{
			Family:         contextfabric.QuestionFamilyOutcome{Family: contextfabric.QuestionFamilyExplicitComparison, Source: contextfabric.QuestionFamilySourceModel},
			Interpretation: model, Requirements: rows,
		})
		for _, want := range named {
			if !slices.Contains(plan.FactKinds, want) {
				t.Errorf("%s pair: plan %v lacks %s, which the operand row names", kind, plan.FactKinds, want)
			}
		}
		if kind == contextfabric.SubjectRepository && !slices.Contains(plan.FactKinds, contextfabric.FactMetrics) {
			t.Errorf("repository pair: plan %v lacks metrics, the reach turn-1 gap", plan.FactKinds)
		}
	}
}

func TestPlanOperandReadsLineCertifies(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordOperandReadKinds(ctx, principalOrg1, 2, 3, 2)
	pinKeys(t, bytes.NewBuffer(buf.Bytes()), []string{"time", "level", "msg", "org_id", "operand_rows", "kinds_named", "kinds_added", "request_id"}, 1)
	certifyRequestLine(t, buf.Bytes(), eventspec.PlanOperandReads, map[string]any{"org_id": "org_1"})
}
