package contextfabric

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// --- SynthesisDraft.ValidateAgainst: unsupported references, missing fields, invalid enums ---

func TestSynthesisDraftValidateAgainstRejectsDriverWithUnknownPathID(t *testing.T) {
	t.Parallel()
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	draft.Drivers[0].PathIDs = []string{"path_not_in_investigation"}
	if err := draft.ValidateAgainst(input); err == nil || !strings.Contains(err.Error(), "unknown path") {
		t.Fatalf("ValidateAgainst() error = %v, want unknown path error", err)
	}
}

func TestSynthesisDraftValidateAgainstRejectsDriverWithSubjectOutsideInvestigation(t *testing.T) {
	t.Parallel()
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	invented := SubjectRef{Kind: SubjectProject, CanonicalID: "project_invented_by_model", Label: "Invented Project"}
	draft.Drivers[0].AffectedSubjects = []SubjectRef{invented}
	if err := draft.ValidateAgainst(input); err == nil || !strings.Contains(err.Error(), "outside the investigation") {
		t.Fatalf("ValidateAgainst() error = %v, want subject outside investigation error", err)
	}
}

func TestSynthesisDraftValidateAgainstRejectsDriverWithUnknownEvidence(t *testing.T) {
	t.Parallel()
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	draft.Drivers[0].EvidenceRefIDs = []string{"evidence_invented_by_model"}
	if err := draft.ValidateAgainst(input); err == nil || !strings.Contains(err.Error(), "unknown evidence") {
		t.Fatalf("ValidateAgainst() error = %v, want unknown evidence error", err)
	}
}

func TestSynthesisDraftValidateAgainstRejectsFindingWithSubjectOutsideInvestigation(t *testing.T) {
	t.Parallel()
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	invented := SubjectRef{Kind: SubjectProject, CanonicalID: "project_invented_by_model", Label: "Invented Project"}
	draft.ReadinessGaps = []Finding{{
		FindingID: "finding_12345678", Kind: "readiness_gap", Summary: "Invented finding.",
		Subjects: []SubjectRef{invented}, EvidenceRefIDs: []string{"evidence_release_1234"},
	}}
	if err := draft.ValidateAgainst(input); err == nil || !strings.Contains(err.Error(), "outside the investigation") {
		t.Fatalf("ValidateAgainst() error = %v, want subject outside investigation error", err)
	}
}

func TestSynthesisDraftValidateAgainstRequiresDirectJudgmentForAnswerCapableStatus(t *testing.T) {
	t.Parallel()
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	draft.DirectJudgment = ""
	if err := draft.ValidateAgainst(input); err == nil || !strings.Contains(err.Error(), "direct judgment") {
		t.Fatalf("ValidateAgainst() error = %v, want missing direct judgment error", err)
	}
}

func TestSynthesisDraftValidateAgainstRequiresDeterministicAnswer(t *testing.T) {
	t.Parallel()
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	draft.DeterministicAnswer = ""
	if err := draft.ValidateAgainst(input); err == nil || !strings.Contains(err.Error(), "deterministic answer") {
		t.Fatalf("ValidateAgainst() error = %v, want missing deterministic answer error", err)
	}
}

func TestSynthesisDraftValidateAgainstRejectsInvalidStatus(t *testing.T) {
	t.Parallel()
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	draft.Status = "made_up_status"
	if err := draft.ValidateAgainst(input); err == nil || !strings.Contains(err.Error(), "status is invalid") {
		t.Fatalf("ValidateAgainst() error = %v, want invalid status error", err)
	}
}

func TestSynthesisDraftValidateAgainstAcceptsFullyClosedDraft(t *testing.T) {
	t.Parallel()
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v", err)
	}
}

// --- RuntimeQuestionInterpreter / RuntimeAnswerSynthesizer wiring ---

type fakeModelRuntime struct {
	interpreted InterpretedQuestion
	draft       SynthesisDraft
	interpErr   error
	synthErr    error
	receipt     ModelExecutionReceipt
}

func (f fakeModelRuntime) InterpretQuestion(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, ModelExecutionReceipt, error) {
	return f.interpreted, f.receipt, f.interpErr
}

func (f fakeModelRuntime) SynthesizeAnswer(context.Context, storage.Principal, SynthesisInput) (SynthesisDraft, ModelExecutionReceipt, error) {
	return f.draft, f.receipt, f.synthErr
}

type fakeReceiptSink struct {
	recorded []ModelExecutionReceipt
	err      error
}

func (f *fakeReceiptSink) RecordModelExecution(_ context.Context, _ storage.Principal, receipt ModelExecutionReceipt) error {
	f.recorded = append(f.recorded, receipt)
	return f.err
}

func TestRuntimeQuestionInterpreterReturnsErrModelUnavailableWhenRuntimeNil(t *testing.T) {
	t.Parallel()
	interpreter := RuntimeQuestionInterpreter{}
	_, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("Interpret() error = %v, want ErrModelUnavailable", err)
	}
}

func TestRuntimeQuestionInterpreterRecordsReceiptOnSuccess(t *testing.T) {
	t.Parallel()
	sink := &fakeReceiptSink{}
	interpreted := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status_and_drivers",
		TimeContext:      TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
	interpreter := RuntimeQuestionInterpreter{
		Runtime: fakeModelRuntime{interpreted: interpreted, receipt: validModelReceiptFixture(ModelOperationInterpret)},
		Sink:    sink,
	}
	got, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if got.RequestedJudgment != interpreted.RequestedJudgment {
		t.Fatalf("Interpret() = %#v", got)
	}
	if len(sink.recorded) != 1 || sink.recorded[0].Operation != ModelOperationInterpret {
		t.Fatalf("sink.recorded = %#v", sink.recorded)
	}
}

