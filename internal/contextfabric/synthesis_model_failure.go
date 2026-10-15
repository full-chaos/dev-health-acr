package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// SynthesisFailureClass is the closed vocabulary of model call failures that
// are served as a degraded answer.
type SynthesisFailureClass = contractsv1.ContextFabricSynthesisFailureClass

const (
	// SynthesisFailureModelOutputInvalid: the provider's output could not be
	// used and the runtime does not retry it.
	SynthesisFailureModelOutputInvalid = contractsv1.ContextFabricSynthesisFailureModelOutputInvalid
	// SynthesisFailureRejected: every draft the runtime was allowed to ask
	// for failed ACR's own bounds.
	SynthesisFailureRejected = contractsv1.ContextFabricSynthesisFailureRejected
	// SynthesisFailureReceiptUnrecorded: the model call ran, but its receipt
	// was not recorded, so no text from it is served.
	SynthesisFailureReceiptUnrecorded = contractsv1.ContextFabricSynthesisFailureReceiptUnrecorded
	// SynthesisFailureCallNotCompleted: the call ended on a transient class
	// (timeout, cancel, rate limit, unavailable) after the runtime's retries.
	SynthesisFailureCallNotCompleted = contractsv1.ContextFabricSynthesisFailureCallNotCompleted
)

// synthesisModelFailureSource is the coverage source the model-failure detail
// is filed under: the synthesis step, not a read.
const synthesisModelFailureSource = "context-fabric:synthesis"

// newSynthesisModelFailureDetail is the one producer of the coverage detail a
// failed model call is served with. A class without a retry hint is an error.
func newSynthesisModelFailureDetail(class SynthesisFailureClass) (CoverageDetail, error) {
	hint, err := contractsv1.ContextFabricRetryHintFor(class)
	if err != nil {
		return CoverageDetail{}, err
	}
	code := contractsv1.ContextFabricCoverageDetailSynthesisModelFailed
	if _, notRun := contractsv1.ContextFabricSynthesisFactsOnlyLimitationFor(class); notRun {
		code = contractsv1.ContextFabricCoverageDetailSynthesisNotRun
	}
	detail := CoverageDetail{
		Source: synthesisModelFailureSource, Code: code,
		FailureClass: class, RetryHint: hint,
		Raw: string(code) + ":" + string(class),
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	return detail, nil
}

// synthesisFailureCurrentState replaces the "no canonical facts were observed"
// sentence an empty claim list would give: facts were read, and the model call
// that would have claimed them failed.
const synthesisFailureCurrentState = "The model call that writes the answer failed, so no current state is written here; the sources that were read are listed in coverage."

// synthesisFailureWarning is the fixed, content-safe sentence a degraded
// answer carries instead of model prose. It names the class only.
func synthesisFailureWarning(class SynthesisFailureClass) string {
	switch class {
	case contractsv1.ContextFabricSynthesisFailureDeadline:
		return "answer text unavailable: facts only; no second synthesis was started because the time budget was spent (class: " + string(class) + "); the sources that were read are listed in coverage"
	case contractsv1.ContextFabricSynthesisFailureNotNarrowable:
		return "answer text unavailable: facts only; the written answer was over the response budget and the cohort could not be narrowed further (class: " + string(class) + "); the sources that were read are listed in coverage"
	}
	return "answer text unavailable: the model call failed (class: " + string(class) + "); the sources that were read are listed in coverage"
}

// SynthesisCompositionReserveSeconds is the slice of the request deadline the
// model call never uses: the degraded answer is composed and stored inside
// it. A call still running when only this slice is left is ended, and the
// failure that follows is served as a degraded answer, never as a timeout
// with no answer. A dead context composes nothing, so the slice is held back
// before the deadline, not after it.
const SynthesisCompositionReserveSeconds = contractsv1.ContextFabricSynthesisCompositionReserveSeconds

const synthesisCompositionReserve = SynthesisCompositionReserveSeconds * time.Second

// ValidateSynthesisCompositionReserve fails when the request timeout cannot
// hold the reserve: a reserve at or above the request timeout would end every
// model call before it began.
func ValidateSynthesisCompositionReserve(requestTimeout time.Duration) error {
	if requestTimeout <= synthesisCompositionReserve {
		return fmt.Errorf("request timeout %s must exceed the synthesis composition reserve of %ds", requestTimeout, SynthesisCompositionReserveSeconds)
	}
	return nil
}

// boundSynthesisCallContext returns the context the model call runs under:
// the request context ended synthesisCompositionReserve before its deadline.
// A context with no deadline is returned as it is. applied reports whether a
// narrower deadline was set.
func boundSynthesisCallContext(ctx context.Context) (context.Context, context.CancelFunc, bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return ctx, func() {}, false
	}
	callCtx, cancel := context.WithDeadline(ctx, deadline.Add(-synthesisCompositionReserve))
	return callCtx, cancel, true
}

