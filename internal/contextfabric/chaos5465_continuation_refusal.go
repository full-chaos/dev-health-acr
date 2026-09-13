package contextfabric

// The refusal behind a window-only continuation whose carrier could not be
// established.
//
// THE DECISION IT ACTS ON IS ALREADY TAKEN. admitWindowContinuation and the
// composition boundary decide `withheld`, and before this file the turn then
// went on exactly as it did before continuations existed: the fresh reading
// planned, retrieved and answered. That answer is a reading the caller never
// confirmed, served beside a window the caller confirmed for a DIFFERENT
// reading -- the substitution the continuation exists to stop, made silently
// whenever the carrier failed.
//
// So a withheld continuation now ends the turn above retrieval, with a
// no_match result that says so on both surfaces: the closed refusal basis
// `continuation_context_unverifiable` and its fixed sentence. The caller learns
// the one actionable fact -- start a fresh investigation -- and nothing is
// answered under a guessed replacement.
//
// WHAT IT DOES NOT REFUSE, and each exclusion is a rule, not an accident:
//
//   - A request that is not the window-only shape. The window receipt there
//     rides with another semantic change, and that change's own transition
//     governs the turn.
//   - A window-only request whose continuation is `not_applicable` -- a changed
//     question, an indeterminate identity, a carrier that recorded no reading.
//     Nothing was withheld: there was no continuation to make, and the fresh
//     path is the correct one.
//   - A turn whose fresh frame the gate already refused. That refusal stands
//     with its own basis. It is decided on the question the caller asked, it
//     is equally a refusal, and restating it as a carrier failure would replace
//     a true basis with a different one.
//   - A continuation withheld at SAVE time (window supersession). That turn
//     produced an answer; the supersession is disclosed by the terminal that
//     replaces it, and the decision event reports the reversal.

import (
	"context"
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// continuationRefusalPlaceholderJudgment mirrors the other refusing terminals'
// placeholders: a refusal carries no judgment, and the result contract requires
// the field to name what the turn was about. It is deliberately NOT the fresh
// interpretation's judgment -- publishing that would put the reading this turn
// refused to act on into the served document as the turn's own.
const continuationRefusalPlaceholderJudgment = "continuation context unverifiable"

// refusesTurn reports whether this decision ends the turn with the
// continuation refusal.
//
// FOUR CONJUNCTS, each of them load-bearing and each pinned false alone:
// the request carries a window receipt, it IS the window-only shape, the
// continuation was WITHHELD (not merely not applicable), and the fresh frame's
// gate did not already refuse the turn on its own basis.
func (d windowContinuationDecision) refusesTurn(freshGate FrameGate) bool {
	return d.Observed &&
		d.WindowOnlyShape &&
		d.Disposition == ContinuationWithheld &&
		!freshGate.Refuses()
}

// ObservableRefusalBasis renders the refusal basis this decision served, or
// "none" when it served none.
//
// "none" IS EXPLICIT, never an empty value: a decision line with no basis must
// be distinguishable from a line whose key nobody wrote. It is the same
// missing-versus-measured rule FrameGate.ObservableRefusalBasis holds, and a
// withheld decision that did NOT refuse (the fresh gate refused first) reads
// `none` here beside its own `withheld`, which is how a reader tells the two
// refusals apart.
func (d windowContinuationDecision) ObservableRefusalBasis() string {
	if d.RefusalBasis == "" {
		return "none"
	}
	return string(d.RefusalBasis)
}

// continuationRefusalResult is the terminal a withheld window-only continuation
// refuses through.
//
// ITS OWN EXIT, SHAPED LIKE interpretedTimeBoundResult, because it is the same
// kind of exit: it returns before ResolveSubjects, DiscoverContext, ReadFacts
// and Synthesize, and before the planning stage, so there is no subject, no
// fact, no evidence and no plan to publish. It stamps completeness, display
// labels and the budget itself, immediately before its own Validate, as every
// other independent exit does.
//
// NOT terminalResult. That terminal publishes the fresh interpretation, the
// fresh plan and the structure offers composed from it; every one of those
// describes the reading this refusal declines to act on.
func (e *Engine) continuationRefusalResult(
	ctx context.Context, principal storage.Principal, request InvestigationRequest,
	binding ResolvedGraphBinding,
	// priorSubjectReceiptDispositions is composed against a ZERO resolution by
	// the caller, the same "not re-verified this call" convention every other
	// never-resolved terminal uses. A window-only request carries no subject
	// receipt by definition, so this is empty on every reachable call; it is
	// taken as a parameter so a later widening of the shape cannot drop one.
	priorSubjectReceiptDispositions []contractsv1.ContextFabricPriorSubjectReceiptEntry,
	// plan is nil here by construction -- this exit precedes the planning
	// stage. A parameter rather than a literal nil so the signature matches
	// every sibling terminal and a later reordering cannot silently drop a plan
	// that by then exists.
	plan *AnswerPlan, ancestryParent string,
) (InvestigationResult, error) {
	limitation := contractsv1.ContextFabricContinuationContextUnverifiableLimitation
	emptyCoverage := Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}
	refusedInterpretation := InterpretedQuestion{
		Shape:             ShapeOpen,
		RequestedJudgment: continuationRefusalPlaceholderJudgment,
		TimeContext:       request.TimeContext,
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
		Interpretation:     refusedInterpretation,
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
		RefusalBasis:       contractsv1.ContextFabricRefusalBasisContinuationContextUnverifiable,
		EvidenceRefIDs:     []string{},
		ClaimedFacts:       []ClaimedFact{},
		Coverage:           emptyCoverage,
		Temporal:           composeTemporalLabel(refusedInterpretation, emptyCoverage, ""),
		Versions:           e.terminalVersions(),
		// The deterministic answer IS the limitation, as on the other refusing
		// terminal: there is no judgment to state, and the one sentence that
		// is true of the turn is the refusal.
		DeterministicAnswer: limitation,
		Warnings:            []string{},
	}
	result.Completeness = ComputeAnswerCompleteness(result)
	if omitted := capCoverageEntriesToWriteBound(&result); omitted > 0 && e.telemetry != nil {
		e.telemetry.RecordCoverageEntriesCapped(ctx, principal, len(result.Coverage.Details), omitted)
	}
	if fallbacks := applyCoverageDisplayLabels(&result); fallbacks > 0 && e.telemetry != nil {
		e.telemetry.RecordEvidenceLabelFallback(ctx, principal, fallbacks)
	}
	result, err := e.finalizeServed(ctx, principal, BudgetAssertContinuationRefusal, result, plan, e.effectiveResponseBudget(request))
	if err != nil {
		return InvestigationResult{}, err
	}
	if err := result.Validate(); err != nil {
		return InvestigationResult{}, stageError(StageValidation, fmt.Errorf("%w: %w", ErrInvalidResult, err))
	}
	if e.results != nil {
		// Keyed on the request's own clamped context, with nil reuse
		// snapshots, exactly like the interpreted-time-bound refusal: a
		// refusal must never become reusable, and nil snapshots are the
		// store's fail-closed "never reusable" reading.
		if err := e.saveResult(ctx, principal, BudgetAssertContinuationRefusal, result, nil, nil, TimeAxisKeyFor(request.TimeContext), binding.Epoch, ancestryParent, absentSemanticState(SemanticStateAbsenceContinuationRefused)); err != nil {
			return InvestigationResult{}, stageError(StagePersistence, fmt.Errorf("save investigation result: %w", err))
		}
	}
	return result, nil
}
