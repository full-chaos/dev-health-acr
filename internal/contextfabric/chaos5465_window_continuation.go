package contextfabric

// CHAOS-5465: a verified window-only turn-two confirmation is a CONTINUATION
// of turn one's validated reading, not a fresh interpretation that happens to
// carry a window hint.
//
// WHAT WAS WRONG, measured rather than argued (D-0 probe 1, run on the parent
// at 3822c1d7 through Engine.Investigate with a real store, a real receipt and
// a forced interpreter disagreement):
//
//	plan-carry OUTCOME    outcome="hit" source_result_id=<turn one> seed_source="receipt"
//	APPLIED-carry emits   0
//	SERVED                family="grouped_cohort_status" family_source="model"
//
// The carry LOOKED UP the validated turn-one reading, found it, and then
// discarded it -- because applyCarriedPlan applies only when this turn
// classified nothing of its own, and turn two classified. The single line an
// operator sees says `hit`. The negative control (same carrier, same receipt,
// a turn that classifies nothing) served family_source="carried", so the zero
// above is discriminating and not a broken fixture.
//
// Measured over the archive as well (D-0 probe 2, 38 t1/t2 pairs of one corpus
// row): 38/38 turn-two requests carry EXACTLY ONE window receipt and no other
// prior-result reference, 37/38 name turn one and repeat its question bytes
// exactly, 37/38 turn-one plans are carriable -- and `family_source=carried`
// appears 0 times.
//
// WHAT THIS FILE DECIDES. Recognition of the transition (D-a), which context
// wins and how disagreement is disclosed (D-b), and the one Info event that
// makes the decision observable (D-c). Containment against everything that is
// NOT this transition (D-d) is enforced here too, by refusing to recognise it.
//
// WHAT THIS FILE DELIBERATELY DOES NOT DO, and why, so the next reader does
// not mistake the gap for an oversight:
//
//   - THE CARRIED CONTEXT IS THE DURABLE PLAN, NOT THE FRAME. Nothing persists
//     a QuestionFrame today. pginvestigation/store.go writes
//     `json.Marshal(result)` as the row payload, so the payload IS the public
//     InvestigationResult, and both context_fabric_investigation_result
//     schemas pin `additionalProperties:false`; binding.go's
//     StoredInvestigationResult doc comment states the resulting rule -- a
//     server-internal value lives on the envelope (a column), never on the
//     payload. Measured: 0 of 38 archived turn-one artefacts carry any frame.
//     So the frame, roles, obligations and requirement declarations cannot be
//     preserved until a column and a migration exist (CHAOS-5465 M2, held).
//     What IS durable, and therefore what this build carries, is exactly the
//     answer plan carriablePlan already reads: family, group kind and declared
//     narrowing basis.
//
//   - THERE IS NO REFUSAL PATH HERE. D-b refuses when a carrier cannot be
//     established at all. That refusal needs BOTH a ContextFabricRefusalBasis
//     member and a new fixed service-authored limitation, and both are wire
//     contract tokens. ContextFabricRefusalBasisLimitation composes its
//     sentence from a declared MEMBER KIND, which this condition has none of,
//     so reusing it would state something false. Until those tokens are ruled,
//     an unestablishable carrier is reported as `withheld` with its own reason
//     and the turn proceeds exactly as it does today -- no regression, and the
//     condition is now visible, which it was not before.
//
// The comparison this file performs is therefore COMPLETE with respect to the
// context it accepts: the accepted context is the plan, and every component of
// the plan is compared. `agreement` never claims agreement about a component
// nothing carried.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ContinuationDisposition is what the engine DID about the continuation, as a
// closed, content-safe vocabulary.
//
// THREE MEMBERS, NOT A BOOL, for the reason carryOriginVerdict states one file
// over: "we withheld a context we could see" and "there was no continuation to
// make" are different facts and must not share a label. A rate computed over a
// two-valued disposition would bucket every ordinary non-continuation request
// with every genuine withholding.
type ContinuationDisposition string