func TestRuntimeQuestionInterpreterWrapsInvalidStructuredOutput(t *testing.T) {
	t.Parallel()
	sink := &fakeReceiptSink{}
	// Shape is outside the domain enum: this is what the RuntimeQuestionInterpreter
	// port must catch for a ModelRuntime implementation that does not already
	// enforce InterpretedQuestion.Validate() itself.
	invalid := InterpretedQuestion{
		Shape: "not_a_real_shape", RequestedJudgment: "status_and_drivers",
		TimeContext: TimeContext{Axis: TemporalCurrent},
	}
	interpreter := RuntimeQuestionInterpreter{
		Runtime: fakeModelRuntime{interpreted: invalid, receipt: validModelReceiptFixture(ModelOperationInterpret)},
		Sink:    sink,
	}
	_, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if !errors.Is(err, ErrModelOutput) {
		t.Fatalf("Interpret() error = %v, want ErrModelOutput", err)
	}
	// CHAOS-3784: an invalid enum is still an interpretation-side
	// rejection (ErrInterpretationRejected), but it is a business rule,
	// not a registry bound -- no ModelBoundViolation in the chain.
	if !errors.Is(err, ErrInterpretationRejected) {
		t.Fatalf("Interpret() error = %v, want ErrInterpretationRejected", err)
	}
	var violation *ModelBoundViolation
	if errors.As(err, &violation) {
		t.Fatalf("Interpret() error = %v, want no ModelBoundViolation for an invalid enum", err)
	}
}

// TestRuntimeQuestionInterpreterClassifiesBoundViolationAsInterpretationRejected
// is the CHAOS-3784 probe reproducing the CHAOS-3770 live-acceptance
// evidence exactly: a 259-character requested_judgment, one character past
// the 256-character cap (validate_context_fabric_result.go,
// ContextFabricRequestedJudgmentMaxLength) -- 5 of 8 real failures in that
// batch had this shape, and every one surfaced identically to a synthesis
// rejection or a provider error at the API layer. This pins that the
// runtime layer now carries enough information (ErrInterpretationRejected
// plus a ModelBoundViolation naming the exact bound) to tell them apart.
func TestRuntimeQuestionInterpreterClassifiesBoundViolationAsInterpretationRejected(t *testing.T) {
	t.Parallel()
	sink := &fakeReceiptSink{}
	invalid := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: strings.Repeat("a", 259),
		TimeContext: TimeContext{Axis: TemporalCurrent},
	}
	interpreter := RuntimeQuestionInterpreter{
		Runtime: fakeModelRuntime{interpreted: invalid, receipt: validModelReceiptFixture(ModelOperationInterpret)},
		Sink:    sink,
	}
	_, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if !errors.Is(err, ErrInterpretationRejected) || !errors.Is(err, ErrModelOutput) {
		t.Fatalf("Interpret() error = %v, want both ErrInterpretationRejected and ErrModelOutput", err)
	}
	if errors.Is(err, ErrSynthesisRejected) {
		t.Fatalf("Interpret() error = %v, must not also classify as ErrSynthesisRejected", err)
	}
	var violation *ModelBoundViolation
	if !errors.As(err, &violation) {
		t.Fatalf("Interpret() error = %v, want a ModelBoundViolation", err)
	}
	if violation.Bound != "interpretation.requested_judgment.max_length" {
		t.Fatalf("violation.Bound = %q, want interpretation.requested_judgment.max_length", violation.Bound)
	}
}

// TestRuntimeQuestionInterpreterCorrectsReceiptOutcomeToInvalidOutputOnRejection
// guards against a regression where the receipt was recorded with whatever
// Outcome the ModelRuntime returned (here "success", simulating a
// ModelRuntime implementation that does not self-validate) BEFORE this
// adapter's own InterpretedQuestion.Validate() call, so a receipt claiming
// success was durably persisted for output this same call then rejected.
func TestRuntimeQuestionInterpreterCorrectsReceiptOutcomeToInvalidOutputOnRejection(t *testing.T) {
	t.Parallel()
	sink := &fakeReceiptSink{}
	invalid := InterpretedQuestion{Shape: "not_a_real_shape", RequestedJudgment: "x", TimeContext: TimeContext{Axis: TemporalCurrent}}
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.Outcome = "success"
	interpreter := RuntimeQuestionInterpreter{
		Runtime: fakeModelRuntime{interpreted: invalid, receipt: receipt},
		Sink:    sink,
	}
	if _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); !errors.Is(err, ErrModelOutput) {
		t.Fatalf("Interpret() error = %v, want ErrModelOutput", err)
	}
	if len(sink.recorded) != 1 || sink.recorded[0].Outcome != "invalid_output" {
		t.Fatalf("sink.recorded = %#v, want exactly one receipt with outcome invalid_output", sink.recorded)
	}
}

// TestRuntimeQuestionInterpreterPromotesPendingValidationOutcomeToSuccess
// guards against the ADR 0008 "pending_validation" outcome never being
// resolved: a ModelRuntime that has not itself validated its output records
// pending_validation, and this adapter is the one place that applies
// InterpretedQuestion.Validate() and must promote the outcome once it does.
func TestRuntimeQuestionInterpreterPromotesPendingValidationOutcomeToSuccess(t *testing.T) {
	t.Parallel()
	sink := &fakeReceiptSink{}
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.Outcome = "pending_validation"
	interpreted := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status_and_drivers", TimeContext: TimeContext{Axis: TemporalCurrent},
	}
	interpreter := RuntimeQuestionInterpreter{
		Runtime: fakeModelRuntime{interpreted: interpreted, receipt: receipt},
		Sink:    sink,
	}
	if _, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest()); err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if len(sink.recorded) != 1 || sink.recorded[0].Outcome != "success" {
		t.Fatalf("sink.recorded = %#v, want exactly one receipt with outcome success", sink.recorded)
	}
}

// TestRuntimeQuestionInterpreterSurfacesSinkFailureAlongsideValidationFailure
// guards against a regression where recordModelReceipt's error was
// discarded whenever a domain validation error was already set: the
// `sinkErr != nil && err == nil` guard means a sink outage on an
// already-rejected draft is silently dropped, leaving the caller with only
// ErrModelOutput and no signal that the rejection receipt itself was never
// durably recorded.
func TestRuntimeQuestionInterpreterSurfacesSinkFailureAlongsideValidationFailure(t *testing.T) {
	t.Parallel()
	sinkErr := errors.New("receipt sink unavailable")
	sink := &fakeReceiptSink{err: sinkErr}
	invalid := InterpretedQuestion{Shape: "not_a_real_shape", RequestedJudgment: "x", TimeContext: TimeContext{Axis: TemporalCurrent}}
	interpreter := RuntimeQuestionInterpreter{
		Runtime: fakeModelRuntime{interpreted: invalid, receipt: validModelReceiptFixture(ModelOperationInterpret)},
		Sink:    sink,
	}
	_, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if !errors.Is(err, ErrModelOutput) {
		t.Fatalf("Interpret() error = %v, want ErrModelOutput", err)
	}
	if !errors.Is(err, sinkErr) {
		t.Fatalf("Interpret() error = %v, want it to also surface the sink failure %v (the rejection receipt was never durably recorded)", err, sinkErr)
	}
}

