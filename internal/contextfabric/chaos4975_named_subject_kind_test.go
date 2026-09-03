package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4975: the kind offer for a named_subject question ("Why is the acr
// project struggling?") omits the frame's own declared kind, because
// NamedSubjectExpression.ExpectedKind has no production writer -- only test
// fixtures ever set it (CHAOS-4967's own handoff, confirmed again here by
// grep: zero non-test construction sites). RequestedSubjectKind
// (model_runtime.go) is the SAME call's sanitized declared-kind signal for
// exactly this case. These tests pin resolveFrame backfilling ExpectedKind
// from it, at frame-build time, so frameKindHints (which reads
// SubjectExpression.MemberKind(), extended in frame.go for the Named case)
// stops treating a named_subject frame as declaring no kind at all.

// TestSubjectExpression_MemberKind_NamedReadsExpectedKind pins the read
// side in isolation: MemberKind() is the single seam frameKindHints reads,
// and its other production caller (cohortKindFromFrame) is unaffected
// because it checks IsCohortVariant() first and named_subject is not a
// cohort variant.
func TestSubjectExpression_MemberKind_NamedReadsExpectedKind(t *testing.T) {
	t.Parallel()
	t.Run("expected kind set", func(t *testing.T) {
		expr := SubjectExpression{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{Terms: []string{"acr"}, ExpectedKind: kindPtr(contractsv1.ContextFabricSubjectProject)}}
		kind, ok := expr.MemberKind()
		if !ok || kind != contractsv1.ContextFabricSubjectProject {
			t.Fatalf("MemberKind() = (%q, %v), want (project, true)", kind, ok)
		}
	})
	t.Run("expected kind absent", func(t *testing.T) {
		expr := SubjectExpression{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{Terms: []string{"acr"}}}
		if kind, ok := expr.MemberKind(); ok {
			t.Fatalf("MemberKind() = (%q, %v), want (\"\", false) when ExpectedKind is nil", kind, ok)
		}
	})
	t.Run("named nil", func(t *testing.T) {
		expr := SubjectExpression{Kind: SubjectExpressionNamed}
		if kind, ok := expr.MemberKind(); ok {
			t.Fatalf("MemberKind() = (%q, %v), want (\"\", false) for a nil Named", kind, ok)
		}
	})
}

func namedFrameWithGoal(terms []string) QuestionFrame {
	return QuestionFrame{
		Goals: []InvestigationGoal{GoalExplainDrivers},
		SubjectExpression: SubjectExpression{
			Kind:  SubjectExpressionNamed,
			Named: &NamedSubjectExpression{Terms: terms},
		},
		Temporal: TemporalIntentCurrent,
	}
}

// TestInterpret_NamedSubjectFrame_BackfillsExpectedKindFromRequestedSubjectKind
// is the end-to-end red-first proof at the real call site (Interpret ->
// resolveFrame), mirroring chaos4632_interpreter_wiring_test.go's own style.
// RED on the code before this ticket's fix: MemberKind() had no Named case
// and resolveFrame never read RequestedSubjectKind, so
// QuestionFamilyOutcome.Frame.SubjectExpression.Named.ExpectedKind stayed
// nil no matter what the model emitted for requested_subject_kind.
func TestInterpret_NamedSubjectFrame_BackfillsExpectedKindFromRequestedSubjectKind(t *testing.T) {
	t.Parallel()
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.RequestedSubjectKind = contractsv1.ContextFabricSubjectProject
	frame := namedFrameWithGoal([]string{"acr"})
	receipt.QuestionFrame = &frame

	interpreted := InterpretedQuestion{
		Shape: ShapeSingleSubject, RequestedJudgment: "why is acr struggling",
		SubjectTerms: []string{"acr"}, TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}

	interpreter := RuntimeQuestionInterpreter{
		Runtime: fakeModelRuntime{interpreted: interpreted, receipt: receipt},
		Sink:    &fakeReceiptSink{},
	}
	_, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if outcome.Frame == nil {
		t.Fatal("outcome.Frame is nil -- the frame did not validate")
	}
	named := outcome.Frame.SubjectExpression.Named
	if named == nil || named.ExpectedKind == nil {
		t.Fatalf("Named.ExpectedKind = %+v, want backfilled to project", named)
	}
	if *named.ExpectedKind != contractsv1.ContextFabricSubjectProject {
		t.Fatalf("Named.ExpectedKind = %q, want project", *named.ExpectedKind)
	}
}

// TestInterpret_NamedSubjectFrame_NeverOverridesAModelStatedExpectedKind
// proves the backfill only fills a GAP: a frame the model already
// populated ExpectedKind on (a future model version, or a test fixture)
// is never second-guessed by RequestedSubjectKind, even when they disagree.
func TestInterpret_NamedSubjectFrame_NeverOverridesAModelStatedExpectedKind(t *testing.T) {
	t.Parallel()
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.RequestedSubjectKind = contractsv1.ContextFabricSubjectProject
	frame := namedFrameWithGoal([]string{"acr"})
	frame.SubjectExpression.Named.ExpectedKind = kindPtr(contractsv1.ContextFabricSubjectRepository)
	receipt.QuestionFrame = &frame

	interpreted := InterpretedQuestion{
		Shape: ShapeSingleSubject, RequestedJudgment: "why is acr struggling",
		SubjectTerms: []string{"acr"}, TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
	interpreter := RuntimeQuestionInterpreter{
		Runtime: fakeModelRuntime{interpreted: interpreted, receipt: receipt},
		Sink:    &fakeReceiptSink{},
	}
	_, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	named := outcome.Frame.SubjectExpression.Named
	if named == nil || named.ExpectedKind == nil || *named.ExpectedKind != contractsv1.ContextFabricSubjectRepository {
		t.Fatalf("Named.ExpectedKind = %+v, want untouched repository (model's own value)", named)
	}
}

// TestInterpret_NamedSubjectFrame_UnrecognizedRequestedKindNeverBackfills
// proves an out-of-vocabulary model emission never reaches the frame: the
// sanitizer already zeroes RequestedSubjectKind and sets the Unrecognized
// flag (genkitruntime/runtime.go sanitizeFamilyOutput), and this is the
// guard that keeps a corrupted receipt from producing a corrupted frame.
func TestInterpret_NamedSubjectFrame_UnrecognizedRequestedKindNeverBackfills(t *testing.T) {
	t.Parallel()
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.RequestedSubjectKind = ""
	receipt.RequestedSubjectKindUnrecognized = true
	frame := namedFrameWithGoal([]string{"acr"})
	receipt.QuestionFrame = &frame

	interpreted := InterpretedQuestion{
		Shape: ShapeSingleSubject, RequestedJudgment: "why is acr struggling",
		SubjectTerms: []string{"acr"}, TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
	interpreter := RuntimeQuestionInterpreter{
		Runtime: fakeModelRuntime{interpreted: interpreted, receipt: receipt},
		Sink:    &fakeReceiptSink{},
	}
	_, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if named := outcome.Frame.SubjectExpression.Named; named != nil && named.ExpectedKind != nil {
		t.Fatalf("Named.ExpectedKind = %v, want nil (unrecognized kind must never backfill)", *named.ExpectedKind)
	}
}

// TestInterpret_NamedSubjectFrame_RawProposalTelemetryNeverSeesTheBackfill
// is the artifact-diagnosability guard the ticket's own trace flagged:
// resolveFrame must back-fill only the VALIDATED copy, never mutate through
// the pointer the raw "proposed" frame shares with it (NormalizeFrame never
// touches SubjectExpression, so they alias the same *NamedSubjectExpression
// unless the fix allocates a new one). This test fails if a future edit
// reintroduces the shared-pointer mutation, by refusing the frame and
// checking the REFUSED telemetry copy is untouched.
func TestInterpret_NamedSubjectFrame_RawProposalTelemetryNeverSeesTheBackfill(t *testing.T) {
	t.Parallel()
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.RequestedSubjectKind = contractsv1.ContextFabricSubjectProject
	// No Goals -- fails frame validation (refused), so the receipt keeps
	// the RAW PROPOSAL per resolveFrame's own documented refusal branch.
	frame := QuestionFrame{
		SubjectExpression: SubjectExpression{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{Terms: []string{"acr"}}},
		Temporal:          TemporalIntentCurrent,
	}
	receipt.QuestionFrame = &frame

	interpreted := InterpretedQuestion{
		Shape: ShapeSingleSubject, RequestedJudgment: "why is acr struggling",
		SubjectTerms: []string{"acr"}, TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
	interpreter := RuntimeQuestionInterpreter{
		Runtime: fakeModelRuntime{interpreted: interpreted, receipt: receipt},
		Sink:    &fakeReceiptSink{},
	}
	_, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if outcome.Frame != nil {
		t.Fatal("outcome.Frame must be nil -- the frame was refused, never validated")
	}
	if named := frame.SubjectExpression.Named; named.ExpectedKind != nil {
		t.Fatalf("the raw proposal's own Named.ExpectedKind = %v, want nil -- the backfill must never touch the model's raw emission", *named.ExpectedKind)
	}
}