const (
	// ContinuationNotApplicable: this request is not a window-only
	// continuation, or it is one whose referenced turn decided nothing to
	// continue. Nothing was withheld because nothing was available.
	ContinuationNotApplicable ContinuationDisposition = "not_applicable"
	// ContinuationApplied: the carried context was admitted and is what the
	// rest of the turn executes under.
	ContinuationApplied ContinuationDisposition = "applied"
	// ContinuationWithheld: this request IS a window-only continuation and a
	// carrier was named, but admission could not be established. Fail-closed
	// against semantic substitution: the carrier is not used, and neither is
	// the old family-only carry (see D-d).
	ContinuationWithheld ContinuationDisposition = "withheld"
)

// ContinuationDecisionReason is the closed admission/evaluation reason. It is
// ALWAYS present -- there is no empty member, because an empty reason beside a
// `not_applicable` disposition is indistinguishable from a code path that
// never decided.
//
// `unspecified` NEVER means admission succeeded. It is the fail-closed member,
// the same discipline ContextFabricRefusalBasisUnspecified holds: reaching it
// means a decision site was added without a reason line here.
type ContinuationDecisionReason string

const (
	// ContinuationReasonNone: no reason to withhold. Valid ONLY alongside
	// ContinuationApplied.
	ContinuationReasonNone ContinuationDecisionReason = "none"
	// ContinuationReasonNotWindowOnly: the request carries a window receipt
	// but is not the window-only shape -- plural window receipts, another
	// prior-result reference, or an added typed selection. D-d: a window
	// receipt riding along with a subject/kind/anchor/handle/candidate
	// selection is that selection's turn, not a continuation.
	ContinuationReasonNotWindowOnly ContinuationDecisionReason = "not_window_only"
	// ContinuationReasonWindowVeto: window validation vetoed this request, so
	// there was never a verified confirmation to continue from. Existing
	// window vetoes keep their existing handling; this reason only records
	// that the continuation decision did not run (D-e).
	ContinuationReasonWindowVeto ContinuationDecisionReason = "window_veto"
	// ContinuationReasonChangedQuestion: the referenced turn answered a
	// DIFFERENT question. The window receipt stays subject to the existing
	// window rules; it cannot import the prior non-window context (D-d).
	ContinuationReasonChangedQuestion ContinuationDecisionReason = "changed_question"
	// ContinuationReasonIndeterminateIdentity: one of the two questions
	// canonicalizes to the empty string, so there is no identity to compare.
	// Not drift -- nothing was shown to differ (carryOriginVerdict's own
	// three-state rule, reused rather than re-derived).
	ContinuationReasonIndeterminateIdentity ContinuationDecisionReason = "indeterminate_identity"
	// ContinuationReasonMissingContext: the carrier read back perfectly and
	// carried no reading to continue -- an unclassified or absent plan.
	// carriablePlan refuses unclassified deliberately ("the next turn is
	// entitled to its own attempt"), and that shipped rule is preserved: this
	// is not_applicable, not a withholding.
	ContinuationReasonMissingContext ContinuationDecisionReason = "missing_context"
	// ContinuationReasonInvalidContext: the carrier could not be read at all,
	// or failed the CHAOS-3898 ingress taint gate.
	ContinuationReasonInvalidContext ContinuationDecisionReason = "invalid_context"
	// ContinuationReasonContextVersionMismatch: the carrier's recorded family
	// definition-table version is not the one in force. D-a: revalidate under
	// the RECORDED standard; an unsupported version is an admission failure,
	// never permission to reinterpret the context under today's tables.
	ContinuationReasonContextVersionMismatch ContinuationDecisionReason = "context_version_mismatch"
	// ContinuationReasonFreshContextUnavailable: the diagnostic fresh proposal
	// was not produced. It does NOT invalidate an independently admitted
	// carrier -- see admitWindowContinuation.
	ContinuationReasonFreshContextUnavailable ContinuationDecisionReason = "fresh_context_unavailable"
	// ContinuationReasonUnspecified: a decision site reached a return without
	// recording a reason. Loud by construction.
	ContinuationReasonUnspecified ContinuationDecisionReason = "unspecified"
)