// synthesisFailureDegradeClass says whether a synthesis failure is served as a
// degraded answer, and under which class. callErr is what the model call (and
// the draft's validation) returned; sinkErr is what recording its receipt
// returned. They are kept apart because a receipt store's own failure wraps
// whatever it hit (an unavailable database, a deadline) and must not be read
// as the model call's class.
//
// ctx is the REQUEST context, not the call's: a call that ended at its own
// deadline leaves the request context alive, and that is what is served.
//
// Served: a call failure the runtime will not retry (invalid output, a draft
// that fails ACR's bounds after the runtime's own re-draw budget), a transient
// call class once the runtime says its retries are used (ErrModelRetriesExhausted)
// or the reserve ended the call (timeout, cancel, rate limit, unavailable), and a receipt that was not recorded after a call that itself
// succeeded. Not served: a caller that is gone, and an input that no bounding
// fits.
func synthesisFailureDegradeClass(ctx context.Context, callErr, sinkErr error, reserveFired bool) (SynthesisFailureClass, bool) {
	if ctx.Err() != nil {
		return "", false
	}
	if callErr == nil {
		if sinkErr != nil && errors.Is(sinkErr, ErrModelReceiptUnrecorded) {
			return SynthesisFailureReceiptUnrecorded, true
		}
		return "", false
	}
	if errors.Is(callErr, ErrModelInputTooLarge) {
		return "", false
	}
	for _, transient := range []error{
		context.Canceled, context.DeadlineExceeded, ErrModelCancelled, ErrModelRateLimited, ErrModelUnavailable,
	} {
		if !errors.Is(callErr, transient) {
			continue
		}
		// A transient end is served only when it is final: the runtime used
		// its attempt budget, or the composition reserve ended the call. One
		// that ended earlier keeps its error and its retry signal.
		if reserveFired || errors.Is(callErr, ErrModelRetriesExhausted) {
			return SynthesisFailureCallNotCompleted, true
		}
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
// answer served for a failed model call: it carries the code detail or, when
// that detail could not be filed, the fixed failure warning. Such an answer says nothing about
// the question that a recovered model would not say better, so it is never a
// reuse source.
func IsSynthesisModelFailureAnswer(result InvestigationResult) bool {
	if result.Status != InvestigationDegraded {
		return false
	}
	for _, detail := range result.Coverage.Details {
		if detail.Code == contractsv1.ContextFabricCoverageDetailSynthesisModelFailed || detail.Code == contractsv1.ContextFabricCoverageDetailSynthesisNotRun {
			return true
		}
	}
	// The code detail is not filed when the coverage it would sit beside does
	// not pair with degraded_reasons. The fixed warning names the failure class
	// the same way, so such an answer is still never a reuse source.
	for _, class := range contractsv1.ContextFabricSynthesisFailureClasses() {
		if slices.Contains(result.Warnings, synthesisFailureWarning(class)) {
			return true
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
	// Bound is set when the call that failed was placed with a reduced fact
	// set: the disclosure the degraded answer carries.
	Bound *synthesisInputBound
	// Rule and Stage name a work-item tuple rule breach that was served as this
	// failure, and the stage that served it (synthesis or validation).
	Rule  string
	Stage string
	// DeferEvent leaves the model-failure event to the caller, which records it
	// only once the degraded answer is actually served.
	DeferEvent bool
	// ReserveFired is set when the call was ended by the composition reserve
	// and not by its own timeout or the provider: the degraded answer says so
	// in the log.
	ReserveFired bool
	// CallDeadline is the deadline the model call ran under when the reserve
	// bounded it.
	CallDeadline time.Time
	cause        error
	// retryRan is set on a facts-only answer served after a narrower retry ran and did not fit: its facts are the first
	// pass's, and the answer says a retry ran.
	retryRan bool
	// withheldScoreKinds are the fact kinds whose declared scores the degraded composition did not claim; the composition
	// files one fact_score_withheld detail for each.
	withheldScoreKinds []FactKind
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
	if !failure.DeferEvent {
		r.recordSynthesisModelFailure(ctx, principal, SynthesisModelFailureEvent{Class: failure.Class, Attempts: failure.Attempts, ElapsedMS: failure.Elapsed.Milliseconds(), Rule: failure.Rule, Stage: failure.Stage, ReserveFired: failure.ReserveFired, CallDeadline: failure.CallDeadline})
	}
	draft := degradedSynthesisDraft(failure.Class)
	claims, evidence, truncated, withheld := mintDegradedFactClaimsWithholding(input.Facts.Facts, degradedClaimLimit(input), scoreLookup(r.FactCapabilities))
	r.recordWithheldScores(ctx, principal, failure.Class, withheld)
	failure.withheldScoreKinds = withheldKinds(withheld)
	if limitation, factsOnlyClass := contractsv1.ContextFabricSynthesisFactsOnlyLimitationFor(failure.Class); factsOnlyClass {
		draft.ClaimedFacts, draft.EvidenceRefIDs = claims, evidence
		if len(claims) == 0 {
			draft.ClaimedFacts, draft.EvidenceRefIDs = []ClaimedFact{}, []string{}
		}
		additions := []string{limitation}
		if failure.retryRan {
			additions = append(additions, contractsv1.ContextFabricSynthesisRetryRanLimitation)
		}
		draft.Limitations, _ = appendBoundedLimitations(draft.Limitations, additions)
	} else if len(claims) > 0 {
		draft.ClaimedFacts = claims
		draft.EvidenceRefIDs = evidence
		additions := []string{contractsv1.ContextFabricSynthesisFailedFactsOnlyLimitation}
		if truncated {
			additions = append(additions, contractsv1.ContextFabricSynthesisFailedFactsTruncatedLimitation)
		}
		draft.Limitations, _ = appendBoundedLimitations(draft.Limitations, additions)
	} else {
		draft.Limitations, _ = appendBoundedLimitations(draft.Limitations, []string{contractsv1.ContextFabricSynthesisFailedNoFactsLimitation})
	}
	return r.composeSynthesisResult(ctx, principal, input, draft, failure.Receipt, failure.Bound, failure)
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
	// Rule and Stage are set only for a work-item tuple rule breach served as
	// this failure: the closed rule token and where it was caught.
	Rule  string
	Stage string
	// ReserveFired is set when the composition reserve ended the call;
	// CallDeadline is then the deadline the call ran under.
	ReserveFired bool
	CallDeadline time.Time
}

func (r RuntimeAnswerSynthesizer) recordSynthesisModelFailure(ctx context.Context, principal storage.Principal, event SynthesisModelFailureEvent) {
	if r.Telemetry != nil {
		r.Telemetry.RecordSynthesisModelFailure(ctx, principal, event)
		return
	}
	NewSlogEngineTelemetry(slog.Default()).RecordSynthesisModelFailure(ctx, principal, event)
}

// recordWithheldScores logs the score fields a degraded answer did not claim and the drivers table each one needed, at
// Info: the response names the fact kinds (a coverage detail), the trace names the fields and tables.
func (r RuntimeAnswerSynthesizer) recordWithheldScores(ctx context.Context, principal storage.Principal, class SynthesisFailureClass, withheld []withheldScore) {
	if len(withheld) == 0 {
		return
	}
	attrs := []any{"org_id", SanitizeLogAttr(principal.OrgID), "class", SanitizeLogAttr(string(class)), "withheld", len(withheld), "reason", "drivers_not_served"}
	for index, item := range withheld {
		if index >= 8 {
			break
		}
		attrs = append(attrs, "field_"+strconv.Itoa(index), SanitizeLogAttr(string(item.Kind)+"."+item.Field+"@"+string(item.SubjectKind)), "drivers_table_"+strconv.Itoa(index), SanitizeLogAttr(item.DriversTable))
	}
	slog.Default().InfoContext(ctx, "context fabric degraded answer withheld scores without their drivers", attrs...)
}

func withheldKinds(withheld []withheldScore) []FactKind {
	kinds := []FactKind{}
	for _, item := range withheld {
		if !slices.Contains(kinds, item.Kind) {
			kinds = append(kinds, item.Kind)
		}
	}
	return kinds
}

// newFactScoreWithheldDetail is the coverage detail of a fact kind whose declared scores a degraded answer did not
// claim. A disclosure: the answer's status already says degraded.
func newFactScoreWithheldDetail(kind FactKind) CoverageDetail {
	detail := CoverageDetail{
		Source: synthesisModelFailureSource, Code: contractsv1.ContextFabricCoverageDetailFactScoreWithheld, FactKind: kind,
		Raw: string(contractsv1.ContextFabricCoverageDetailFactScoreWithheld) + ":" + string(kind),
	}
	detail.Label = contractsv1.ComposeCoverageDetailLabel(detail)
	return detail
}