func TestRuntimeAnswerSynthesizerReturnsErrModelUnavailableWhenRuntimeNil(t *testing.T) {
	t.Parallel()
	synthesizer := RuntimeAnswerSynthesizer{}
	_, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInputFixture())
	if !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("Synthesize() error = %v, want ErrModelUnavailable", err)
	}
}

func TestRuntimeAnswerSynthesizerRejectsDraftThatInventsEvidence(t *testing.T) {
	t.Parallel()
	sink := &fakeReceiptSink{}
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	draft.EvidenceRefIDs = []string{"evidence_invented_by_model"}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:    sink,
	}
	_, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if !errors.Is(err, ErrModelOutput) {
		t.Fatalf("Synthesize() error = %v, want ErrModelOutput", err)
	}
	// CHAOS-3784: inventing evidence is a claim-binding/grounding business
	// rule, not a registry bound -- ErrSynthesisRejected without a
	// ModelBoundViolation.
	if !errors.Is(err, ErrSynthesisRejected) {
		t.Fatalf("Synthesize() error = %v, want ErrSynthesisRejected", err)
	}
	var violation *ModelBoundViolation
	if errors.As(err, &violation) {
		t.Fatalf("Synthesize() error = %v, want no ModelBoundViolation for invented evidence", err)
	}
}

// TestRuntimeAnswerSynthesizerClassifiesBoundViolationAsSynthesisRejected is
// the synthesis-side counterpart of
// TestRuntimeQuestionInterpreterClassifiesBoundViolationAsInterpretationRejected
// (CHAOS-3784): an over-length driver title is a registry bound
// (synthesis.driver.title.max_length), so the runtime must report
// ErrSynthesisRejected AND the exact violated bound, distinct from both
// ErrInterpretationRejected and a claim-binding/grounding rejection with no
// bound.
func TestRuntimeAnswerSynthesizerClassifiesBoundViolationAsSynthesisRejected(t *testing.T) {
	t.Parallel()
	sink := &fakeReceiptSink{}
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	draft.Drivers[0].Title = strings.Repeat("a", contractsv1.ContextFabricDriverTitleMaxLength+1)
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:    sink,
	}
	_, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if !errors.Is(err, ErrSynthesisRejected) || !errors.Is(err, ErrModelOutput) {
		t.Fatalf("Synthesize() error = %v, want both ErrSynthesisRejected and ErrModelOutput", err)
	}
	if errors.Is(err, ErrInterpretationRejected) {
		t.Fatalf("Synthesize() error = %v, must not also classify as ErrInterpretationRejected", err)
	}
	var violation *ModelBoundViolation
	if !errors.As(err, &violation) {
		t.Fatalf("Synthesize() error = %v, want a ModelBoundViolation", err)
	}
	if violation.Bound != "synthesis.driver.title.max_length" {
		t.Fatalf("violation.Bound = %q, want synthesis.driver.title.max_length", violation.Bound)
	}
}

// TestRuntimeAnswerSynthesizerReportsTheSectionValidateAgainstActuallyRejectedOn
// is the CHAOS-3784 round-3 R3-2 regression. Two DIFFERENT finding
// sections (remaining_work, readiness_gaps) each carry a DIFFERENT genuine
// bound violation. Before this fix, ValidateAgainst walked sections via a
// Go map -- iteration order randomized per range, so which section's
// error (and therefore which bound a caller sees) surfaced was
// nondeterministic, even though diagnoseSynthesisDraftBound always
// diagnoses in the same fixed order (remaining_work, readiness_gaps,
// conflicts) -- the two could disagree. Both now walk sections in that
// identical fixed order, so remaining_work's violation must be the one
// that rejects, and its bound the one reported, on EVERY call -- looped
// many times, since Go randomizes map iteration order on every range, not
// just across process runs, so a real regression would show up within a
// single test run.
func TestRuntimeAnswerSynthesizerReportsTheSectionValidateAgainstActuallyRejectedOn(t *testing.T) {
	t.Parallel()
	input := validSynthesisInputFixture()
	base := validSynthesisDraftFixture(input)
	base.RemainingWork = []Finding{{
		FindingID: "finding_12345678", Kind: "narrative",
		Summary:        strings.Repeat("a", contractsv1.ContextFabricFindingSummaryMaxLength+1),
		EvidenceRefIDs: []string{"evidence_release_1234"},
	}}
	base.ReadinessGaps = []Finding{{
		FindingID: "finding_87654321", Kind: strings.Repeat("k", contractsv1.ContextFabricFindingKindMaxLength+1),
		Summary:        "Readiness gap summary.",
		EvidenceRefIDs: []string{"evidence_release_1234"},
	}}
	for i := 0; i < 50; i++ {
		synthesizer := RuntimeAnswerSynthesizer{
			Runtime: fakeModelRuntime{draft: base, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
			Sink:    &fakeReceiptSink{},
		}
		_, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
		if !strings.Contains(err.Error(), "remaining_work:") {
			t.Fatalf("iteration %d: error = %v, want the remaining_work section to be the one ValidateAgainst rejected on", i, err)
		}
		var violation *ModelBoundViolation
		if !errors.As(err, &violation) {
			t.Fatalf("iteration %d: error = %v, want a ModelBoundViolation", i, err)
		}
		if violation.Bound != "synthesis.finding.summary.max_length" {
			t.Fatalf("iteration %d: violation.Bound = %q, want synthesis.finding.summary.max_length (remaining_work's violation, not readiness_gaps')", i, violation.Bound)
		}
	}
}

// TestRuntimeAnswerSynthesizerCorrectsReceiptOutcomeToInvalidOutputOnRejection
// guards against a regression where the receipt was recorded with whatever
// Outcome the ModelRuntime returned (here "success") BEFORE this adapter's
// own draft.ValidateAgainst(input) call, so a receipt claiming success was
// durably persisted for a draft this same call then rejected with
// ErrModelOutput.
func TestRuntimeAnswerSynthesizerCorrectsReceiptOutcomeToInvalidOutputOnRejection(t *testing.T) {
	t.Parallel()
	sink := &fakeReceiptSink{}
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	draft.EvidenceRefIDs = []string{"evidence_invented_by_model"}
	receipt := validModelReceiptFixture(ModelOperationSynthesize)
	receipt.Outcome = "success"
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: receipt},
		Sink:    sink,
	}
	if _, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input); !errors.Is(err, ErrModelOutput) {
		t.Fatalf("Synthesize() error = %v, want ErrModelOutput", err)
	}
	if len(sink.recorded) != 1 || sink.recorded[0].Outcome != "invalid_output" {
		t.Fatalf("sink.recorded = %#v, want exactly one receipt with outcome invalid_output", sink.recorded)
	}
}