// ContinuationConflictReason is the Info-only disagreement token.
//
// A RECONCILED DISAGREEMENT IS NOT A REFUSAL. The user already supplied the
// authorized change by redeeming the window offer; asking them to arbitrate a
// model proposal the server did not use would turn sampler variance into a new
// product requirement.
type ContinuationConflictReason string

const (
	// ContinuationConflictNone: explicit, never the zero value standing in for
	// "not measured". An unevaluated comparison also reports `none` and is
	// told apart by ComparisonEvaluated.
	ContinuationConflictNone ContinuationConflictReason = "none"
	// ContinuationConflictNonWindowContext: the fresh proposal disagreed with
	// the admitted carried context on something other than the window.
	ContinuationConflictNonWindowContext ContinuationConflictReason = "non_window_context_conflict"
)

// ContinuationConflictField names a SEMANTIC COMPONENT that differed. These
// are not wire fields and they are not new schema.
//
// The vocabulary is declared WHOLE, matching the design of record, so that the
// members a later slice can populate are already named rather than invented
// twice. This build populates only the members its accepted context actually
// carries -- see continuationComparableFields, and the pin that asserts which
// members are populatable today.
type ContinuationConflictField string

const (
	ContinuationConflictFieldFamily             ContinuationConflictField = "family"
	ContinuationConflictFieldSubjectExpression  ContinuationConflictField = "subject_expression"
	ContinuationConflictFieldNarrowingBasis     ContinuationConflictField = "narrowing_basis"
	ContinuationConflictFieldRoles              ContinuationConflictField = "roles"
	ContinuationConflictFieldGoals              ContinuationConflictField = "goals"
	ContinuationConflictFieldTemporal           ContinuationConflictField = "temporal"
	ContinuationConflictFieldEmphasis           ContinuationConflictField = "emphasis"
	ContinuationConflictFieldDimensions         ContinuationConflictField = "dimensions"
	ContinuationConflictFieldObligations        ContinuationConflictField = "obligations"
	ContinuationConflictFieldWidenedObligations ContinuationConflictField = "widened_obligations"
	ContinuationConflictFieldRequirements       ContinuationConflictField = "requirements"
	ContinuationConflictFieldInterpretation     ContinuationConflictField = "interpretation"
	ContinuationConflictFieldFrameGate          ContinuationConflictField = "frame_gate"
)

// continuationComparableFields is the subset this build can actually compare,
// in a fixed order so conflict_fields is deterministic.
//
// Every other member of the vocabulary above describes something no stored
// result carries, so this build never claims those agreed and never claims
// they conflicted -- there is nothing carried for them to disagree with. When
// durable context persistence lands, this list grows and the pin that names it
// fails until it is updated deliberately.
//
// NARROWING BASIS IS CARRIED BUT NOT COMPARED, and the distinction is the
// point. The carried basis reaches the plan through the existing plan-carry
// path (Investigate stamps plan.Budget.NarrowingBasis from a carry hit), but
// the FRESH side has no basis of its own to disagree with: it is derived
// inside PlanAnswer, after this comparison, from a default. Listing it as
// comparable would manufacture a comparison against a value the interpreter
// never proposed -- which is precisely the "compare a copy that merely happens
// to be equal" shape this package has been bitten by before.
func continuationComparableFields() []ContinuationConflictField {
	return []ContinuationConflictField{
		ContinuationConflictFieldFamily,
		ContinuationConflictFieldSubjectExpression,
	}
}

