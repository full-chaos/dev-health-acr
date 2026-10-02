package main

import (
	"context"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// replayRuntime is a contextfabric.ModelRuntime whose "model call" returns
// one saved raw output. It mirrors the file-exchange transport
// (internal/runtime/hosted/file_exchange_runtime_test.go
// fileExchangeRuntime.InterpretQuestion): ParseInterpretationOutputSignals,
// then ApplyInterpretationCapture, then "pending_validation" so that
// RuntimeQuestionInterpreter runs its own validation exactly as it does for
// the genkit runtime. No network, no model client.
type replayRuntime struct {
	raw    []byte
	prompt string
}

// replayClock is fixed so receipts are deterministic; receipts are never
// persisted anywhere.
var replayClock = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

func (r replayRuntime) InterpretQuestion(_ context.Context, _ storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	receipt := contextfabric.ModelExecutionReceipt{
		Operation: contextfabric.ModelOperationInterpret, Provider: "offline-replay", Model: "saved-output", ModelVersion: "n/a",
		PromptVersion: genkitruntime.DefaultInterpretationPromptVersion, SchemaVersion: "v1", EvaluatorVersion: "intent-training-helper",
		StartedAt: replayClock, CompletedAt: replayClock, Attempts: 1,
		InputDigest: contextfabric.DigestModelValue([]byte(r.prompt)),
	}
	interpreted, capture, err := genkitruntime.ParseInterpretationOutputSignals(r.raw, request.TimeContext)
	if err != nil {
		receipt.Outcome = "invalid_output"
		return contextfabric.InterpretedQuestion{}, receipt, fmt.Errorf("%w: %v", contextfabric.ErrModelOutput, err)
	}
	receipt.OutputDigest = contextfabric.DigestModelValue(r.raw)
	genkitruntime.ApplyInterpretationCapture(&receipt, capture)
	receipt.Outcome = "pending_validation"
	return interpreted, receipt, nil
}

func (replayRuntime) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, fmt.Errorf("%w: synthesis is not replayed", contextfabric.ErrModelUnavailable)
}

// captureSink keeps the final receipt RuntimeQuestionInterpreter records:
// the validated or repaired frame, the frame outcome, the gate and the
// repaired judgment all live there.
type captureSink struct {
	receipts []contextfabric.ModelExecutionReceipt
}

func (s *captureSink) RecordModelExecution(_ context.Context, _ storage.Principal, receipt contextfabric.ModelExecutionReceipt) error {
	s.receipts = append(s.receipts, receipt)
	return nil
}

// captureFrameTelemetry keeps the frame validation event, which is the only
// place the failed phase, failure detail and repair decision are exposed.
type captureFrameTelemetry struct {
	events []contextfabric.FrameValidationEvent
}

func (c *captureFrameTelemetry) RecordFrameValidation(_ context.Context, _ storage.Principal, event contextfabric.FrameValidationEvent) {
	c.events = append(c.events, event)
}

type replayResult struct {
	Interpreted contextfabric.InterpretedQuestion
	Outcome     contextfabric.QuestionFamilyOutcome
	Err         error
	Receipt     *contextfabric.ModelExecutionReceipt
	FrameEvent  *contextfabric.FrameValidationEvent
	// The ModelRuntime call itself, before RuntimeQuestionInterpreter's
	// own validation and frame resolution.
	DirectCalled      bool
	DirectInterpreted contextfabric.InterpretedQuestion
	DirectErr         error
}

// recordingRuntime keeps the direct result of the wrapped ModelRuntime's
// InterpretQuestion call.
type recordingRuntime struct {
	inner       contextfabric.ModelRuntime
	called      bool
	interpreted contextfabric.InterpretedQuestion
	err         error
}

func (r *recordingRuntime) InterpretQuestion(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	interpreted, receipt, err := r.inner.InterpretQuestion(ctx, principal, request)
	r.called, r.interpreted, r.err = true, interpreted, err
	return interpreted, receipt, err
}

func (r *recordingRuntime) SynthesizeAnswer(ctx context.Context, principal storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return r.inner.SynthesizeAnswer(ctx, principal, input)
}

// replayGenkit replays one saved model answer through the production
// genkitruntime.Runtime bound to the in-process recorder model, then
// through RuntimeQuestionInterpreter. The harness lock is held for the
// whole call because the recorder's answer is shared state.
func replayGenkit(raw string, request contextfabric.InvestigationRequest) (replayResult, error) {
	harness, err := sharedHarness()
	if err != nil {
		return replayResult{}, err
	}
	harness.mu.Lock()
	defer harness.mu.Unlock()
	harness.begin(raw)
	result := replayInterpretation(harness.runtime, request)
	if request.Validate() == nil && harness.calls != 1 {
		return replayResult{}, fmt.Errorf("genkit replay called the recorder %d times, want exactly 1", harness.calls)
	}
	return result, nil
}

// replayInterpretation runs the production consumer of one interpretation:
// RuntimeQuestionInterpreter.Interpret at N=1 (the production default when
// ACR_CONTEXT_FABRIC_INTERPRETATION_ENSEMBLE_SIZE is unset). It is wired
// without a RequirementDeriver, which needs the live fact registry, so
// requirement rows are not derived here.
func replayInterpretation(runtime contextfabric.ModelRuntime, request contextfabric.InvestigationRequest) replayResult {
	sink := &captureSink{}
	frames := &captureFrameTelemetry{}
	recorder := &recordingRuntime{inner: runtime}
	interpreter := contextfabric.RuntimeQuestionInterpreter{
		Runtime:        recorder,
		Sink:           sink,
		FrameTelemetry: frames,
	}
	principal := storage.Principal{OrgID: "offline-helper", Subject: "offline-helper"}
	interpreted, outcome, err := interpreter.Interpret(context.Background(), principal, request)
	result := replayResult{Interpreted: interpreted, Outcome: outcome, Err: err,
		DirectCalled: recorder.called, DirectInterpreted: recorder.interpreted, DirectErr: recorder.err}
	if len(sink.receipts) > 0 {
		last := sink.receipts[len(sink.receipts)-1]
		result.Receipt = &last
	}
	if len(frames.events) > 0 {
		last := frames.events[len(frames.events)-1]
		result.FrameEvent = &last
	}
	return result
}