// TestRuntimeAnswerSynthesizerSurfacesSinkFailureAlongsideValidationFailure
// is the Synthesize-path counterpart of the Interpret-path regression
// above: a sink outage on an already-rejected draft must not be silently
// discarded.
func TestRuntimeAnswerSynthesizerSurfacesSinkFailureAlongsideValidationFailure(t *testing.T) {
	t.Parallel()
	sinkErr := errors.New("receipt sink unavailable")
	sink := &fakeReceiptSink{err: sinkErr}
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	draft.EvidenceRefIDs = []string{"evidence_invented_by_model"}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:    sink,
	}
	_, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if !errors.Is(err, ErrModelOutput) {
		t.Fatalf("Synthesize() error = %v, want ErrModelOutput", err)
	}
	if !errors.Is(err, sinkErr) {
		t.Fatalf("Synthesize() error = %v, want it to also surface the sink failure %v (the rejection receipt was never durably recorded)", err, sinkErr)
	}
}

// TestRuntimeAnswerSynthesizerDowngradesRejectedFallbackOutcomeToInvalidOutput
// covers the fallback seam specifically: a ModelRuntime may record
// Outcome:"fallback" when its primary call failed and a fallback draft was
// returned instead, but if that fallback draft itself fails this adapter's
// validation, the persisted outcome must become invalid_output -- a rejected
// fallback is not a successful fallback.
func TestRuntimeAnswerSynthesizerDowngradesRejectedFallbackOutcomeToInvalidOutput(t *testing.T) {
	t.Parallel()
	sink := &fakeReceiptSink{}
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	draft.EvidenceRefIDs = []string{"evidence_invented_by_model"}
	receipt := validModelReceiptFixture(ModelOperationSynthesize)
	receipt.Outcome = "fallback"
	receipt.FallbackUsed = true
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: receipt},
		Sink:    sink,
	}
	if _, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input); !errors.Is(err, ErrModelOutput) {
		t.Fatalf("Synthesize() error = %v, want ErrModelOutput", err)
	}
	if len(sink.recorded) != 1 || sink.recorded[0].Outcome != "invalid_output" {
		t.Fatalf("sink.recorded = %#v, want exactly one receipt with outcome invalid_output", sink.recorded)
	}
}

// TestRuntimeAnswerSynthesizerPromotesPendingValidationOutcomeToSuccess
// guards against the ADR 0008 "pending_validation" outcome never being
// resolved for the synthesis path.
func TestRuntimeAnswerSynthesizerPromotesPendingValidationOutcomeToSuccess(t *testing.T) {
	t.Parallel()
	sink := &fakeReceiptSink{}
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	receipt := validModelReceiptFixture(ModelOperationSynthesize)
	receipt.Outcome = "pending_validation"
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: receipt},
		Sink:    sink,
	}
	if _, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input); err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(sink.recorded) != 1 || sink.recorded[0].Outcome != "success" {
		t.Fatalf("sink.recorded = %#v, want exactly one receipt with outcome success", sink.recorded)
	}
}

func TestRuntimeAnswerSynthesizerBuildsResultVersionsFromReceiptAndOptions(t *testing.T) {
	t.Parallel()
	sink := &fakeReceiptSink{}
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	receipt := validModelReceiptFixture(ModelOperationSynthesize)
	receipt.SchemaVersion = "schema-v9"
	receipt.PromptVersion = "prompt-v9"
	receipt.ModelVersion = "model-v9"
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: receipt},
		Sink:    sink,
		Options: RuntimeAnswerSynthesizerOptions{
			ServiceVersion: "acr-v9", Backend: "graph", BackendVersion: "graph-v1",
			ProjectionVersion: "projection-v9", QueryVersion: "query-v9", CanonicalServiceVersion: "ops-v9",
		},
	}
	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if result.Versions.InterpretationVersion != "schema-v9" || result.Versions.SynthesisVersion != "prompt-v9+model-v9" {
		t.Fatalf("Versions = %#v", result.Versions)
	}
	if result.Versions.ServiceVersion != "acr-v9" || result.Versions.Backend != "graph" || result.Versions.CanonicalServiceVersion != "ops-v9" {
		t.Fatalf("Versions = %#v", result.Versions)
	}
	if len(sink.recorded) != 1 {
		t.Fatalf("sink.recorded = %#v", sink.recorded)
	}
}

