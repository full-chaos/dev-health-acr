package contextfabric

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12596: a served OPERAND read row's serving kinds join the plan. Reach
// turn 1 compared two repositories; its state/operand/repository row named
// [health identity metrics] and the plan read health only.

type fixedRowsDeriver []DerivedRequirement

func (d fixedRowsDeriver) DeriveRequirements(QuestionFrame) []DerivedRequirement {
	return append([]DerivedRequirement(nil), d...)
}

func operandStateRow(subject SubjectKind, kinds ...FactKind) DerivedRequirement {
	return DerivedRequirement{
		RequirementCoordinate: RequirementCoordinate{Obligation: ObligationState, Role: SubjectRoleOperand, Subject: subject},
		Kind:                  ObligationKindRead, FactKinds: kinds, Quantifier: CompletionQuantifierCorroborated,
	}
}

func TestOperandReadRowKindsJoinThePlan(t *testing.T) {
	t.Parallel()
	definition, _ := LookupQuestionFamily(QuestionFamilyExplicitComparison)
	rows := []DerivedRequirement{operandStateRow(SubjectRepository, FactHealth, FactIdentity, FactMetrics)}
	model := InterpretedQuestion{FactRequirements: []FactRequirement{{Kind: FactPullRequests}, {Kind: FactFlow}}}
	got := planFactKinds(definition, model, rows)
	for _, kind := range []FactKind{FactIdentity, FactMetrics} {
		if !slices.Contains(got, kind) {
			t.Fatalf("plan = %v, want %s from the operand read row", got, kind)
		}
	}
	if got[0] != FactPullRequests || got[1] != FactFlow {
		t.Fatalf("plan = %v, want the model's kinds first (first-kind-wins)", got)
	}
	if requirements := operandReadFactRequirements(rows, false); len(requirements) != 3 || !slices.ContainsFunc(requirements, func(r FactRequirement) bool { return r.Kind == FactMetrics }) {
		t.Fatalf("operand read requirements = %v, want health, identity, metrics", requirements)
	}
	if requirements := operandReadFactRequirements(rows, true); requirements != nil {
		t.Fatalf("work-item tuple operand read requirements = %v, want none", requirements)
	}
}

// Only a served operand read row widens the plan by its serving kinds: a
// subject-role read row whose quantifier does not require every kind, a
// member-role read row, an unavailable operand row and a computed operand row
// add nothing. (A subject-role row whose quantifier requires every kind it
// names widens the plan under CHAOS-12710, pinned by
// TestOnlyRequiredKindsOfACommittedSubjectWidenThePlan.)
func TestOnlyServedOperandReadRowsWidenThePlan(t *testing.T) {
	t.Parallel()
	definition, _ := LookupQuestionFamily(QuestionFamilyExplicitComparison)
	base := planFactKinds(definition, InterpretedQuestion{}, nil)
	subjectRow := operandStateRow(SubjectRepository, FactHealth, FactIdentity, FactMetrics)
	subjectRow.Role = SubjectRoleSubject
	memberRow := operandStateRow(SubjectRepository, FactMetrics)
	memberRow.Role = SubjectRoleMember
	// A hand-built row naming kinds AND a reason breaks the row invariant
	// (exactly one of FactKinds / Step / Unavailable); the Served guard is what
	// keeps it out, the same defence ComputedStepInputReads pins.
	unavailable := operandStateRow(SubjectRepository, FactMetrics)
	unavailable.Unavailable = RequirementReasonNoDeclaringProducer
	computed := operandStateRow(SubjectRepository, FactMetrics)
	computed.Kind = ObligationKindComputed
	for name, row := range map[string]DerivedRequirement{"subject role": subjectRow, "member role": memberRow, "unavailable": unavailable, "computed": computed} {
		if got := planFactKinds(definition, InterpretedQuestion{}, []DerivedRequirement{row}); !slices.Equal(got, base) {
			t.Errorf("%s: plan = %v, want %v unchanged", name, got, base)
		}
	}
}

func TestComparisonTurnRecordsOperandReads(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	repository := SubjectRepository
	operand := func(term string) SubjectOperand {
		return SubjectOperand{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{Terms: []string{term}, ExpectedKind: &repository}}
	}
	frame := DeriveFrameObligations(QuestionFrame{
		Goals:             []InvestigationGoal{GoalCompare},
		SubjectExpression: SubjectExpression{Kind: SubjectExpressionExplicitSet, Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{operand("a"), operand("b")}}},
		Temporal:          TemporalIntentCurrent, Version: QuestionFrameVersion,
	}, nil)
	interpreter := &countingInterpreter{
		interpretation: InterpretedQuestion{Shape: ShapeExplicitCohort, RequestedJudgment: "compare", SubjectTerms: []string{"a", "b"}, ComparisonTerms: []string{"a", "b"}, TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{}},
		family:         QuestionFamilyOutcome{Family: QuestionFamilyExplicitComparison, Source: QuestionFamilySourceModel, Frame: &frame},
	}
	acr := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:acr", Label: "full-chaos/dev-health-acr"}
	ops := SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:ops", Label: "full-chaos/dev-health-ops"}
	var read []CanonicalFactRequest
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreter,
		Graph: &acceptanceGraphReader{resolution: SubjectResolution{
			Committed: []SubjectRef{acr, ops},
			Candidates: []SubjectCandidate{
				{Subject: acr, State: ResolutionCommitted, Confidence: 1, MatchReasons: []string{"exact"}, MatchedTerms: []string{"a"}},
				{Subject: ops, State: ResolutionCommitted, Confidence: 1, MatchReasons: []string{"exact"}, MatchedTerms: []string{"b"}},
			},
		}, context: emptyGraphContext()},
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			read = append(read, request)
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{}, errors.New("not under test")
		}),
		Results:      newMapResultStore(),
		Telemetry:    telemetry,
		Requirements: fixedRowsDeriver{operandStateRow(SubjectRepository, FactHealth, FactIdentity, FactMetrics)},
	}, EngineOptions{ServiceVersion: "operand-read-test", Now: func() time.Time { return time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC) }, NewResultID: func() string { return "result_operandread0001" }})
	if err != nil {
		t.Fatal(err)
	}
	// The turn stops at synthesis (the stub refuses). The fact read, and the
	// line, run before that.
	_, _ = engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if len(read) != 1 {
		t.Fatalf("fact reads = %d, want 1 (the turn must reach the canonical fact read)", len(read))
	}
	var kinds []FactKind
	for _, requirement := range read[0].Requirements {
		kinds = append(kinds, requirement.Kind)
	}
	for _, want := range []FactKind{FactHealth, FactIdentity, FactMetrics} {
		if !slices.Contains(kinds, want) {
			t.Errorf("fact request kinds = %v, want %s from the operand read row", kinds, want)
		}
	}
	if len(telemetry.operandReadKinds) != 1 || telemetry.operandReadKinds[0] != [3]int{1, 3, 3} {
		t.Fatalf("operand read lines = %v, want one line {rows 1, named 3, added 3}", telemetry.operandReadKinds)
	}
}