// continuationCarriedContext is the validated prior context this build
// preserves: the answer plan's own reading of the question.
type continuationCarriedContext struct {
	Family         QuestionFamily
	GroupKind      SubjectKind
	NarrowingBasis contractsv1.ContextFabricNarrowingBasis
	FamilyVersion  string
	SourceResultID string
}

// continuationFreshProposal is the interpreter's return, held as a
// NON-AUTHORITATIVE comparison for an admitted continuation.
type continuationFreshProposal struct {
	Available bool
	Family    QuestionFamily
	GroupKind SubjectKind
}

// windowContinuationDecision is the whole decision, and the single value that
// both drives execution and populates the event.
//
// ONE VALUE, TWO USES, deliberately: a decision logged from one variable and
// executed from another is the "log the intended selection, pass the fresh one
// downstream" defect the mutant matrix names, and it is invisible from the
// event alone.
type windowContinuationDecision struct {
	// Observed is false when the request carries no window receipt at all. No
	// event is emitted for such a request -- the event's denominator is
	// "requests carrying window receipts", and widening it to every request
	// would make the rate meaningless.
	Observed bool

	Disposition ContinuationDisposition
	Reason      ContinuationDecisionReason

	Carried  *continuationCarriedContext
	Fresh    continuationFreshProposal
	Accepted *continuationCarriedContext

	SeedSource CarrySeedSource

	ComparisonEvaluated bool
	Agreement           bool
	ConflictReason      ContinuationConflictReason
	ConflictFields      []ContinuationConflictField

	AppliedWindow *contractsv1.ContextFabricEffectiveEvidenceWindow
}

// Applies reports whether the carried context is authoritative for this turn.
func (d windowContinuationDecision) Applies() bool {
	return d.Disposition == ContinuationApplied && d.Accepted != nil
}

// requestCarriesWindowReceipts is the event's denominator.
func requestCarriesWindowReceipts(request InvestigationRequest) bool {
	for _, receipt := range request.PriorWindowReceipts {
		if strings.TrimSpace(receipt.ResultID) != "" {
			return true
		}
	}
	return false
}

// windowOnlyReferencedResultID reports the ONE prior result a window-only
// continuation may continue, and whether the request has that shape at all.
//
// THE SHAPE IS THE CONTAINMENT (D-d). Every other prior-result reference and
// every typed selection disqualifies the transition, because each of them is a
// semantic change the caller made on THIS turn -- and a continuation is
// defined as the turn that changed only the evidence window. A request that
// selects a subject, kind, anchor, handle or candidate AND redeems a window
// offer is that selection's turn; it takes its own ratified transition.
//
// Deliberately computed from the REQUEST alone, with no I/O, so it can be
// decided before anything expensive runs and so a veto path can report it.
func windowOnlyReferencedResultID(request InvestigationRequest) (string, bool) {
	var seen []string
	for _, receipt := range request.PriorWindowReceipts {
		id := strings.TrimSpace(receipt.ResultID)
		if id == "" {
			continue
		}
		seen = append(seen, id)
	}
	// Plural window receipts are CHAOS-5271's territory (resolveWindowReceipts
	// vetoes them). They are never a continuation here either, and the two
	// facts are kept separate: this returns false, it does not suppress the
	// veto.
	if len(seen) != 1 {
		return "", false
	}
	if strings.TrimSpace(request.ParentResultID) != "" {
		return "", false
	}
	if len(request.PriorSubjectReceipts) > 0 ||
		len(request.PriorKindReceipts) > 0 ||
		len(request.PriorAnchorReceipts) > 0 ||
		len(request.PriorHandleReceipts) > 0 ||
		len(request.PriorCandidateReceipts) > 0 {
		return "", false
	}
	return seen[0], true
}