// TestRuntimeAnswerSynthesizerProducesNonNilCollectionsForEmptyDraft guards
// against a regression where an ordinary, fully valid draft with no
// conflicts/limitations/warnings/etc. produced a public InvestigationResult
// with nil collection fields (via append([]T(nil), empty...)) that the
// public contract validator rejects outright, so the engine would fail
// closed on the common case of a clean, no-conflict investigation.
func TestRuntimeAnswerSynthesizerProducesNonNilCollectionsForEmptyDraft(t *testing.T) {
	t.Parallel()
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input) // RemainingWork/ReadinessGaps/Conflicts/Limitations/Warnings are all empty
	draft.StrongestPressures = nil             // no pressures identified is an ordinary, valid draft state
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:    &fakeReceiptSink{},
		Options: RuntimeAnswerSynthesizerOptions{ServiceVersion: "acr-v1", Backend: "graph", ProjectionVersion: "p-v1", QueryVersion: "q-v1", CanonicalServiceVersion: "ops-v1"},
	}
	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	for name, isNil := range map[string]bool{
		"StrongestPressures": result.StrongestPressures == nil,
		"Drivers":            result.Drivers == nil, "RemainingWork": result.RemainingWork == nil, "ReadinessGaps": result.ReadinessGaps == nil,
		"Paths": result.Paths == nil, "Conflicts": result.Conflicts == nil, "Limitations": result.Limitations == nil,
		"EvidenceRefIDs": result.EvidenceRefIDs == nil, "Warnings": result.Warnings == nil,
	} {
		if isNil {
			t.Errorf("result.%s is nil; the public contract validator rejects that", name)
		}
	}
	// Fill in the envelope fields the engine (not the synthesizer) owns, then
	// confirm the result the engine would return is contract-valid end to end.
	result.SchemaVersion = InvestigationResultSchemaV1
	result.ResultID = "result_12345678"
	result.RequestID = "request_12345678"
	result.GeneratedAt = time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	result.Question = input.Request.Question
	result.Interpretation = input.Interpretation
	result.SubjectResolution = input.Graph.Resolution
	if err := result.Validate(); err != nil {
		t.Fatalf("result.Validate() error = %v, want a valid public result for an empty-collection draft", err)
	}
	// The Go validator (checked above) permits StrongestPressures to be
	// nil, but the JSON Schema (contracts/jsonschema/v1/
	// context_fabric_investigation_result.v1.schema.json) requires
	// strongest_pressures as a non-nullable array and lists it in
	// "required" -- so a nil slice, which encoding/json marshals to JSON
	// null (there is no `omitempty` on this field), is wire-invalid even
	// though it passes Go-level Validate(). Assert the wire bytes directly
	// so the two validation layers can't silently disagree again.
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), `"strongest_pressures":null`) {
		t.Fatalf("WIRE-INVALID: strongest_pressures marshaled to null, but the JSON Schema requires an array: %s", encoded)
	}
}

// --- fixtures ---

func validSynthesisInputFixture() SynthesisInput {
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	workItem := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_1", Label: "Release acceptance"}
	path := RelationshipPath{
		PathID: "path_12345678", Nodes: []SubjectRef{project, workItem},
		Edges: []RelationshipEdge{{
			Type: "BLOCKS", From: project, To: workItem,
			Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved,
			EvidenceRefIDs: []string{"evidence_release_1234"},
		}},
		WhyRelevant: "The open work blocks release.", EvidenceRefIDs: []string{"evidence_release_1234"},
	}
	return SynthesisInput{
		Request: validInvestigationRequest(),
		Interpretation: InterpretedQuestion{
			Shape: ShapeOpen, RequestedJudgment: "status_and_drivers",
			TimeContext:      TimeContext{Axis: TemporalCurrent},
			FactRequirements: []FactRequirement{{Kind: FactReadiness}},
		},
		Graph: GraphContext{
			Resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			Paths:      []RelationshipPath{path}, EvidenceRefIDs: []string{"evidence_release_1234"},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
		Facts: CanonicalFactBundle{
			Facts: []CanonicalFact{{
				Kind: FactReadiness, Subject: project,
				Fields:         map[string]FactValue{"release_ready": BooleanFactValue(false)},
				EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: SourceAvailable,
				Source: "ops", SourceVersion: "v1",
			}},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1",
		},
	}
}

func validSynthesisDraftFixture(input SynthesisInput) SynthesisDraft {
	project := input.Graph.Resolution.Committed[0]
	return SynthesisDraft{
		Status: InvestigationComplete, DirectJudgment: "Ask Dev is not release-ready.",
		CurrentState:       "Tracked completion and release readiness diverge.",
		StrongestPressures: []string{"Release acceptance remains open."},
		Drivers: []DriverJudgment{{
			DriverID: "driver_12345678", Standing: DriverPrincipal,
			Category: "relationship", Title: "Release acceptance remains open",
			Summary: "Required acceptance has not completed.", AffectedSubjects: []SubjectRef{project},
			PathIDs: []string{"path_12345678"}, EvidenceRefIDs: []string{"evidence_release_1234"},
			Derivation: DerivationRuleInferred, EpistemicStatus: EpistemicInferred,
			Confidence: 0.9, Current: true,
		}},
		RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Conflicts: []Finding{},
		Limitations: []string{}, EvidenceRefIDs: []string{"evidence_release_1234"},
		DeterministicAnswer: "Ask Dev is not release-ready because release acceptance remains open.", Warnings: []string{},
	}
}

func validModelReceiptFixture(operation ModelOperation) ModelExecutionReceipt {
	now := time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)
	return ModelExecutionReceipt{
		Operation: operation, Provider: "test-provider", Model: "test-model", ModelVersion: "model-v1",
		PromptVersion: "prompt-v1", SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1",
		StartedAt: now, CompletedAt: now, Attempts: 1,
		InputDigest: strings.Repeat("a", 64), OutputDigest: strings.Repeat("b", 64), Outcome: "success",
	}
}

var _ ModelRuntime = fakeModelRuntime{}
var _ ModelReceiptSink = (*fakeReceiptSink)(nil)

// --- Value-level evidence closure (CHAOS-3755) ---

// closureFixture builds a SynthesisInput/valid draft pair where the driver's
// Category is "readiness" -- a category in
// ContextFabricDriverCategoryRequiresClaimedFact's closed table -- backed by
// a canonical FactReadiness fact with field release_ready=false, and a
// ClaimedFact that restates it correctly. This is the exact scenario the
// Linear must-do named: "a synthesis draft claiming 'release-ready' against
// canonical release_ready=false".
func closureFixture() (SynthesisInput, SynthesisDraft) {
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	draft.Drivers[0].Category = "readiness"
	draft.Drivers[0].ClaimedFactIDs = []string{"claim_readiness_1"}
	draft.ClaimedFacts = []ClaimedFact{{
		ClaimID: "claim_readiness_1", Kind: FactReadiness, Subject: input.Graph.Resolution.Committed[0],
		Field: "release_ready", Value: boolScalar(false),
	}}
	return input, draft
}

func TestSynthesisDraftValidateAgainstAcceptsClaimedFactMatchingCanonicalValue(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want a matching claim to validate", err)
	}
}

