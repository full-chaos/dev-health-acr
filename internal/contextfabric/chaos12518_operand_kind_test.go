package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12518: the model has no channel for a named operand's kind, so a named
// pair never reached ComparisonAdmittedNamedPair on a model turn and
// resolveNamedComparison ran only on hand-built frames. These tests run the real
// call site (Interpret -> resolveFrame) on the frame the sanitizer builds: terms
// and no kind.

const operandKindAsked = contractsv1.ContextFabricSubjectRepository

// interpretOperands runs one interpret turn and hands back the frame it resolved
// and the proposal the model emitted.
func interpretOperands(t *testing.T, frame QuestionFrame, edit func(*ModelExecutionReceipt)) (resolved, proposed *QuestionFrame) {
	t.Helper()
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.QuestionFrame = &frame
	edit(&receipt)
	interpreted := InterpretedQuestion{
		Shape: contractsv1.ContextFabricShapeExplicitCohort, RequestedJudgment: "compare the two",
		SubjectTerms: []string{"acme-api", "acme-web"}, TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
	interpreter := RuntimeQuestionInterpreter{Runtime: fakeModelRuntime{interpreted: interpreted, receipt: receipt}, Sink: &fakeReceiptSink{}}
	_, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if outcome.Frame == nil {
		t.Fatal("the frame did not validate, so this test proves nothing")
	}
	return outcome.Frame, &frame
}

func askedKind(receipt *ModelExecutionReceipt) { receipt.RequestedSubjectKind = operandKindAsked }

func TestAModelTurnReachesTheAdmittedNamedPair(t *testing.T) {
	t.Parallel()
	resolved, proposed := interpretOperands(t, comparisonFrame(
		comparisonNamedOperandWithoutKind("acme-api"),
		comparisonNamedOperandWithoutKind("acme-web"),
	), askedKind)

	operands := ClassifyComparisonOperands(resolved)
	if operands.Admission != ComparisonAdmittedNamedPair {
		t.Fatalf("admission = %q, want %q", operands.Admission, ComparisonAdmittedNamedPair)
	}
	for position, want := range []string{"acme-api", "acme-web"} {
		slot := operands.Slots[position]
		if slot.Kind != operandKindAsked || len(slot.Terms) != 1 || slot.Terms[0] != want {
			t.Fatalf("slot %d = %+v, want its own term %q and kind %q", position, slot, want, operandKindAsked)
		}
	}
	// The proposal telemetry shows what the model emitted (CHAOS-4975).
	for position, operand := range proposed.SubjectExpression.Explicit.Operands {
		if operand.Named.ExpectedKind != nil {
			t.Fatalf("proposed operand %d gained a kind the model never emitted", position)
		}
	}
}

func TestANamedPairOutsideTheBackfillKeepsItsAdmission(t *testing.T) {
	t.Parallel()
	bare := []SubjectOperand{comparisonNamedOperandWithoutKind("acme-api"), comparisonNamedOperandWithoutKind("acme-web")}
	for name, tc := range map[string]struct {
		operands []SubjectOperand
		edit     func(*ModelExecutionReceipt)
		want     ComparisonAdmission
	}{
		"no kind asked": {bare, func(*ModelExecutionReceipt) {}, ComparisonOutOfCutUnstatedKind},
		"a kind out of the vocabulary": {bare, func(r *ModelExecutionReceipt) {
			askedKind(r)
			r.RequestedSubjectKindUnrecognized = true
		}, ComparisonOutOfCutUnstatedKind},
		"one side already stated": {
			[]SubjectOperand{comparisonNamedOperand("acme-api", contractsv1.ContextFabricSubjectProject), comparisonNamedOperandWithoutKind("acme-web")},
			askedKind, ComparisonOutOfCutUnstatedKind,
		},
		"a scoped side": {
			[]SubjectOperand{comparisonNamedOperandWithoutKind("acme-api"), comparisonScopedOperand("platform", operandKindAsked)},
			askedKind, ComparisonOutOfCutUnstatedKind,
		},
		"three named sides": {
			[]SubjectOperand{comparisonNamedOperandWithoutKind("acme-api"), comparisonNamedOperandWithoutKind("acme-web"), comparisonNamedOperandWithoutKind("acme-ops")},
			askedKind, ComparisonOutOfCutOperandCount,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resolved, _ := interpretOperands(t, comparisonFrame(tc.operands...), tc.edit)
			if got := ClassifyComparisonOperands(resolved).Admission; got != tc.want {
				t.Fatalf("admission = %q, want %q", got, tc.want)
			}
			named := 0
			for position, operand := range resolved.SubjectExpression.Explicit.Operands {
				if operand.Named == nil {
					continue
				}
				named++
				stated := tc.operands[position].Named.ExpectedKind
				got := operand.Named.ExpectedKind
				if (stated == nil) != (got == nil) || (stated != nil && *stated != *got) {
					t.Fatalf("operand %d kind = %v, want it left as %v", position, got, stated)
				}
			}
			if named == 0 {
				t.Fatal("no named operand reached the assertion")
			}
		})
	}
}

// A valid frame always holds its set, so only a direct call can reach this.
func TestANamedPairBackfillOfNoSetIsNoSet(t *testing.T) {
	t.Parallel()
	if got := backfillNamedPairExpectedKind(nil, operandKindAsked); got != nil {
		t.Fatalf("backfill of no set = %+v, want nil", got)
	}
}
