package contextfabric

import (
	"context"
	"errors"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Outcome-driven assembly: the served answer says, per requirement, what
// became of it.
//
// WHAT THIS REPLACES. Stage 3 measures the assembled result and, when it
// does not fit, narrows the COHORT and re-synthesizes once. That is its only
// lever. A single-subject question has no cohort, so the stage reaches its
// terminal with the entire repertoire empty -- no content reduction is ever
// attempted -- and refuses, while the unresolved subject candidates, which
// ARE charged against the item budget, sit untouched. Decision D5's own
// header already recorded that candidates are the term re-synthesis cannot
// reach; this file is that paragraph turned into behaviour.
//
// WHAT IT IS NOT. Assembly here still MEASURES and then reduces. Bounding
// assembly BY CONSTRUCTION -- planning against declared caps so the
// unfittable shape is never created -- is a separate, larger change and is
// not delivered here. Saying so plainly matters: a post-hoc cap on
// candidates that called itself a plan would be that change renamed rather
// than made.

type (
	// RequirementOutcome is what happened to one requirement.
	RequirementOutcome = contractsv1.ContextFabricPlanRequirementOutcome
	// AnswerImpactKind is what the reader loses where it did not happen.
	AnswerImpactKind = contractsv1.ContextFabricAnswerImpactKind
	// RequirementOutcomeRow is one row of the outcome set.
	RequirementOutcomeRow = contractsv1.ContextFabricPlanRequirementOutcomeRow
	// AnswerCompletenessState is what the whole set adds up to.
	AnswerCompletenessState = contractsv1.ContextFabricAnswerCompletenessState
)

// requirementIdentity is a requirement row's stable name.
//
// It is the COORDINATE and nothing else -- obligation, role, subject kind --
// because that is what the derivation itself is keyed on. Minting a separate
// id here would create a second authority for which requirement a row is
// about, and the two would drift the first time the derivation changed.
func requirementIdentity(requirement DerivedRequirement) string {
	return string(requirement.Obligation) + "/" + string(requirement.Role) + "/" + string(requirement.Subject)
}

// seedRequirementOutcomes turns this turn's derived requirement rows into
// the initial outcome set.
//
// A row the registry can serve starts SATISFIED. Assembly appends a
// narrowing later if it reduces the thing that row covers; nothing here
// pre-judges an outcome a stage has not reached yet.
//
// A row no producer can serve is UNAVAILABLE with impact `dimension`, and
// its cause is the derivation's OWN closed reason token carried through
// rather than re-classified: the derivation already decided why the cell is
// empty, and deciding it a second time here is how two authorities for one
// fact begin.
//
// Returns nil when there is no frame or no deriver. That is an honest
// absence and it is visible downstream: an empty set derives the
// `not_derived` completeness state, which says the outcomes were never
// derived rather than claiming everything was fine.
func seedRequirementOutcomes(frame *QuestionFrame, deriver RequirementDeriver) []RequirementOutcomeRow {
	return SeedRequirementOutcomes(deriveTurnRequirements(frame, deriver))
}

// SeedRequirementOutcomes is the seed over rows ALREADY DERIVED.
//
// Exported for the same reason PlanRequirementsFromDerived is: it is the other
// projection of one derivation, and a caller building a document outside the
// engine must build these rows through it rather than by hand.
//
// It is split out from seedRequirementOutcomes so a caller that already holds
// the derived rows does not re-derive them. The two published arrays are
// evaluated at two different points in the turn and agree because the
// derivation is pure -- see PlanRequirementsFromDerived's header for why that
// is a property to state carefully rather than a single call to claim.
func SeedRequirementOutcomes(requirements []DerivedRequirement) []RequirementOutcomeRow {
	if len(requirements) == 0 {
		return nil
	}
	rows := make([]RequirementOutcomeRow, 0, len(requirements))
	for _, requirement := range requirements {
		rows = append(rows, planningStageOutcomeRow(
			requirementIdentity(requirement),
			string(requirement.Obligation),
			requirement.Unavailable,
		))
	}
	return rows
}

// SeedOutcomesFromPublishedPlanRequirements builds planning-stage rows for the
// requirements a PLAN publishes, for the exits the derivation-side seed above
// never reaches.
//
// It exists because the two halves of the account were produced at two
// different places, and only one of them ran on every exit. The requirement
// rows are stamped where the plan is CREATED, so every terminal downstream of
// planning carries them; the seed above ran inside finalization, which the
// window- and structure-veto terminals never reach. Those terminals therefore
// served -- and SAVED -- a plan describing requirements that no outcome row
// accounted for, which the document-level join then refuses.
//
// The input is the published array precisely so this cannot become a second
// opinion about what the requirements ARE. It reads what the plan already
// says, so a row it emits cannot describe a requirement the plan does not
// publish.
//
// ITS ROWS ARE NOT THE SEED'S ROWS, AND THAT IS DELIBERATE. This header used
// to end "it re-uses the SAME row builder and the SAME cause table as the
// derivation-side seed, so the two cannot drift". That stopped being true when
// the gap path got its own builder: a row minted HERE describes a requirement
// the turn NEVER REACHED, so it reads `not_attempted` with impact `dimension`
// and the cause `answer_terminated_before_attempt`, while the derivation-side
// seed reads `satisfied` with impact `none` for a requirement the registry can
// serve. See unattemptedRequirementRow for why sharing one builder between the
// two situations forced one default to stand for both and the wrong one won.
// What the two still share is the IDENTITY and the cause table for an
// unservable requirement, which is exactly what
// TestTheSeedAndTheGapRowAgreeOnIdentityAndDisagreeOnOutcome pins.
//
// So a CALLER MUST NOT reach for this function to obtain "a planning seed".
// It is the gap builder. A test fixture that takes its seed from here and then
// expects seed semantics gets a row that is already not-lossless, which makes
// the completeness derivation return `partial` before any later pass is
// consulted -- an outcome that looks like the behaviour under test and is not.
func SeedOutcomesFromPublishedPlanRequirements(published []contractsv1.ContextFabricPlanRequirement) []RequirementOutcomeRow {
	if len(published) == 0 {
		return nil
	}
	rows := make([]RequirementOutcomeRow, 0, len(published))
	for _, requirement := range published {
		rows = append(rows, unattemptedRequirementRow(
			requirement.Requirement,
			requirement.Obligation,
			// The plan carries the reason as its wire token. Converting it
			// back is safe in the only sense that matters here: the cause
			// table fails CLOSED, so a token it does not name yields the
			// empty code rather than an invented one.
			RequirementUnavailableReason(requirement.Unavailable),
		))
	}
	return rows
}

// unattemptedRequirementRow builds the row for a requirement the turn NEVER
// REACHED.
//
// A SEPARATE BUILDER, not a widened default, and that is the whole point. The
// seed rows and these rows describe opposite situations: a seed row is written
// where the derivation RAN and knows what it found, and one of these is
// written where nothing ran at all. They shared `planningStageOutcomeRow`
// briefly, and the shared default is `satisfied` -- so every row minted here
// claimed the requirement had been served in full, on exactly the exits that
// read nothing. Sharing a builder between two situations forced one default to
// stand for both, and the wrong one won.
//
// `not_attempted` is not a lossless outcome, so the row must also carry a
// non-none impact and name a cause. The cause is
// `answer_terminated_before_attempt`, which exists for this and only this: the
// nearest alternative would have said a fact was pruned when nothing was read.
//
// CauseObserved is FALSE, deliberately. That flag means the derivation
// reported this reason for this cell. Nothing reported anything here -- the
// answer ended first -- and claiming otherwise would be a smaller version of
// the same lie this function was written to remove.
//
// An UNSERVABLE requirement keeps its own account: the plan already carries
// the derivation's reason for it, and that reason is true whether or not the
// turn was later vetoed, so it is reported as unavailable exactly as the seed
// would have reported it.
func unattemptedRequirementRow(identity, obligation string, unavailable RequirementUnavailableReason) RequirementOutcomeRow {
	if unavailable != "" {
		return planningStageOutcomeRow(identity, obligation, unavailable)
	}
	return RequirementOutcomeRow{
		Stage:         contractsv1.ContextFabricOutcomeStagePlanning,
		Requirement:   identity,
		Obligation:    obligation,
		Outcome:       contractsv1.ContextFabricRequirementNotAttempted,
		Impact:        contractsv1.ContextFabricAnswerImpactDimension,
		CauseCoverage: contractsv1.ContextFabricCoverageDetailAnswerTerminatedBeforeAttempt,
		CauseObserved: false,
	}
}

// planningStageOutcomeRow is the one place a planning-stage seed row is built.
//
// Both seeds call it. That is the point: before it, the plan-side and
// derivation-side seeds were two copies of the same nine lines, and the first
// reason token added to the vocabulary would have been mapped by one of them
// and missed by the other.
func planningStageOutcomeRow(identity, obligation string, unavailable RequirementUnavailableReason) RequirementOutcomeRow {
	row := RequirementOutcomeRow{
		// The seed rows belong to the stage that PLANNED them, so a
		// reader can see which rows the plan carried and which a later
		// stage appended.
		Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
		Requirement: identity,
		Obligation:  obligation,
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
		Impact:      contractsv1.ContextFabricAnswerImpactNone,
	}
	if unavailable != "" {
		row.Outcome = contractsv1.ContextFabricRequirementUnavailable
		row.Impact = contractsv1.ContextFabricAnswerImpactDimension
		row.CauseCoverage = unavailableRequirementCause(unavailable)
		// Observed: the derivation reported this reason for this cell,
		// it was not defaulted by anything here.
		row.CauseObserved = true
	}
	return row
}

// unavailableRequirementCause maps the derivation's own unavailable reason
// onto the shipped coverage-detail vocabulary.
//
// The mapping is EXPLICIT rather than a pass-through because the two
// vocabularies are owned by different layers and a silent cast would couple
// them: a new reason token would then reach the wire as a coverage code that
// vocabulary never declared. Anything this table does not name yields the
// empty code, and the row still carries its outcome, impact and numbers --
// an unnamed cause is a gap in this table, not a licence to invent one.
func unavailableRequirementCause(reason RequirementUnavailableReason) contractsv1.ContextFabricCoverageDetailCode {
	switch reason {
	case RequirementReasonNoDeclaringProducer:
		return contractsv1.ContextFabricCoverageDetailFactUnconfigured
	case RequirementReasonSubjectKindUnsupported:
		return contractsv1.ContextFabricCoverageDetailFactPruned
	case RequirementReasonTableShapeUndeclared:
		return contractsv1.ContextFabricCoverageDetailFactUnconfigured
	case RequirementReasonComputedPopulationAbsent:
		return contractsv1.ContextFabricCoverageDetailFactPruned
	default:
		return ""
	}
}

// candidateNarrowing is what narrowCandidatesToBudget did, or did not do.
type candidateNarrowing struct {
	Served   int
	Declared int
	Narrowed bool
}

// OutcomeReductionDeclined is the CLOSED vocabulary of why the outcome
// layer's candidate reduction did not serve an answer.
//
// It exists because the alternative was a silent fallback. Before it, every
// run in which the reduction did not save the answer emitted
// `outcome_reduction_applied=false` -- the same value a run emits
// when the reduction was never applicable at all. One value stood for "the
// byte axis overran, so this lever does not apply", "there were no
// candidates to cut", and "the cut RAN and the document still did not fit",
// which have three different fixes and no way to tell apart from the run's
// own artifacts. That is exactly the branch-reaches-a-default-in-silence
// shape this repository's diagnosis-in-artifacts rule forbids.
type OutcomeReductionDeclined string

const (
	// OutcomeReductionNotApplicable is the zero value: the reduction served
	// the answer, or this run never reached an arm that would attempt it.
	OutcomeReductionNotApplicable OutcomeReductionDeclined = ""
	// OutcomeReductionNotItemsAxis: the answer overran on BYTES. The
	// reduction's arithmetic is exact on the items axis and has no
	// equivalent on the byte axis, so the planned refusal stands by design
	// rather than by omission.
	OutcomeReductionNotItemsAxis OutcomeReductionDeclined = "not_items_axis"
	// OutcomeReductionNoItemBudget: this engine was given no item ceiling,
	// so there is no allowance to compute. A deployment-configuration
	// signal, not an answer-shape one.
	OutcomeReductionNoItemBudget OutcomeReductionDeclined = "no_item_budget"
	// OutcomeReductionNothingReducible: the resolver committed without
	// leaving alternatives, so the one collection this layer may cut is
	// already empty. The overrun is entirely in terms the composed judgment
	// cites, which this seam may not drop.
	OutcomeReductionNothingReducible OutcomeReductionDeclined = "nothing_reducible"
	// OutcomeReductionWouldNotReduce: the ceiling already admits every
	// declared candidate, so cutting the list would drop content without
	// fixing anything -- and would publish a narrowing that did not help as
	// though it had.
	//
	// UNREACHABLE from both live call sites today, and deliberately kept.
	// Both callers pair an `overrun` with the measurement it was derived
	// from, and `overrun == items` means that measurement's own
	// `Budgeted() > MaxItems`, which forces `allowance < declared` by
	// arithmetic. That is why a mutant deleting this guard survived the
	// battery: no behavioural test can kill it, because no live input
	// reaches it. It stays because it is the only thing standing between a
	// caller that ever passes a mismatched (result, measurement) pair and
	// an out-of-range slice panic -- measured, not assumed: with the guard
	// removed the direct test panics `slice bounds out of range [:24] with
	// capacity 4`. It is covered by that function-level test rather than an
	// end-to-end one.
	OutcomeReductionWouldNotReduce OutcomeReductionDeclined = "would_not_reduce"
	// OutcomeReductionInsufficient: the cut RAN, and the served document
	// still did not fit. The refusal stands, and this token is what tells
	// an operator the lever was pulled and was not enough -- rather than
	// leaving that run indistinguishable from one where it was never
	// applicable.
	OutcomeReductionInsufficient OutcomeReductionDeclined = "insufficient"
	// OutcomeReductionUnmeasurable: the narrowed document could not be
	// marshaled. A server defect, kept distinct from every over-budget
	// reason so a serialization bug is never counted as an answer that was
	// too big.
	OutcomeReductionUnmeasurable OutcomeReductionDeclined = "unmeasurable"
)

// OutcomeReductionDeclinedVocabulary is the closed set, for the enumeration
// tests that keep a token from shipping unreachable or unnamed.
func OutcomeReductionDeclinedVocabulary() []OutcomeReductionDeclined {
	return []OutcomeReductionDeclined{
		OutcomeReductionNotApplicable,
		OutcomeReductionNotItemsAxis,
		OutcomeReductionNoItemBudget,
		OutcomeReductionNothingReducible,
		OutcomeReductionWouldNotReduce,
		OutcomeReductionInsufficient,
		OutcomeReductionUnmeasurable,
	}
}

// narrowCandidatesToBudget reduces the ONE collection assembly may reduce
// without orphaning prose, and returns what it did.
//
// WHY THE CANDIDATES AND NOTHING ELSE. Drivers, findings and claimed facts
// are cited by the composed judgment; dropping one leaves prose describing
// content that is no longer present, which is the defect this seam is
// forbidden to introduce. Resolution candidates are alternatives the
// resolver did NOT commit to -- nothing in the answer cites them -- so
// removing them changes how many options the caller is shown and nothing
// else. That is a real loss, which is why it is disclosed as one.
//
// THE ITEMS AXIS ONLY. The reduction is computed, not searched: the charged
// terms other than candidates are fixed by the time assembly runs, so the
// number of candidates the ceiling admits is arithmetic. There is no
// equivalent arithmetic for the byte axis -- candidate sizes vary -- and the
// honest alternatives are a guess dressed as arithmetic or an iterated
// shrink loop. Both are refused here: a byte overrun keeps the existing
// planned refusal, and the residual is stated rather than hidden.
func narrowCandidatesToBudget(result InvestigationResult, budget ResponseBudget, measurement ResponseMeasurement, overrun contractsv1.ContextFabricBudgetOverrun) (InvestigationResult, candidateNarrowing, OutcomeReductionDeclined) {
	declared := len(result.SubjectResolution.Candidates)
	unchanged := candidateNarrowing{Served: declared, Declared: declared}
	// Each precondition returns its OWN token. Collapsing them into one
	// "not applicable" would put three different operator actions --
	// reconfigure the ceiling, accept a byte-axis refusal, accept that the
	// resolver left nothing to cut -- behind a single value.
	switch {
	case overrun != contractsv1.ContextFabricBudgetOverrunItems:
		return result, unchanged, OutcomeReductionNotItemsAxis
	case budget.MaxItems <= 0:
		return result, unchanged, OutcomeReductionNoItemBudget
	case declared == 0:
		return result, unchanged, OutcomeReductionNothingReducible
	}
	fixed := measurement.Items.Budgeted() - declared
	allowance := budget.MaxItems - fixed
	if allowance < 0 {
		allowance = 0
	}
	if allowance >= declared {
		// The candidates are not what pushed this over. Reducing them
		// would drop content without fixing anything, and would publish a
		// narrowing that did not help as though it had.
		//
		// Unreachable from either live call site -- see
		// OutcomeReductionWouldNotReduce for the arithmetic and for why the
		// guard is kept and tested directly rather than end-to-end.
		return result, unchanged, OutcomeReductionWouldNotReduce
	}
	// The LEADING allowance, in the order the resolver returned them --
	// which is its own ranking. No re-ordering: sorting the survivors would
	// change the served document beyond narrowing it, and would let the
	// answer present a different ranking than the resolver computed.
	kept := make([]SubjectCandidate, allowance)
	copy(kept, result.SubjectResolution.Candidates[:allowance])
	resolution := result.SubjectResolution
	resolution.Candidates = kept
	result.SubjectResolution = resolution
	return result, candidateNarrowing{Served: allowance, Declared: declared, Narrowed: true}, OutcomeReductionNotApplicable
}

// candidateNarrowingOutcomeRow names what narrowCandidatesToBudget did.
//
// The cause is the DECLARED CEILING, from the shipped overrun vocabulary --
// not a selection order, because no selection ran: the list was truncated at
// its own declared order. Naming a selection basis here would state that an
// order chose the survivors when a ceiling did.
//
// It carries the requirement it reduced when this turn has requirement rows
// to attribute it to, and carries none when it does not. The second case is
// not a silent gap: the completeness state derived from the set says
// `not_derived` for exactly the turns where attribution was impossible.
func candidateNarrowingOutcomeRow(narrowing candidateNarrowing, overrun contractsv1.ContextFabricBudgetOverrun, requirement string, obligation string) RequirementOutcomeRow {
	row := RequirementOutcomeRow{
		Stage:       contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement: requirement,
		Obligation:  obligation,
		Outcome:     contractsv1.ContextFabricRequirementNarrowed,
		// Scope: the caller is shown fewer subjects than the resolver
		// found. Not depth -- the subjects that remain carry exactly the
		// evidence they carried before.
		Impact:       contractsv1.ContextFabricAnswerImpactScope,
		CauseOverrun: overrun,
		// Observed: this stage measured the overrun that forced the cut.
		// Nothing here defaulted.
		CauseObserved: true,
		Served:        narrowing.Served,
		Declared:      narrowing.Declared,
	}
	// THE REDUCTION STEP ITSELF, derived from the row rather than built
	// beside it. Served and Declared are a before and an after with the step
	// between them erased; this says which stage cut and what forced it. The
	// cause here is the ceiling, never a selection basis -- no selection ran,
	// the list was truncated at its own declared order, and this function's
	// header refuses to claim otherwise. Deriving it from the row is what
	// keeps that true without restating it.
	return contractsv1.ContextFabricWithReductionRefinement(row)
}

// subjectScopeRequirement finds the requirement row a candidate-list
// reduction is attributable to: the one whose obligation is about the
// SUBJECT SET the resolver was choosing from.
//
// Returns empty strings when the outcome set carries no such row. An
// unattributed narrowing is reported as one rather than attached to the
// nearest plausible requirement -- a wrong attribution is worse than an
// absent one, because a reader acts on it.
func subjectScopeRequirement(rows []RequirementOutcomeRow) (string, string) {
	for _, row := range rows {
		if row.Obligation == string(ObligationState) {
			return row.Requirement, row.Obligation
		}
	}
	return "", ""
}

// appendOutcomeRows is the APPEND half of the invariant, in one place.
//
// It appends and never rewrites: no caller may edit a row another stage
// wrote, and routing every append through here is what makes that checkable
// by reading one function rather than every call site.
func appendOutcomeRows(existing []RequirementOutcomeRow, added ...RequirementOutcomeRow) []RequirementOutcomeRow {
	if len(added) == 0 {
		return existing
	}
	out := make([]RequirementOutcomeRow, 0, len(existing)+len(added))
	out = append(out, existing...)
	out = append(out, added...)
	return out
}

// outcomeNarrowingAttempt is one run of the outcome layer's reduction: what
// it produced, what it did, and -- when it did not serve -- why.
//
// It is a VALUE rather than a pair of return flags because the decision and
// the telemetry that describes it are now taken at different moments: stage 3
// must know whether an answer will be served BEFORE it emits the event whose
// `refusal_planned` field claims one will not be. Returning the attempt lets
// the caller order those two correctly; a function that emitted as it decided
// could not.
type outcomeNarrowingAttempt struct {
	// Result is the narrowed, re-finalized document. Only meaningful when
	// Served is true.
	Result InvestigationResult
	// Narrowing is the reduction's own served/declared numbers.
	Narrowing candidateNarrowing
	// Measured is the MEASURED ATTEMPT for the document that will actually
	// be served -- never the pre-narrowing one.
	//
	// The whole attempt rather than a bare measurement, because the quota
	// belongs to the same document as the count: three review rounds found
	// arms that carried one and not the other, and a single object is what
	// makes carrying half of it unexpressible.
	Measured MeasuredAttempt
	// Declined names why nothing is served, and is empty when something is.
	Declined OutcomeReductionDeclined
	// Served reports whether Result fits the budget and may be returned to
	// the caller.
	Served bool
}

// planCandidateNarrowing is the decision this seam adds to stage 3, WITHOUT
// its telemetry.
//
// It runs at the two points that used to refuse unconditionally: when the
// cohort lever declined, and when the one bounded retry still did not fit.
// At both it asks the question the stage never asked -- is there anything
// charged against this budget that can be reduced without orphaning prose --
// and produces a narrowed, DISCLOSED answer when there is.
//
// It declines with a NAMED reason when there is not, and the caller's planned
// refusal stands. That refusal is a real terminal case, not an aspiration: an
// answer whose remaining content is all cited by its own judgment cannot be
// made smaller here without describing content that is no longer present.
// What is new is that the refusal now says which of the reasons it was.
//
// The order matters and is not incidental. The document is narrowed, the
// outcome row is APPENDED, and only then is the result re-finalized so
// completeness is derived from the extended set. Deriving before appending
// would be the measure-then-shrink defect this layer exists to remove,
// reproduced inside the fix for it.
func (e *Engine) planCandidateNarrowing(
	ctx context.Context,
	principal storage.Principal,
	plan *AnswerPlan,
	frame *QuestionFrame,
	result InvestigationResult,
	budget ResponseBudget,
	measured MeasuredAttempt,
) (outcomeNarrowingAttempt, error) {
	narrowedResult, narrowing, declined := narrowCandidatesToBudget(result, budget, measured.Measurement, measured.Overrun)
	if !narrowing.Narrowed {
		// The attempt the caller was given is still the one that describes
		// this document: a reduction that did not run leaves the measured
		// attempt of the assembled result as the record of what happened.
		// Carrying it here is what keeps the declining arms from emitting
		// zeros they never measured.
		return outcomeNarrowingAttempt{Narrowing: narrowing, Declined: declined, Measured: measured}, nil
	}
	requirement, obligation := subjectScopeRequirement(narrowedResult.Completeness.Outcomes)
	row := candidateNarrowingOutcomeRow(narrowing, measured.Overrun, requirement, obligation)
	narrowedResult.Completeness.Outcomes = appendOutcomeRows(narrowedResult.Completeness.Outcomes, row)
	narrowedResult = e.finalizeResult(narrowedResult, *plan, frame)

	// Measure what will actually be served. If the reduction did not
	// deliver a fitting document the refusal stands -- serving an answer
	// that still exceeds the ceiling, having announced that it was
	// narrowed to fit, would be worse than refusing.
	//
	// A FULL re-measurement, ledger included: the reduction removed items,
	// so the account of the served document is not the account of the one
	// measured above. Reusing the earlier ledger here would be the
	// "retaining an earlier balanced ledger beside a mutated result"
	// residual, reproduced inside the fix for it.
	served, err := e.measureAssembledAttempt(ctx, principal, "candidate_narrowing", measured.Allocation, narrowedResult, budget)
	if err != nil {
		if errors.Is(err, ErrItemAccounting) {
			// An account that does not reconcile is a SERVER defect and
			// must reach the caller as one. It is not a declined
			// reduction, and reporting it as one would let the answer be
			// refused as oversized on the strength of a defect.
			return outcomeNarrowingAttempt{Narrowing: narrowing}, err
		}
		return outcomeNarrowingAttempt{Narrowing: narrowing, Declined: OutcomeReductionUnmeasurable, Measured: measured}, nil
	}
	if served.Overrun != contractsv1.ContextFabricBudgetFits || !served.CertifiedFit() {
		// The lever was pulled and was not enough. Saying so is the whole
		// difference between this refusal and one where the lever never
		// applied: an operator reading the artifacts can tell that the
		// candidate list was not the binding term here.
		//
		// BOTH AXES, and the certificate. The certificate is about ITEMS --
		// a reconciled account inside a positive item ceiling -- and this
		// stage must not announce a document as narrowed to fit when it
		// still exceeds the BYTE ceiling. An earlier revision of this line
		// checked only the certificate, and the existing byte-window sweep
		// caught it immediately: a document that fitted on items and
		// overran on bytes was served here and refused by the final
		// assertion instead, which reclassifies the caller's refusal from
		// items to bytes and announces a reduction that did not deliver a
		// servable answer. The certificate is still required, because an
		// unbounded budget and an unreconciled account are both "not
		// overrun" and neither may be announced as a fit.
		return outcomeNarrowingAttempt{Narrowing: narrowing, Declined: OutcomeReductionInsufficient, Measured: served}, nil
	}
	return outcomeNarrowingAttempt{
		Result:    narrowedResult,
		Narrowing: narrowing,
		Measured:  served,
		Served:    true,
	}, nil
}

// recordCandidateNarrowing emits the decision-basis event for an attempt
// whose reduction was APPLIED and passed this stage's own fit check.
//
// Not "an attempt that served": the final byte assertion runs later, after
// the plan re-stamp and the display labels have both added bytes, and it can
// still refuse. What this event records is what this stage decided.
//
// Separated from the decision above so stage 3 can emit the retry's own
// event first, with an accurate `refusal_planned`. When the two were fused,
// a retry that did not fit published `refusal_planned=true` and this event
// published `outcome_reduction_applied=true` for the same
// investigation, which was served with a 200 -- a refusal counter counting
// answers that were never refused.
func (e *Engine) recordCandidateNarrowing(
	ctx context.Context,
	principal storage.Principal,
	plan *AnswerPlan,
	attempt outcomeNarrowingAttempt,
	overrun contractsv1.ContextFabricBudgetOverrun,
	grouped bool,
	basis contractsv1.ContextFabricNarrowingBasis,
	before, after int,
	declined RetryDeclinedReason,
	retryAttempted bool,
	retryFit bool,
) {
	e.recordPlanNarrowingStep(plan, PlanNarrowing{
		Stage:   contractsv1.ContextFabricPlanNarrowingAssembledResult,
		Basis:   planStageBasis(contractsv1.ContextFabricPlanNarrowingAssembledResult, grouped, basis),
		Before:  before,
		After:   after,
		Groups:  false,
		Overrun: overrun,
	})
	event := PlanNarrowingEventFrom(*plan, contractsv1.ContextFabricPlanNarrowingAssembledResult, before, after, grouped, false, overrun, basis)
	event.recordMeasurement(attempt.Measured)
	event.PredictedItems = PredictedItemsForPlan(*plan, after)
	event.RetryAttempted = retryAttempted
	// The RETRY's own outcome, not this reduction's. Hardcoding true said a
	// re-synthesis had fitted the answer on exactly the runs where it had
	// not and the candidate cut was what rescued them -- a second way for
	// this event to describe an artifact other than the one served.
	event.RetryFit = retryFit
	event.DeadlineReserved = e.synthesisDeadlineReserve > 0
	event.RetryDeclined = declined
	// The decision itself, as its own dimension: this run reached a point
	// that used to refuse unconditionally, and the reduction was applied
	// instead. Without it the refusal rate falls and an operator cannot
	// tell whether questions stopped overrunning or started being narrowed.
	//
	// It does NOT claim the answer was served -- see the field's own doc
	// comment for the window where it is applied, fits here, and is still
	// refused by the final assertion.
	event.OutcomeReductionApplied = true
	// The inner fit, named as the inner fit. planCandidateNarrowing only
	// returns Served when its own re-measurement fits, so this is true
	// wherever the dimension above is -- carried explicitly so the pair
	// states WHICH measurement passed rather than implying the final one.
	event.OutcomeReductionInnerFit = true
	event.OutcomeItemsServed = attempt.Narrowing.Served
	event.OutcomeItemsDeclared = attempt.Narrowing.Declared
	event.OutcomeCompletenessState = attempt.Result.Completeness.State
	e.recordPlanNarrowing(ctx, principal, event)
}