// TestSynthesisDraftValidateAgainstRejectsClaimedFactContradictingCanonicalValue
// is the direct proof for the Linear must-do: a claim that restates a
// canonical field with the WRONG value must not survive, even though it
// references a real field on a real canonical fact (unlike the older
// "invents evidence/subject/path" tests, this is a value-level mismatch,
// not a structural one).
func TestSynthesisDraftValidateAgainstRejectsClaimedFactContradictingCanonicalValue(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	// The canonical fact says release_ready=false; the model claims true.
	draft.ClaimedFacts[0].Value = boolScalar(true)
	err := draft.ValidateAgainst(input)
	if err == nil || !strings.Contains(err.Error(), "contradicts the canonical value") {
		t.Fatalf("ValidateAgainst() error = %v, want a contradicts-canonical-value error", err)
	}
}

func TestSynthesisDraftValidateAgainstRejectsClaimedFactForUnobservedField(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	draft.ClaimedFacts[0].Field = "field_never_observed"
	err := draft.ValidateAgainst(input)
	if err == nil || !strings.Contains(err.Error(), "not canonically observed") {
		t.Fatalf("ValidateAgainst() error = %v, want a not-canonically-observed error", err)
	}
}

func TestSynthesisDraftValidateAgainstRejectsClaimedFactForKindWithNoCanonicalObservation(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	// FactHealth was never read into input.Facts.Facts at all.
	draft.ClaimedFacts[0].Kind = FactHealth
	err := draft.ValidateAgainst(input)
	if err == nil || !strings.Contains(err.Error(), "no canonical observation") {
		t.Fatalf("ValidateAgainst() error = %v, want a no-canonical-observation error", err)
	}
}

func TestSynthesisDraftValidateAgainstRejectsDriverInFactShapedCategoryWithoutAnyClaim(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	draft.Drivers[0].ClaimedFactIDs = nil
	draft.ClaimedFacts = nil
	err := draft.ValidateAgainst(input)
	if err == nil || !strings.Contains(err.Error(), "requires a claimed fact") {
		t.Fatalf("ValidateAgainst() error = %v, want a category-requires-claimed-fact error", err)
	}
}

// TestSynthesisDraftValidateAgainstDoesNotRequireClaimsForNarrativeCategory
// proves the category->claim requirement is a closed enum lookup, not a
// blanket rule: "relationship" (the shared fixture's category) is a known
// narrative/graph-associated category, deliberately absent from
// ContextFabricDriverCategoryRequiresClaimedFact's table, so plain
// evidence closure remains sufficient for it.
func TestSynthesisDraftValidateAgainstDoesNotRequireClaimsForNarrativeCategory(t *testing.T) {
	t.Parallel()
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want a narrative category to validate without any claim", err)
	}
}

// TestSynthesisDraftValidateAgainstRejectsUnrecognizedCategory is the H4
// fix itself (Codex adversarial review, CHAOS-3755): Category is now a
// closed contract enum (ContextFabricDriverCategory), so a model that
// picks a novel spelling to dodge ContextFabricDriverCategoryRequiresClaimedFact's
// exact-match lookup is rejected outright at driver.Validate(), not
// silently treated as an unmapped/no-claim-required category. This
// replaces the prior test of the same shape, which unintentionally
// "blessed" the bypass by asserting a free-text category validated fine.
func TestSynthesisDraftValidateAgainstRejectsUnrecognizedCategory(t *testing.T) {
	t.Parallel()
	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	draft.Drivers[0].Category = "release_readiness_but_spelled_differently_to_dodge_the_table"
	err := draft.ValidateAgainst(input)
	if err == nil || !strings.Contains(err.Error(), "driver judgment violates v1 bounds") {
		t.Fatalf("ValidateAgainst() error = %v, want an unrecognized category to be rejected outright", err)
	}
}

func TestSynthesisDraftValidateAgainstRejectsUnknownClaimedFactID(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	draft.Drivers[0].ClaimedFactIDs = []string{"claim_never_declared"}
	err := draft.ValidateAgainst(input)
	if err == nil || !strings.Contains(err.Error(), "unknown claimed fact") {
		t.Fatalf("ValidateAgainst() error = %v, want an unknown-claimed-fact error", err)
	}
}

func TestSynthesisDraftValidateAgainstRejectsClaimedFactSubjectOutsideInvestigation(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	draft.ClaimedFacts[0].Subject = SubjectRef{Kind: SubjectProject, CanonicalID: "project_invented_by_model", Label: "Invented"}
	err := draft.ValidateAgainst(input)
	if err == nil || !strings.Contains(err.Error(), "outside the investigation") {
		t.Fatalf("ValidateAgainst() error = %v, want a subject-outside-investigation error", err)
	}
}

// TestRuntimeAnswerSynthesizerComposesDeterministicAnswerServerSide proves
// DeterministicAnswer is a pure function of validated Status/Drivers/
// ClaimedFacts, not whatever prose the model produced: the model's own
// DeterministicAnswer text is discarded entirely, and the server-composed
// replacement can never itself contradict a canonical value because it
// only ever renders values already proven equal to the canonical bundle.
func TestRuntimeAnswerSynthesizerComposesDeterministicAnswerServerSide(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	draft.DeterministicAnswer = "the model's own possibly-ungrounded prose, which must be discarded"
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
	}
	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if strings.Contains(result.DeterministicAnswer, "possibly-ungrounded") {
		t.Fatalf("DeterministicAnswer = %q, want the model's prose discarded", result.DeterministicAnswer)
	}
	if !strings.Contains(result.DeterministicAnswer, "Release acceptance remains open") {
		t.Fatalf("DeterministicAnswer = %q, want it to include the principal driver title", result.DeterministicAnswer)
	}
	if !strings.Contains(result.DeterministicAnswer, "readiness.release_ready=false") {
		t.Fatalf("DeterministicAnswer = %q, want it to include the claimed fact restatement", result.DeterministicAnswer)
	}
	if len(result.ClaimedFacts) != 1 || result.ClaimedFacts[0].ClaimID != "claim_readiness_1" {
		t.Fatalf("result.ClaimedFacts = %#v, want the validated claim carried through", result.ClaimedFacts)
	}
}

func boolScalar(value bool) ScalarValue { return ScalarValue{Boolean: &value} }

// --- H1/H3 (Codex adversarial review): claims must bind to their citing
// driver/finding's subjects, and subject/claim labels must match the
// investigation input verbatim. ---