// continuationQuestionIdentity applies D-a's identity rule to the two
// questions.
//
// RAW BYTES, and the design says why: "equal byte lengths alone prove nothing"
// and "a byte-different question is outside this first-cut continuation rule,
// including when canonicalization considers it equivalent". The canonical
// identity guard is kept ON TOP of that, reused from the same-question
// containment one file over rather than re-derived, because two questions that
// both canonicalize to the empty string are not the same question -- they are
// questions the hash cannot tell apart.
func continuationQuestionIdentity(requestQuestion, priorQuestion string) ContinuationDecisionReason {
	if CanonicalizeQuestion(requestQuestion) == "" || CanonicalizeQuestion(priorQuestion) == "" {
		return ContinuationReasonIndeterminateIdentity
	}
	if requestQuestion != priorQuestion {
		return ContinuationReasonChangedQuestion
	}
	return ContinuationReasonNone
}

// admitWindowContinuation decides eligibility and carrier admission. It runs
// AFTER receipt validation and graph binding and BEFORE the fresh
// interpretation is consumed for anything, so the accepted context is settled
// before any consumer of a fresh semantic value can act on it.
//
// It performs NO fresh-value comparison: that is a separate step
// (compareContinuationProposal) taken once the proposal exists, precisely so a
// failure of the DIAGNOSTIC proposal cannot invalidate an independently
// admitted carrier.
func (e *Engine) admitWindowContinuation(
	ctx context.Context,
	principal storage.Principal,
	request InvestigationRequest,
	binding ResolvedGraphBinding,
	preloaded map[string]InvestigationResult,
) windowContinuationDecision {
	decision := windowContinuationDecision{
		Observed:       requestCarriesWindowReceipts(request),
		Disposition:    ContinuationNotApplicable,
		Reason:         ContinuationReasonUnspecified,
		SeedSource:     CarrySeedNone,
		ConflictReason: ContinuationConflictNone,
		ConflictFields: []ContinuationConflictField{},
	}
	if !decision.Observed {
		decision.Reason = ContinuationReasonNotWindowOnly
		return decision
	}
	decision.SeedSource = CarrySeedReceipt

	referenced, windowOnly := windowOnlyReferencedResultID(request)
	if !windowOnly {
		decision.Reason = ContinuationReasonNotWindowOnly
		return decision
	}
	if e.results == nil {
		decision.Disposition = ContinuationWithheld
		decision.Reason = ContinuationReasonInvalidContext
		return decision
	}

	stored, err := carryLoadResult(ctx, e.results, principal, referenced)
	if err != nil {
		decision.Disposition = ContinuationWithheld
		decision.Reason = ContinuationReasonInvalidContext
		return decision
	}
	// The SAME CHAOS-3898 ingress taint gate every other carrier check
	// applies, and it is applied here even for a preloaded entry's id,
	// because this decision is about semantic authority rather than about a
	// hint: a rebuild between turns can legitimately change what the prior
	// reading meant.
	if stored.GraphEpoch == nil || *stored.GraphEpoch != binding.Epoch {
		decision.Disposition = ContinuationWithheld
		decision.Reason = ContinuationReasonInvalidContext
		return decision
	}
	prior := stored.Result
	if cached, ok := preloaded[referenced]; ok {
		prior = cached
	}

	if reason := continuationQuestionIdentity(request.Question, prior.Question); reason != ContinuationReasonNone {
		// NOT a withholding: the transition was never established, so there
		// was no continuation to withhold. The window receipt keeps its
		// existing treatment; what it may not do is import this context.
		decision.Disposition = ContinuationNotApplicable
		decision.Reason = reason
		return decision
	}

	plan := carriablePlan(prior)
	if plan == nil {
		// The carrier read back perfectly and had nothing to continue.
		decision.Disposition = ContinuationNotApplicable
		decision.Reason = ContinuationReasonMissingContext
		return decision
	}
	// D-a: revalidate under the RECORDED standard. A carrier stamped by a
	// different family definition table is not reinterpreted under today's;
	// an unsupported version is an admission failure.
	if strings.TrimSpace(plan.FamilyVersion) != "" && plan.FamilyVersion != QuestionFamilyTableVersion {
		decision.Disposition = ContinuationWithheld
		decision.Reason = ContinuationReasonContextVersionMismatch
		return decision
	}

	decision.Carried = &continuationCarriedContext{
		Family:         plan.Family,
		GroupKind:      plan.GroupKind,
		NarrowingBasis: plan.Budget.NarrowingBasis,
		FamilyVersion:  plan.FamilyVersion,
		SourceResultID: prior.ResultID,
	}
	decision.Accepted = decision.Carried
	decision.Disposition = ContinuationApplied
	decision.Reason = ContinuationReasonNone
	return decision
}

