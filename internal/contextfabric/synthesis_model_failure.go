package contextfabric

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// SynthesisFailureClass is the closed vocabulary of model call failures that
// are served as a degraded answer.
type SynthesisFailureClass string

const (
	// SynthesisFailureModelOutputInvalid: the provider's output could not be
	// used and the runtime does not retry it.
	SynthesisFailureModelOutputInvalid SynthesisFailureClass = "model_output_invalid"
	// SynthesisFailureRejected: every draft the runtime was allowed to ask
	// for failed ACR's own bounds.
	SynthesisFailureRejected SynthesisFailureClass = "synthesis_rejected"
	// SynthesisFailureReceiptUnrecorded: the model call ran, but its receipt
	// was not recorded, so no text from it is served.
	SynthesisFailureReceiptUnrecorded SynthesisFailureClass = "model_receipt_unrecorded"
)

// synthesisFailureWarning is the fixed, content-safe sentence a degraded
// answer carries instead of model prose. It names the class only.
func synthesisFailureWarning(class SynthesisFailureClass) string {
	return "answer text unavailable: the model call failed (class: " + string(class) + "); the facts below were read and are served without model prose"
}

// synthesisFailureDegradeClass says whether a synthesis error is served as a
// degraded answer, and under which class.
//
// Served: a failure the runtime will not retry (invalid output, a draft that
// fails ACR's bounds after the runtime's own re-draw budget) and an
// unrecorded receipt. Not served: a caller that is gone, a transient class
// whose retry signal is the error itself (rate limit, unavailable, deadline),
// and an input that no bounding fits. When the call itself also failed with
// one of those, that failure names the request, so the error stands.
func synthesisFailureDegradeClass(ctx context.Context, err error) (SynthesisFailureClass, bool) {
	if err == nil || ctx.Err() != nil {
		return "", false
	}
	var overflow *ModelInputOverflow
	for _, kept := range []error{
		context.Canceled, context.DeadlineExceeded, ErrModelCancelled, ErrModelRateLimited, ErrModelUnavailable,
		ErrRateLimited, ErrUnavailable, ErrModelInputTooLarge,
	} {
		if errors.Is(err, kept) {
			return "", false
		}
	}
	if errors.As(err, &overflow) {
		return "", false
	}
	switch {
	case errors.Is(err, ErrSynthesisRejected):
		return SynthesisFailureRejected, true
	case errors.Is(err, ErrModelOutput):
		return SynthesisFailureModelOutputInvalid, true
	case errors.Is(err, ErrModelReceiptUnrecorded):
		return SynthesisFailureReceiptUnrecorded, true
	}
	return "", false
}

// degradedSynthesisDraft is the draft a failed model call is served with: no
// model-authored field, a degraded status, and the fixed warning.
func degradedSynthesisDraft(class SynthesisFailureClass) SynthesisDraft {
	return SynthesisDraft{
		Status:             InvestigationDegraded,
		StrongestPressures: []string{},
		Drivers:            []DriverJudgment{},
		RemainingWork:      []Finding{},
		ReadinessGaps:      []Finding{},
		Conflicts:          []Finding{},
		Limitations:        make([]string, 0),
		EvidenceRefIDs:     []string{},
		ClaimedFacts:       []ClaimedFact{},
		Warnings:           []string{synthesisFailureWarning(class)},
	}
}

// SynthesisFailure is the error a synthesis call ends with when the failure
// is one the engine serves as a degraded answer. It reads, wraps and
// classifies exactly as the cause it wraps.
type SynthesisFailure struct {
	Class    SynthesisFailureClass
	Attempts int
	Elapsed  time.Duration
	Receipt  ModelExecutionReceipt
	cause    error
}

func (f *SynthesisFailure) Error() string { return f.cause.Error() }
func (f *SynthesisFailure) Unwrap() error { return f.cause }

// DegradedSynthesizer is a Synthesizer that can compose the served answer for
// a failed call without calling the model again.
type DegradedSynthesizer interface {
	ComposeDegraded(ctx context.Context, principal storage.Principal, input SynthesisInput, failure *SynthesisFailure) (InvestigationResult, error)
}

// ComposeDegraded composes the answer a failed call is served with: the facts,
// paths and coverage of the input, no model-authored content, a degraded
// status and the fixed warning.
func (r RuntimeAnswerSynthesizer) ComposeDegraded(ctx context.Context, principal storage.Principal, input SynthesisInput, failure *SynthesisFailure) (InvestigationResult, error) {
	r.recordSynthesisModelFailure(ctx, principal, SynthesisModelFailureEvent{Class: failure.Class, Attempts: failure.Attempts, ElapsedMS: failure.Elapsed.Milliseconds()})
	return r.composeSynthesisResult(ctx, principal, input, degradedSynthesisDraft(failure.Class), failure.Receipt, false, true)
}

// synthesisVersionOf is the receipt's prompt and model version pair. A call
// that failed before it named either has no version to report.
func synthesisVersionOf(receipt ModelExecutionReceipt) string {
	if receipt.PromptVersion == "" || receipt.ModelVersion == "" {
		return "unwired"
	}
	return receipt.PromptVersion + "+" + receipt.ModelVersion
}

const synthesisModelFailureMessage = "context fabric synthesis model call failed, degraded answer served"

// SynthesisModelFailureEvent reports one model call failure that was served
// as a degraded answer. A closed class and counts only.
type SynthesisModelFailureEvent struct {
	Class     SynthesisFailureClass
	Attempts  int
	ElapsedMS int64
}

func (r RuntimeAnswerSynthesizer) recordSynthesisModelFailure(ctx context.Context, principal storage.Principal, event SynthesisModelFailureEvent) {
	if r.Telemetry != nil {
		r.Telemetry.RecordSynthesisModelFailure(ctx, principal, event)
		return
	}
	NewSlogEngineTelemetry(slog.Default()).RecordSynthesisModelFailure(ctx, principal, event)
}