func TestSynthesisDraftValidateAgainstRejectsClaimAboutSubjectOutsideDriverAffectedSubjects(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	workItem := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_1", Label: "Release acceptance"}
	// A second, real, in-bounds canonical fact -- about workItem, not
	// project (the driver's only AffectedSubjects entry).
	input.Facts.Facts = append(input.Facts.Facts, CanonicalFact{
		Kind: FactReadiness, Subject: workItem, Fields: map[string]FactValue{"release_ready": BooleanFactValue(true)},
		EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	})
	draft.ClaimedFacts = append(draft.ClaimedFacts, ClaimedFact{
		ClaimID: "claim_readiness_workitem", Kind: FactReadiness, Subject: workItem, Field: "release_ready", Value: boolScalar(true),
	})
	// The driver is about `project` (AffectedSubjects=[project]) but cites
	// the workItem claim instead of (or in addition to) its own subject's
	// claim -- a false public assertion about workItem's readiness under
	// a driver whose AffectedSubjects never named it.
	draft.Drivers[0].ClaimedFactIDs = []string{"claim_readiness_workitem"}
	err := draft.ValidateAgainst(input)
	if err == nil || !strings.Contains(err.Error(), "outside its own affected subjects") {
		t.Fatalf("ValidateAgainst() error = %v, want a claim-outside-affected-subjects error", err)
	}
}

func TestSynthesisDraftValidateAgainstAllowsFindingClaimAboutInvestigationSubjectWhenFindingHasNoSubjects(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	// A finding with no Subjects of its own falls back to the whole
	// investigation's subjects (not zero subjects, which would make every
	// claim reference impossible for subject-less findings).
	draft.ReadinessGaps = []Finding{{
		FindingID: "finding_readiness_gap1", Kind: "readiness", Summary: "Readiness is negative investigation-wide.",
		EvidenceRefIDs: []string{"evidence_release_1234"}, ClaimedFactIDs: []string{"claim_readiness_1"},
	}}
	if err := draft.ValidateAgainst(input); err != nil {
		t.Fatalf("ValidateAgainst() error = %v, want a subject-less finding to fall back to investigation-wide subjects", err)
	}
}

func TestSynthesisDraftValidateAgainstRejectsDriverSubjectLabelMismatch(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	forged := draft.Drivers[0].AffectedSubjects[0]
	forged.Label = "A Completely Different Project Name"
	draft.Drivers[0].AffectedSubjects = []SubjectRef{forged}
	err := draft.ValidateAgainst(input)
	if err == nil || !strings.Contains(err.Error(), "label") {
		t.Fatalf("ValidateAgainst() error = %v, want a label-mismatch error", err)
	}
}

func TestSynthesisDraftValidateAgainstRejectsClaimedFactSubjectLabelMismatch(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	draft.ClaimedFacts[0].Subject.Label = "A Completely Different Project Name"
	err := draft.ValidateAgainst(input)
	if err == nil || !strings.Contains(err.Error(), "label") {
		t.Fatalf("ValidateAgainst() error = %v, want a label-mismatch error", err)
	}
}

// --- H2 (Codex adversarial review): DirectJudgment/CurrentState are
// server-composed, not model-authored, so unvalidated prose can never
// contradict an already-validated claim. ---

func TestRuntimeAnswerSynthesizerComposesDirectJudgmentAndCurrentStateServerSide(t *testing.T) {
	t.Parallel()
	input, draft := closureFixture()
	draft.DirectJudgment = "Ask Dev is completely on track and release-ready, contradicting the validated claim"
	draft.CurrentState = "Everything is fine, nothing to see here, also contradicting the validated claim"
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
	}
	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if strings.Contains(result.DirectJudgment, "contradicting") || strings.Contains(result.CurrentState, "contradicting") {
		t.Fatalf("model prose leaked through: direct_judgment=%q current_state=%q", result.DirectJudgment, result.CurrentState)
	}
	if !strings.Contains(result.CurrentState, "readiness.release_ready=false") {
		t.Fatalf("CurrentState = %q, want it composed from the validated claim", result.CurrentState)
	}
	if result.DirectJudgment == "" {
		t.Fatal("DirectJudgment is empty")
	}
}

// --- M4 (Codex adversarial review): composed fields must self-truncate at
// their contract bound rather than let Validate() reject an oversized
// result (ErrInvalidResult -> 500) for a reason of ACR's own making. ---

func TestComposeDeterministicAnswerTruncatesAtContractBoundWithManyDrivers(t *testing.T) {
	t.Parallel()
	input := validSynthesisInputFixture()
	project := input.Graph.Resolution.Committed[0]
	draft := SynthesisDraft{
		Status: InvestigationComplete, DirectJudgment: "x", CurrentState: "x", StrongestPressures: []string{},
		RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Conflicts: []Finding{}, Limitations: []string{},
		EvidenceRefIDs: []string{}, DeterministicAnswer: "x", Warnings: []string{},
	}
	// Enough principal drivers with long titles to exceed 16000 runes if
	// nothing bounded the composition.
	for i := 0; i < 400; i++ {
		title := strings.Repeat("a very long driver title that keeps repeating to grow the composed answer ", 3)
		draft.Drivers = append(draft.Drivers, DriverJudgment{
			DriverID: fmt.Sprintf("driver_%08d", i), Standing: DriverPrincipal, Category: "relationship",
			Title: title, Summary: "summary", AffectedSubjects: []SubjectRef{project},
			PathIDs: []string{"path_12345678"}, Derivation: DerivationRuleInferred, EpistemicStatus: EpistemicInferred,
			Confidence: 0.9, Current: true,
		})
	}
	answer := composeDeterministicAnswer(draft)
	if len([]rune(answer)) > deterministicAnswerMaxLength {
		t.Fatalf("composeDeterministicAnswer() length = %d, want <= %d", len([]rune(answer)), deterministicAnswerMaxLength)
	}
	if !strings.Contains(answer, "truncated") {
		t.Fatalf("composeDeterministicAnswer() = %q, want an explicit truncation marker", answer)
	}
	// The synthesized field must itself pass the same bound
	// InvestigationResult.Validate() enforces -- proving this doesn't just
	// avoid the symptom but actually satisfies the real contract.
	if len(answer) == 0 || len([]rune(answer)) < 1 {
		t.Fatal("truncated answer must not be empty")
	}
}