// compareContinuationProposal folds the fresh interpreter return in as a
// NON-AUTHORITATIVE comparison and records the disagreement.
//
// FAIL-CLOSED AGAINST SEMANTIC SUBSTITUTION, and this is the whole ordering
// rule in one function: an unavailable or failed proposal records an
// unevaluated comparison and the carrier still wins. Only failure to establish
// the CARRIER changes what is served.
func compareContinuationProposal(decision windowContinuationDecision, fresh continuationFreshProposal) windowContinuationDecision {
	decision.Fresh = fresh
	if !decision.Applies() {
		// Nothing was accepted, so there is nothing to compare against. Both
		// booleans stay false; the disposition is what tells the two apart.
		decision.ComparisonEvaluated = false
		decision.Agreement = false
		decision.ConflictReason = ContinuationConflictNone
		decision.ConflictFields = []ContinuationConflictField{}
		return decision
	}
	if !fresh.Available {
		decision.ComparisonEvaluated = false
		decision.Agreement = false
		decision.ConflictReason = ContinuationConflictNone
		decision.ConflictFields = []ContinuationConflictField{}
		return decision
	}

	accepted := *decision.Accepted
	// EVERY differing component, in a fixed order -- not merely the first.
	// A same-family subject-expression substitution is exactly the shape that
	// is invisible when only the family is compared.
	differs := map[ContinuationConflictField]bool{
		ContinuationConflictFieldFamily:            accepted.Family != fresh.Family,
		ContinuationConflictFieldSubjectExpression: accepted.GroupKind != fresh.GroupKind,
	}
	fields := []ContinuationConflictField{}
	for _, field := range continuationComparableFields() {
		if differs[field] {
			fields = append(fields, field)
		}
	}
	decision.ComparisonEvaluated = true
	decision.ConflictFields = fields
	decision.Agreement = len(fields) == 0
	if len(fields) == 0 {
		decision.ConflictReason = ContinuationConflictNone
	} else {
		decision.ConflictReason = ContinuationConflictNonWindowContext
	}
	return decision
}

// applyWindowContinuation makes the accepted context the turn's own reading.
//
// It writes the SAME fields applyCarriedPlan writes, for the same reasons
// (the group kind rides on the winning sample because that is where PlanAnswer
// reads it), and it sets Switched against the family it actually replaced
// rather than hardcoding it.
func applyWindowContinuation(outcome QuestionFamilyOutcome, decision windowContinuationDecision) (QuestionFamilyOutcome, bool) {
	if !decision.Applies() {
		return outcome, false
	}
	accepted := *decision.Accepted
	replaced := outcome.Family
	outcome.Family = accepted.Family
	outcome.Source = QuestionFamilySourceCarried
	outcome.WinningSample.GroupKind = accepted.GroupKind
	outcome.Route = FamilyRouteDecision{
		Family: accepted.Family,
		Source: FamilyRouteSourceCarried,
		// EXPLICITLY empty: a comparison did happen this turn, but it was a
		// diagnostic one whose result is published on the continuation event's
		// own agreement/conflict fields. Reporting an agreement CLASS here
		// would claim the routing tables reached this family by consensus,
		// which they did not -- a prior turn did.
		Class:       "",
		Disposition: FamilyRouteCarried,
		Switched:    accepted.Family != replaced,
	}
	return outcome, true
}

