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

// synthesisFailureCurrentState replaces the "no canonical facts were observed"
// sentence an empty claim list would give: facts were read, and the model call
// that would have claimed them failed.
const synthesisFailureCurrentState = "The model call that writes the answer failed, so no current state is written here; the sources that were read are listed in coverage."

// synthesisFailureWarning is the fixed, content-safe sentence a degraded
// answer carries instead of model prose. It names the class only.
func synthesisFailureWarning(class SynthesisFailureClass) string {
	return "answer text unavailable: the model call failed (class: " + string(class) + "); the sources that were read are listed in coverage"
}

// synthesisFailureDegradeClass says whether a synthesis failure is served as a
// degraded answer, and under which class. callErr is what the model call (and
// the draft's validation) returned; sinkErr is what recording its receipt
// returned. They are kept apart because a receipt store's own failure wraps
// whatever it hit (an unavailable database, a deadline) and must not be read
// as the model call's class.
//
// Served: a call failure the runtime will not retry (invalid output, a draft
// that fails ACR's bounds after the runtime's own re-draw budget), and a
// receipt that was not recorded after a call that itself succeeded. Not
// served: a caller that is gone, a transient call class whose retry signal is
// the error itself (rate limit, unavailable, deadline), and an input that no
// bounding fits. When the call itself failed with one of those, that failure
// names the request, so the error stands whatever the sink did.
func synthesisFailureDegradeClass(ctx context.Context, callErr, sinkErr error) (SynthesisFailureClass, bool) {
	if ctx.Err() != nil {
		return "", false
	}
	if callErr == nil {
		if sinkErr != nil && errors.Is(sinkErr, ErrModelReceiptUnrecorded) {
			return SynthesisFailureReceiptUnrecorded, true
		}
		return "", false
	}
	var overflow *ModelInputOverflow
	for _, kept := range []error{
		context.Canceled, context.DeadlineExceeded, ErrModelCancelled, ErrModelRateLimited, ErrModelUnavailable, ErrModelInputTooLarge,
	} {
		if errors.Is(callErr, kept) {
			return "", false
		}
	}
	if errors.As(callErr, &overflow) {
		return "", false
	}
	switch {
	case errors.Is(callErr, ErrSynthesisRejected):
		return SynthesisFailureRejected, true
	case errors.Is(callErr, ErrModelOutput):
		return SynthesisFailureModelOutputInvalid, true
	}
	return "", false
}

// IsSynthesisModelFailureAnswer reports whether a result is the degraded
// answer served for a failed model call. Such an answer says nothing about
// the question that a recovered model would not say better, so it is never a
// reuse source.
func IsSynthesisModelFailureAnswer(result InvestigationResult) bool {
	if result.Status != InvestigationDegraded {
		return false
	}
	for _, class := range []SynthesisFailureClass{SynthesisFailureModelOutputInvalid, SynthesisFailureRejected, SynthesisFailureReceiptUnrecorded} {
		for _, warning := range result.Warnings {
			if warning == synthesisFailureWarning(class) {
				return true
			}
		}
	}
	return false
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
	// InputBounded: the call that failed was placed with a reduced fact set.
	InputBounded bool
	cause        error
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
	return r.composeSynthesisResult(ctx, principal, input, degradedSynthesisDraft(failure.Class), failure.Receipt, failure.InputBounded, true)
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