func TestTruncateAtSentenceBoundaryLeavesShortTextUnchanged(t *testing.T) {
	t.Parallel()
	short := "This is a short sentence."
	if got := truncateAtSentenceBoundary(short, 8000); got != short {
		t.Fatalf("truncateAtSentenceBoundary() = %q, want unchanged", got)
	}
}

// --- M4 exact-boundary tests (Codex delta review, CHAOS-3755) ---
//
// The generic truncation test above proves truncation kicks in eventually.
// These prove it kicks in at exactly the right rune count for
// DirectJudgment and CurrentState specifically (8000, per
// ContextFabricInvestigationResult.Validate()'s
// stringLengthBetween(_, 0, 8000) bound) -- not one rune early (which
// would silently drop content that should have fit) and not one rune late
// (which would let an oversized field back into a persisted result).

func TestTruncateAtSentenceBoundaryAtDirectJudgmentExactBoundaryIsUnchanged(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("a", directJudgmentMaxLength)
	got := truncateAtSentenceBoundary(text, directJudgmentMaxLength)
	if got != text {
		t.Fatalf("truncateAtSentenceBoundary() at exactly %d runes was altered (got %d runes), want it left unchanged", directJudgmentMaxLength, len([]rune(got)))
	}
	if strings.Contains(got, "truncated") {
		t.Fatal("text exactly at the boundary must not carry a truncation marker")
	}
}

func TestTruncateAtSentenceBoundaryOneRuneOverDirectJudgmentBoundaryIsTruncated(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("a", directJudgmentMaxLength+1)
	got := truncateAtSentenceBoundary(text, directJudgmentMaxLength)
	if length := len([]rune(got)); length > directJudgmentMaxLength {
		t.Fatalf("truncateAtSentenceBoundary() length = %d, want <= %d for input one rune over the boundary", length, directJudgmentMaxLength)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatal("text one rune over the boundary must carry an explicit truncation marker")
	}
}

func TestTruncateAtSentenceBoundaryAtCurrentStateExactBoundaryIsUnchanged(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("a", currentStateMaxLength)
	got := truncateAtSentenceBoundary(text, currentStateMaxLength)
	if got != text {
		t.Fatalf("truncateAtSentenceBoundary() at exactly %d runes was altered (got %d runes), want it left unchanged", currentStateMaxLength, len([]rune(got)))
	}
	if strings.Contains(got, "truncated") {
		t.Fatal("text exactly at the boundary must not carry a truncation marker")
	}
}

func TestTruncateAtSentenceBoundaryOneRuneOverCurrentStateBoundaryIsTruncated(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("a", currentStateMaxLength+1)
	got := truncateAtSentenceBoundary(text, currentStateMaxLength)
	if length := len([]rune(got)); length > currentStateMaxLength {
		t.Fatalf("truncateAtSentenceBoundary() length = %d, want <= %d for input one rune over the boundary", length, currentStateMaxLength)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatal("text one rune over the boundary must carry an explicit truncation marker")
	}
}

// TestComposeFieldConstantsMatchContractBounds ties the composer constants
// to the actual Go-level contract bounds
// (validate_context_fabric_result.go's ContextFabricInvestigationResult.Validate)
// so a bound changing on one side without the other fails loudly here
// instead of silently drifting.
// TestErrInterpretationRejectedAndErrSynthesisRejectedAreDistinctFromEachOther
// is the CHAOS-3784 probe at the runtime layer: before this change, both an
// interpretation-side rejection and a genuinely different synthesis-side
// rejection classified identically (errors.Is(err, ErrModelOutput) alone),
// giving a caller no way to tell the two failure classes apart. This pins
// that the two are now mutually exclusive.
func TestErrInterpretationRejectedAndErrSynthesisRejectedAreDistinctFromEachOther(t *testing.T) {
	t.Parallel()
	interpreter := RuntimeQuestionInterpreter{
		Runtime: fakeModelRuntime{
			interpreted: InterpretedQuestion{
				Shape: ShapeOpen, RequestedJudgment: strings.Repeat("a", 259),
				TimeContext: TimeContext{Axis: TemporalCurrent},
			},
			receipt: validModelReceiptFixture(ModelOperationInterpret),
		},
		Sink: &fakeReceiptSink{},
	}
	_, interpretErr := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())

	input := validSynthesisInputFixture()
	draft := validSynthesisDraftFixture(input)
	draft.EvidenceRefIDs = []string{"evidence_invented_by_model"}
	synthesizer := RuntimeAnswerSynthesizer{
		Runtime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)},
		Sink:    &fakeReceiptSink{},
	}
	_, synthesizeErr := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	for _, err := range []error{interpretErr, synthesizeErr} {
		if !errors.Is(err, ErrModelOutput) {
			t.Fatalf("error = %v, want ErrModelOutput (both still classify as model-output-invalid)", err)
		}
	}
	if !errors.Is(interpretErr, ErrInterpretationRejected) || errors.Is(interpretErr, ErrSynthesisRejected) {
		t.Fatalf("interpretErr = %v, want ErrInterpretationRejected only", interpretErr)
	}
	if !errors.Is(synthesizeErr, ErrSynthesisRejected) || errors.Is(synthesizeErr, ErrInterpretationRejected) {
		t.Fatalf("synthesizeErr = %v, want ErrSynthesisRejected only", synthesizeErr)
	}
}

func TestComposeFieldConstantsMatchContractBounds(t *testing.T) {
	t.Parallel()
	if directJudgmentMaxLength != 8000 {
		t.Fatalf("directJudgmentMaxLength = %d, want 8000 to match ContextFabricInvestigationResult.Validate()'s DirectJudgment bound", directJudgmentMaxLength)
	}
	if currentStateMaxLength != 8000 {
		t.Fatalf("currentStateMaxLength = %d, want 8000 to match ContextFabricInvestigationResult.Validate()'s CurrentState bound", currentStateMaxLength)
	}
	if deterministicAnswerMaxLength != 16000 {
		t.Fatalf("deterministicAnswerMaxLength = %d, want 16000 to match ContextFabricInvestigationResult.Validate()'s DeterministicAnswer bound", deterministicAnswerMaxLength)
	}
}