// continuationContextID is a short, content-safe digest identifying an
// immutable decision artifact.
//
// IDS, CLOSED VALUES AND EQUALITY RESULTS ONLY -- never corpus question text
// or subject labels. The carried context's artifact is the prior RESULT, so
// its id is that result id; a fresh proposal has no stored artifact, so it is
// identified by a digest of its own closed values, which is stable across
// replicates and reveals nothing.
func continuationContextID(family QuestionFamily, groupKind SubjectKind, basis contractsv1.ContextFabricNarrowingBasis) string {
	sum := sha256.Sum256([]byte(string(family) + "\x1f" + string(groupKind) + "\x1f" + string(basis)))
	return "cfctx_" + hex.EncodeToString(sum[:8])
}

// CarriedContextID / FreshContextID / AcceptedContextID render the three
// artifact ids, explicitly empty when that context is absent.
func (d windowContinuationDecision) CarriedContextID() string {
	if d.Carried == nil {
		return ""
	}
	return d.Carried.SourceResultID
}

func (d windowContinuationDecision) FreshContextID() string {
	if !d.Fresh.Available {
		return ""
	}
	return continuationContextID(d.Fresh.Family, d.Fresh.GroupKind, "")
}

func (d windowContinuationDecision) AcceptedContextID() string {
	if d.Accepted == nil {
		return ""
	}
	return d.Accepted.SourceResultID
}

// FamilyCarried / FamilyFresh / FamilyAccepted are the three family readings,
// explicitly empty where that context is absent -- never a fabricated family.
func (d windowContinuationDecision) FamilyCarried() QuestionFamily {
	if d.Carried == nil {
		return ""
	}
	return d.Carried.Family
}

func (d windowContinuationDecision) FamilyFresh() QuestionFamily {
	if !d.Fresh.Available {
		return ""
	}
	return d.Fresh.Family
}

func (d windowContinuationDecision) FamilyAccepted() QuestionFamily {
	if d.Accepted == nil {
		return ""
	}
	return d.Accepted.Family
}

// AcceptedFamilySource is the provenance value of the accepted context, empty
// when nothing was accepted.
func (d windowContinuationDecision) AcceptedFamilySource() QuestionFamilySource {
	if d.Accepted == nil {
		return ""
	}
	return QuestionFamilySourceCarried
}

// ConflictCount is explicitly zero or the number of differing components.
func (d windowContinuationDecision) ConflictCount() int { return len(d.ConflictFields) }

// ConflictFieldTokens renders conflict_fields as a stable, content-safe list.
func (d windowContinuationDecision) ConflictFieldTokens() []string {
	tokens := make([]string, 0, len(d.ConflictFields))
	for _, field := range d.ConflictFields {
		tokens = append(tokens, string(field))
	}
	return tokens
}

// AppliedWindowToken renders the actual applied window -- relative id, frozen
// bounds and provenance -- or the empty string when no window applied.
//
// Rendered rather than logged as a struct so the line is one closed,
// greppable value and so an absent window is explicitly empty rather than a
// zero-valued object that reads like a real window with empty bounds.
func (d windowContinuationDecision) AppliedWindowToken() string {
	if d.AppliedWindow == nil {
		return ""
	}
	window := *d.AppliedWindow
	var start, end string
	if window.Start != nil {
		start = window.Start.UTC().Format("2006-01-02T15:04:05Z")
	}
	if window.End != nil {
		end = window.End.UTC().Format("2006-01-02T15:04:05Z")
	}
	return string(window.RelativeID) + "|" + start + "|" + end + "|" + string(window.Provenance)
}

