package contextfabric

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
		"Drivers": result.Drivers == nil, "RemainingWork": result.RemainingWork == nil, "ReadinessGaps": result.ReadinessGaps == nil,
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
}

// --- fixtures ---

func validSynthesisInputFixture() SynthesisInput {
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	workItem := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_1", Label: "Release acceptance"}
	path := RelationshipPath{
		PathID: "path_12345678", Nodes: []SubjectRef{project, workItem},
		Edges: []RelationshipEdge{{
			Type: "REQUIRES", From: project, To: workItem,
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
			Category: "release_readiness", Title: "Release acceptance remains open",
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
