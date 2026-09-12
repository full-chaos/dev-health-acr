package contextfabric

import (
	"context"
	"fmt"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-5421 -- the interpreted time bound.
//
// resolveTimeContext (temporal.go) is the engine's definition of what it
// can honestly answer, and Investigate calls it at TWO sites for two
// different actors: once on request.TimeContext, which is the CALLER's own
// assertion, and once on interpretation.TimeContext, which is the
// INTERPRETER's own output. Both sites returned the same ErrInvalidTimeBound,
// so internal/api's route classifier could not tell them apart and wrote an
// engine-side interpreter defect back to the caller as
// `400 invalid_request / "ACR rejected the investigation request"` -- on a
// request whose entire content was a question. The post-Interpret site also
// returned its error unwrapped, so FailureStage reported "unknown".
//
// The design of record already said what this site should do.
// docs/design/context-fabric-historical-time-axis.md §1, on the two check
// points, verbatim: "Its doc comment's reasoning is unchanged; only the
// verdict changes from 'refuse' to 'bind the as-of and label it'." And its
// rule 1: "Tolerance: `+1m` for clock skew, then clamp to `now`."
//
// So this file is the post-Interpret verdict, and only that. The
// wire-request site keeps resolveTimeContext and keeps its 400 byte for
// byte: a caller who asks for a window this service will not read is still
// told so, and told it is their request that was refused.
//
// What the two sites now differ on is the ACTOR, which is the whole point:
//
//   - the caller asserted a bound     -> refuse the REQUEST (400, unchanged)
//   - the interpreter produced one    -> clamp it, or refuse the TURN with a
//     named basis the caller can read
//
// A model that reasons about a calendar period rather than reading this
// service's clock is not a malformed request, and a caller cannot fix it by
// asking differently.

// InterpretedTimeBoundOutcome is the closed vocabulary for what this
// engine decided about the time context its own interpreter produced.
//
// It is an ALLOW-LIST: every member below names one rule, and there is no
// "else" arm anywhere in this file. A deny-list would admit the next rule,
// and the zero value, by default.
//
// The membership is derived from resolveTimeContext's own arms rather than
// invented: that function distinguishes seven refusal texts, and they group
// onto five refusal members. absent_or_zero_instant is the one member
// covering more than one text (the representability/zero guard, a
// point-in-time axis with no as-of, and a range missing an endpoint),
// because all three are the same fact about the world -- the interpreter did
// not supply a usable instant.
type InterpretedTimeBoundOutcome string

const (
	// InterpretedTimeBoundOK: the interpreted bound is answerable exactly
	// as produced. Nothing was clamped and nothing was refused.
	InterpretedTimeBoundOK InterpretedTimeBoundOutcome = "ok"
	// InterpretedTimeBoundFutureEnd: the interpreted bound reached past
	// now, and was pulled back to now. This is the design's own verdict
	// ("then clamp to `now`") and it is ANSWERABLE -- the turn proceeds and
	// the answer's label states the clamped instant, so it speaks for a
	// time that has happened.
	InterpretedTimeBoundFutureEnd InterpretedTimeBoundOutcome = "future_end"
	// InterpretedTimeBoundRangeTooWide: the range is wider than this
	// service reads, measured AFTER any clamp, since a clamp can only
	// narrow it.
	InterpretedTimeBoundRangeTooWide InterpretedTimeBoundOutcome = "range_too_wide"
	// InterpretedTimeBoundAbsentOrZero: an instant the axis requires is
	// missing, is the zero value (which is an assertion about year 1, not
	// an absence -- only nil is absence), or is outside the representable
	// range.
	InterpretedTimeBoundAbsentOrZero InterpretedTimeBoundOutcome = "absent_or_zero_instant"
	// InterpretedTimeBoundUnknownAxis: the interpreter named an axis
	// outside the closed temporal vocabulary. Reachable because
	// QuestionInterpreter is a PORT -- the shipped adapter validates its
	// own output, but the guarantee has to hold for any implementation.
	InterpretedTimeBoundUnknownAxis InterpretedTimeBoundOutcome = "unknown_axis"
	// InterpretedTimeBoundMalformedRange: the range's end precedes its
	// start. Distinct from every member above: it is not a future bound, it
	// is not too wide, both instants are present and representable, and the
	// axis is legal. Clamping cannot repair it.
	InterpretedTimeBoundMalformedRange InterpretedTimeBoundOutcome = "malformed_range"
)

// ValidInterpretedTimeBoundOutcome reports membership. Written as an
// allow-list switch naming every member, so adding a member without
// classifying it here fails to compile the exhaustiveness test rather than
// passing by default.
func ValidInterpretedTimeBoundOutcome(outcome InterpretedTimeBoundOutcome) bool {
	switch outcome {
	case InterpretedTimeBoundOK, InterpretedTimeBoundFutureEnd, InterpretedTimeBoundRangeTooWide,
		InterpretedTimeBoundAbsentOrZero, InterpretedTimeBoundUnknownAxis, InterpretedTimeBoundMalformedRange:
		return true
	default:
		return false
	}
}

// interpretedTimeBoundOutcomes is the vocabulary's backing array, in
// published order -- the same unexported-array-plus-accessor pattern the
// window vocabularies use.
var interpretedTimeBoundOutcomes = [...]InterpretedTimeBoundOutcome{
	InterpretedTimeBoundOK,
	InterpretedTimeBoundFutureEnd,
	InterpretedTimeBoundRangeTooWide,
	InterpretedTimeBoundAbsentOrZero,
	InterpretedTimeBoundUnknownAxis,
	InterpretedTimeBoundMalformedRange,
}

// InterpretedTimeBoundOutcomeCount is the closed vocabulary's size.
const InterpretedTimeBoundOutcomeCount = len(interpretedTimeBoundOutcomes)

// InterpretedTimeBoundOutcomeVocabulary returns the closed vocabulary in
// published order.
func InterpretedTimeBoundOutcomeVocabulary() [InterpretedTimeBoundOutcomeCount]InterpretedTimeBoundOutcome {
	return interpretedTimeBoundOutcomes
}

// InterpretedTimeBoundDecision is the whole verdict for one Investigate
// call, and the payload of the Info observable this change adds.
//
// Outcome and ClampApplied are INDEPENDENT arms, not a cross-product: a
// range that was clamped back to now and is STILL too wide reports
// outcome=range_too_wide with clamp_applied=true, because both are true and
// each is one fact. Collapsing them into a combined member would make the
// vocabulary 2^n and would mean a new arm changed an existing decision.
type InterpretedTimeBoundDecision struct {
	// Axis is the axis the interpreter named, echoed verbatim -- including
	// an axis outside the vocabulary, which is exactly the case an operator
	// needs to see.
	Axis TemporalAxis
	// Outcome is the closed-vocabulary verdict.
	Outcome InterpretedTimeBoundOutcome
	// ClampApplied reports whether any instant was pulled back to now. It
	// is always written, true or false, so a missing clamp and a clamp of
	// zero instants are never the same reading.
	ClampApplied bool
	// RangeDays is the width, in whole days, of the FINAL bound on the
	// range axis, and an explicit 0 on every other axis. Explicit rather
	// than omitted for the same reason: a reader must never have to guess
	// whether 0 means "not a range" or "we did not measure".
	RangeDays int
	// Bound is the context every layer below should bind to. It is
	// meaningful only when Answerable reports true; on a refusal it is the
	// interpreter's own unusable value, carried for no purpose but the
	// decision's own completeness.
	Bound TimeContext
}

// Answerable reports whether the turn proceeds. Exactly two members are
// answerable, and the split is the substance of this change: a clamped
// future bound is answered, and every other non-ok member refuses.
func (d InterpretedTimeBoundDecision) Answerable() bool {
	switch d.Outcome {
	case InterpretedTimeBoundOK, InterpretedTimeBoundFutureEnd:
		return true
	case InterpretedTimeBoundRangeTooWide, InterpretedTimeBoundAbsentOrZero,
		InterpretedTimeBoundUnknownAxis, InterpretedTimeBoundMalformedRange:
		return false
	default:
		// Not reachable through resolveInterpretedTimeContext, which only
		// ever produces a member. Fails CLOSED so a member added without
		// classifying it here refuses turns rather than serving them
		// silently.
		return false
	}
}

// interpretedTimeBoundLimitations is the caller-facing basis for each
// refusing member: one fixed, non-interpolated sentence naming what this
// service could not do, never a mechanism, provider, model or raw bound.
// Same convention as windowVetoLimitations.
//
// The answerable members deliberately have NO entry -- they produce no
// refusal, so a limitation for them would be a sentence nothing can emit.
var interpretedTimeBoundLimitations = map[InterpretedTimeBoundOutcome]string{
	InterpretedTimeBoundRangeTooWide:   "The time span this question was understood to cover is wider than this service reads, so no answer was produced for it. Asking about a narrower period will be answered.",
	InterpretedTimeBoundAbsentOrZero:   "The time this question was understood to be about could not be established, so no answer was produced for it. Naming the period explicitly will be answered.",
	InterpretedTimeBoundUnknownAxis:    "The kind of time this question was understood to be about is not one this service answers for, so no answer was produced for it.",
	InterpretedTimeBoundMalformedRange: "The time span this question was understood to cover ended before it began, so no answer was produced for it. Naming the period explicitly will be answered.",
}

// interpretedTimeBoundLimitation returns the basis for a refusing outcome.
// It refuses to invent one for an answerable member or an unknown value:
// ok=false, so a caller that reached here wrongly cannot publish an empty
// or misleading limitation.
func interpretedTimeBoundLimitation(outcome InterpretedTimeBoundOutcome) (string, bool) {
	limitation, ok := interpretedTimeBoundLimitations[outcome]
	return limitation, ok
}

// resolveInterpretedTimeContext is the post-Interpret verdict. It replaces
// the second resolveTimeContext call, and ONLY that one.
//
// Order is the same as resolveTimeContext's, so the two sites agree about
// which rule a value breaks; the difference is the verdict, not the
// classification:
//
//  1. representability and present-zero, over every supplied instant
//  2. the axis's own shape (a required instant present, the axis known)
//  3. range ordering
//  4. the future clamp -- REPLACES resolveTimeContext's tolerance-gated
//     clamp-or-refuse with an unconditional clamp
//  5. the width bound, measured on the CLAMPED range
//
// Steps 4 and 5 are in this order because a clamp can only narrow a range:
// checking width first would refuse a range that the clamp was about to
// bring inside the bound, which would be a refusal caused by the model's
// overshoot rather than by the question's span.
func resolveInterpretedTimeContext(timeContext TimeContext, now time.Time) InterpretedTimeBoundDecision {
	refuse := func(outcome InterpretedTimeBoundOutcome, clamped bool) InterpretedTimeBoundDecision {
		return InterpretedTimeBoundDecision{Axis: timeContext.Axis, Outcome: outcome, ClampApplied: clamped, Bound: timeContext}
	}
	// Step 1. A NON-NIL zero instant is an assertion about year 1, not an
	// absence -- only nil is absence, because a pointer already expresses
	// unset. This mirrors resolveTimeContext's R6-2 guard exactly.
	for _, instant := range []*time.Time{timeContext.AsOf, timeContext.Start, timeContext.End} {
		if instant == nil {
			continue
		}
		if instant.IsZero() || !contractsv1.RepresentableInstant(*instant) {
			return refuse(InterpretedTimeBoundAbsentOrZero, false)
		}
	}
	switch timeContext.Axis {
	case TemporalCurrent:
		// The current axis is always answerable and never clamped. Its
		// RangeDays is an explicit 0, not an omission.
		return InterpretedTimeBoundDecision{Axis: timeContext.Axis, Outcome: InterpretedTimeBoundOK, Bound: timeContext}
	case TemporalValidTime, TemporalObservedTime:
		if timeContext.AsOf == nil {
			return refuse(InterpretedTimeBoundAbsentOrZero, false)
		}
		if !timeContext.AsOf.After(now) {
			return InterpretedTimeBoundDecision{Axis: timeContext.Axis, Outcome: InterpretedTimeBoundOK, Bound: timeContext}
		}
		// Step 4 for a point in time. The instant the answer speaks for is
		// the clamped one, so the label reports a time that has happened.
		clamped := timeContext
		at := now
		clamped.AsOf = &at
		return InterpretedTimeBoundDecision{
			Axis: timeContext.Axis, Outcome: InterpretedTimeBoundFutureEnd, ClampApplied: true, Bound: clamped,
		}
	case TemporalRange:
		if timeContext.Start == nil || timeContext.End == nil {
			return refuse(InterpretedTimeBoundAbsentOrZero, false)
		}
		// Step 3, before the clamp: an inverted range is malformed however
		// the clamp would move its end, and pulling the end back could only
		// invert it further.
		if timeContext.End.Before(*timeContext.Start) {
			return refuse(InterpretedTimeBoundMalformedRange, false)
		}
		// Step 4 for a range.
		bound := timeContext
		clampApplied := false
		if timeContext.End.After(now) {
			end := now
			bound.End = &end
			clampApplied = true
			// A window whose whole span sits in the future would otherwise
			// invert once the end is pulled back -- the same guard
			// resolveTimeContext carries for its tolerance window.
			if timeContext.Start.After(now) {
				start := now
				bound.Start = &start
			}
		}
		// Step 5, on the clamped bound.
		rangeDays := int(bound.End.Sub(*bound.Start) / (24 * time.Hour))
		if bound.End.Sub(*bound.Start) > maxHistoricalRangeDays*24*time.Hour {
			decision := refuse(InterpretedTimeBoundRangeTooWide, clampApplied)
			decision.RangeDays = rangeDays
			decision.Bound = bound
			return decision
		}
		outcome := InterpretedTimeBoundOK
		if clampApplied {
			outcome = InterpretedTimeBoundFutureEnd
		}
		return InterpretedTimeBoundDecision{
			Axis: timeContext.Axis, Outcome: outcome, ClampApplied: clampApplied, RangeDays: rangeDays, Bound: bound,
		}
	default:
		return refuse(InterpretedTimeBoundUnknownAxis, false)
	}
}

// interpretedTimeBoundPlaceholderJudgment mirrors
// windowVetoPlaceholderJudgment: a refusing terminal carries no judgment,
// but the result contract requires the field to say what the turn was
// about.
const interpretedTimeBoundPlaceholderJudgment = "interpreted time bound"

// interpretedTimeBoundResult is the terminal a refusing member produces.
//
// It is a RESULT, not an error, and that is the substance of the fix: the
// caller reads a no_match carrying a Limitation that names the basis,
// instead of a 400 telling them their request was rejected. Modeled on
// windowVetoResult, which already establishes this shape for "this turn
// cannot proceed, and here is why".
//
// The persisted Interpretation carries the INTERPRETER's own time context
// wherever the contract can represent it, and falls back to the REQUEST's
// where it cannot.
//
// Which of the two applies is decided by RUNNING the contract's own
// validator, never by a second list of which members qualify -- a hand-kept
// list here would be a second authority on representability and would drift
// from the first.
//
// The distinction is real and it is not per-member cosmetics. An absent or
// zero instant, an axis the contract does not define and an inverted range
// are values Validate REFUSES, so persisting one would fail the result's own
// Validate and leave the refusal unreadable -- there the request's context is
// the only thing that can be carried. But a range this service will not READ
// is still perfectly representable: Validate owns shape and representability,
// not the maxHistoricalRangeDays bound. Dropping it left the persisted answer
// unable to say what span was refused, which is the one thing a reader of
// that refusal needs, and contradicted the time-axis design's statement that
// Interpretation.TimeContext round-trips {axis, as_of, start, end} in the
// result.
func (e *Engine) interpretedTimeBoundResult(
	ctx context.Context, principal storage.Principal, request InvestigationRequest,
	decision InterpretedTimeBoundDecision, binding ResolvedGraphBinding,
	// priorSubjectReceiptDispositions is composed against a ZERO resolution
	// by the caller: this exit returns before ResolveSubjects runs, so every
	// matched hint reads skipped_failed_reauth, the same "not re-verified
	// this call" convention every other never-resolved terminal uses.
	priorSubjectReceiptDispositions []contractsv1.ContextFabricPriorSubjectReceiptEntry,
	// plan is nil here by construction -- this exit precedes the planning
	// stage. Taken as a parameter rather than hardcoded so the signature
	// matches every sibling terminal and a later reordering cannot silently
	// drop a plan that by then exists.
	plan *AnswerPlan, ancestryParent string,
) (InvestigationResult, error) {
	limitation, ok := interpretedTimeBoundLimitation(decision.Outcome)
	if !ok {
		// Reached only if a caller routed an ANSWERABLE decision here,
		// which Investigate's own Answerable() branch makes impossible.
		// Fails closed and names the member rather than publishing a
		// refusal with no basis.
		return InvestigationResult{}, stageError(StageInterpretation,
			fmt.Errorf("interpreted time bound outcome %q has no stated basis", decision.Outcome))
	}
	emptyCoverage := Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}
	// The interpreter's own context when the wire contract can carry it,
	// the request's when it cannot. Decided by the validator itself.
	persistedTime := request.TimeContext
	if decision.Bound.Validate() == nil {
		persistedTime = decision.Bound
	}
	resolvedInterpretation := InterpretedQuestion{
		Shape:             ShapeOpen,
		RequestedJudgment: interpretedTimeBoundPlaceholderJudgment,
		TimeContext:       persistedTime,
		FactRequirements:  []FactRequirement{},
	}
	result := InvestigationResult{
		SchemaVersion:      InvestigationResultSchemaV1,
		ResultID:           e.newResultID(),
		RequestID:          request.RequestID,
		GeneratedAt:        e.now().UTC(),
		Status:             InvestigationNoMatch,
		Question:           request.Question,
		Reused:             false,
		Interpretation:     resolvedInterpretation,
		SubjectResolution:  SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, PriorSubjectReceiptDispositions: priorSubjectReceiptDispositions},
		DirectJudgment:     "",
		CurrentState:       "",
		StrongestPressures: []string{},
		Drivers:            []DriverJudgment{},
		RemainingWork:      []Finding{},
		ReadinessGaps:      []Finding{},
		Paths:              []RelationshipPath{},
		Conflicts:          []Finding{},
		Limitations:        []string{limitation},
		EvidenceRefIDs:     []string{},
		ClaimedFacts:       []ClaimedFact{},
		Coverage:           emptyCoverage,
		// Nil on the current axis; non-nil once a representable historical
		// context is carried, which the result contract REQUIRES for a
		// non-current axis. Composed rather than hardcoded so the rule
		// stays in one place.
		Temporal:            composeTemporalLabel(resolvedInterpretation, emptyCoverage, ""),
		Versions:            e.terminalVersions(),
		DeterministicAnswer: limitation,
		Warnings:            []string{},
	}
	// This is its own independent exit from Investigate, so it stamps
	// completeness, display labels and the budget itself, immediately
	// before its own Validate -- the placement rule every other terminal
	// exit follows.
	result.Completeness = ComputeAnswerCompleteness(result)
	if omitted := capCoverageEntriesToWriteBound(&result); omitted > 0 && e.telemetry != nil {
		e.telemetry.RecordCoverageEntriesCapped(ctx, principal, len(result.Coverage.Details), omitted)
	}
	if fallbacks := applyCoverageDisplayLabels(&result); fallbacks > 0 && e.telemetry != nil {
		e.telemetry.RecordEvidenceLabelFallback(ctx, principal, fallbacks)
	}
	result, err := e.finalizeServed(ctx, principal, BudgetAssertInterpretedTimeBound, result, plan, e.effectiveResponseBudget(request))
	if err != nil {
		return InvestigationResult{}, err
	}
	if err := result.Validate(); err != nil {
		return InvestigationResult{}, stageError(StageValidation, fmt.Errorf("%w: %w", ErrInvalidResult, err))
	}
	if e.results != nil {
		// Keyed on the REQUEST's own context even when the interpreter's is
		// the one persisted, exactly like windowVetoResult's pre-Interpret
		// branch: a refused span must never become a lookup key, and the
		// nil reuse snapshots below mean this row never becomes reusable
		// anyway.
		if err := e.results.Save(ctx, principal, result, nil, nil, TimeAxisKeyFor(request.TimeContext), e.reuseRetrievalIdentity, e.reusePromptVersions, e.reuseVersionAuthorities, binding.Epoch, ancestryParent); err != nil {
			return InvestigationResult{}, stageError(StagePersistence, fmt.Errorf("save investigation result: %w", err))
		}
	}
	return result, nil
}