// BlocksLegacyCarry reports whether this decision must also stop the OLD
// family-only plan carry from applying.
//
// D-d, stated as its own predicate because the design names the exact escape:
// "Do not let failure of the continuation gate fall through into the old
// family-only carry when the fresh question is `unclassified`."
//
// The hole is real and it is reachable today. carryReferencedResultIDs scans
// PriorWindowReceipts FIRST and the receipt walk is deliberately UNGATED for
// question identity -- a receipt is a redeemed server offer, so the ungated
// treatment is correct for the window it redeems. But it means a window
// receipt pointing at a result that answered a DIFFERENT question can seed the
// plan carry, and if this turn then classifies nothing, applyCarriedPlan
// installs that unrelated reading. The continuation gate is the first thing in
// this package that actually compares the two questions on a receipt-rooted
// path, so it is where the refusal has to be spent.
//
// Reachability is UNCHANGED by this: carryReferencedResultIDs still returns
// exactly what it returned, so the answer-reuse bypass keyed on the same
// population (CHAOS-4998) is untouched. What is narrowed is what a
// receipt-rooted hit is allowed to MEAN.
func (d windowContinuationDecision) BlocksLegacyCarry() bool {
	if !d.Observed {
		return false
	}
	switch d.Reason {
	case ContinuationReasonChangedQuestion, ContinuationReasonIndeterminateIdentity:
		return true
	default:
		return false
	}
}

// blockedLegacyCarryOutcome is the miss this containment reports, chosen from
// the EXISTING closed vocabulary with its existing meanings rather than by
// minting a member: the two conditions are precisely question drift and
// indeterminate question identity, which PlanCarryOutcome already names, and
// which carryOriginVerdict already refuses to collapse into one another.
func (d windowContinuationDecision) blockedLegacyCarryOutcome() PlanCarryOutcome {
	if d.Reason == ContinuationReasonIndeterminateIdentity {
		return PlanCarryMissQuestionIndeterminate
	}
	return PlanCarryMissQuestionDrift
}

// applyAndRecordContinuation applies the accepted context and emits the ONE
// existing event that can carry `family_source=carried`.
//
// IT EMITS RecordPlanCarry TOO, and that is deliberate rather than incidental.
// A continuation IS an applied carry: it stamps QuestionFamilySourceCarried on
// the served plan. Leaving the existing applied-carry counter at zero while
// `family_source=carried` appeared on the wire would reintroduce, in the
// opposite direction, exactly the numerator/denominator split CHAOS-5003
// closed -- an operator would see served carries that the carry telemetry says
// never happened.
//
// The event is built from the SAME decision that drives execution, through the
// existing PlanCarryEventFrom constructor, so the two lines cannot describe
// different families.
func (e *Engine) applyAndRecordContinuation(ctx context.Context, principal storage.Principal, outcome QuestionFamilyOutcome, decision windowContinuationDecision) QuestionFamilyOutcome {
	carried, applied := applyWindowContinuation(outcome, decision)
	if !applied {
		return outcome
	}
	if e.telemetry != nil {
		// THE EVENT IS BUILT DIRECTLY, NOT THROUGH A SYNTHESIZED
		// planCarryResult, and the reason is a structural guard this package
		// already enforces: TestCarryGateClosure refuses any
		// planCarryResult{Outcome: PlanCarryHit} constructed outside
		// resolveCarriedPlan's reach, because a hit built elsewhere is a hit
		// that never met the same-question comparison. That refusal is
		// correct and it is kept. This continuation met a STRICTER identity
		// rule (raw question bytes, not a hash) inside
		// admitWindowContinuation -- but manufacturing a carry hit to say so
		// would put a value into the one shape the guard exists to forbid,
		// and the next reader would have to re-derive why it was safe.
		//
		// outcome.Family is still the PRE-continuation value here, matching
		// applyAndRecordCarry's own ordering and for the same reason.
		e.telemetry.RecordPlanCarry(ctx, principal, PlanCarryEvent{
			FamilyReplaced: outcome.Family,
			FamilyCarried:  carried.Family,
			SourceResultID: decision.Accepted.SourceResultID,
			Route:          carried.Route,
		})
	}
	return carried
}
