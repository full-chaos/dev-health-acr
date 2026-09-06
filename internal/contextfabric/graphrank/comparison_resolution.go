package graphrank

// TURN-1 RESOLUTION OF A TWO-NAMED-OPERAND COMPARISON -- the resolver-local
// working state, and the single place it becomes a published resolution.
//
// NOTHING IN THIS FILE IS EVER SERIALIZED. Not persisted, not embedded in a
// contract DTO, not reconstructed by a consumer. That is a hard constraint
// rather than a preference: contextfabric.SubjectResolution is a TYPE ALIAS to
// the contracts type and is embedded with a JSON tag in two published places,
// so a field added to it is a WIRE CHANGE. The operand structure this file
// works in therefore lives here, dies here, and reaches the wire only through
// the existing fields publishComparisonResolution writes.
//
// GraphReader.ResolveSubjects keeps its existing return signature.
//
// WHAT THIS FILE DOES NOT DO. It does not retrieve. Each slot's candidates
// arrive already authorized, collision-checked, truncated and proof-carrying
// from the retrieval work resolve.go owns -- extracted there and reused here
// rather than duplicated, because a second copy of those rules is a second
// place for them to be wrong. This file decides, and publishes.

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// operandSlotState is the closed vocabulary for what happened to ONE operand.
//
// CLOSED, AND IT NEVER NAMES A COMBINATION. An ambiguous operand beside a
// resolved one is two slot states and one aggregate hold, never a single
// "partially resolved" token -- the same discipline the telemetry vocabulary
// carries, for the same reason: a combination token cannot be counted, and the
// moment one exists every new pairing needs another.
type operandSlotState string

const (
	// operandSlotResolved: exactly one winner, of this slot's stated kind.
	operandSlotResolved operandSlotState = "resolved"
	// operandSlotNoCandidate: retrieval found nothing for this operand's own
	// terms. The user is asked to RESTATE this side, not to choose.
	operandSlotNoCandidate operandSlotState = "no_candidate"
	// operandSlotAmbiguous: more than one candidate and no unique winner.
	operandSlotAmbiguous operandSlotState = "ambiguous"
	// operandSlotWrongKind: a winner was found but its subject kind is not
	// the kind the question stated for this operand.
	operandSlotWrongKind operandSlotState = "wrong_kind"
	// operandSlotOverCommitted: more than one subject bound to this one slot.
	// Admissible cardinality here is ZERO OR ONE; more is refused rather than
	// narrowed, because choosing between them is the guess this design exists
	// to avoid.
	operandSlotOverCommitted operandSlotState = "over_committed"
	// operandSlotScoped: a scoped operand. Never resolved in this cut -- the
	// state exists so the hold can describe the side it is holding.
	operandSlotScoped operandSlotState = "scoped"
)

// operandSlotRun is ONE operand's run: what was retrieved for its own terms,
// what was bound to it, and the decision that follows.
type operandSlotRun struct {
	// slot is the structural description, straight from the classifier. Its
	// Position is the ordering authority for everything published.
	slot contextfabric.ComparisonOperandSlot

	// candidates are the authorized candidates retrieved for THIS SLOT'S OWN
	// TERMS. Neither the whole-question term bag nor another slot's terms may
	// contribute to this list -- that isolation IS the fix.
	candidates []contextfabric.SubjectCandidate

	// committed holds this slot's winner. ADMISSIBLE CARDINALITY IS ZERO OR
	// ONE. A slot handed more than one subject is over-committed and refused;
	// it is never silently narrowed to the first, because "the first" is a
	// property of iteration order, not of evidence.
	committed []contextfabric.SubjectRef

	// bases and digests are this slot's own commit proofs, kept per slot so
	// the union at publication can be first-writer-wins by subject key rather
	// than letting the second slot overwrite the first's.
	bases   contextfabric.CommitBasisSet
	digests contextfabric.CommitDecisionDigestSet

	// receiptBound records that this slot's winner arrived from a carried
	// receipt rather than from this turn's retrieval. It changes no decision
	// here; it is carried for telemetry and for the prompt's wording.
	receiptBound bool

	// retrievalDegraded is THIS SLOT'S retrieval health. Kept per slot because
	// one operand's degradation must be visible as that operand's, and must
	// still propagate to the aggregate.
	retrievalDegraded bool
}

// state derives what happened to this operand from its own contents.
//
// DERIVED, NEVER ASSIGNED. A stored state field would be a second authority
// that could disagree with the candidates and the committed set it claims to
// summarize, and every reader would then have to guess which one was right.
func (s operandSlotRun) state() operandSlotState {
	if s.slot.Variant == contextfabric.ComparisonOperandScoped {
		return operandSlotScoped
	}
	switch {
	case len(s.committed) > 1:
		return operandSlotOverCommitted
	case len(s.committed) == 1:
		if s.committed[0].Kind != s.slot.Kind {
			return operandSlotWrongKind
		}
		return operandSlotResolved
	case len(s.candidates) == 0:
		return operandSlotNoCandidate
	}
	return operandSlotAmbiguous
}

// resolved reports whether this slot may contribute a published subject.
func (s operandSlotRun) resolved() bool { return s.state() == operandSlotResolved }

// comparisonResolutionRun owns the ordered operand runs and the aggregate
// publication decision.
type comparisonResolutionRun struct {
	// admission is the classifier's verdict, carried so publication can
	// distinguish a held scoped pair from a pair that simply did not resolve.
	admission contextfabric.ComparisonAdmission

	// slots are ordered by the validated frame's operand position. NEVER
	// assembled by iterating a subject map: published committed order follows
	// this order, and a map walk is not an order at all.
	slots []operandSlotRun

	// unboundReceipts counts carried selections that matched no operand, or
	// both. Either way they are UNBOUND rather than guessed, and either way
	// they hold the comparison -- a selection the server could not associate
	// with exactly one operand has completed neither.
	unboundReceipts int
}

// retrievalDegraded reports the AGGREGATE retrieval health.
//
// Any slot's degradation is the comparison's degradation. A comparison is one
// answer over two operands, so evidence missing from either side is missing
// from the answer, and reporting otherwise would let a half-degraded
// comparison read as fully evidenced.
func (r comparisonResolutionRun) retrievalDegraded() bool {
	for _, slot := range r.slots {
		if slot.retrievalDegraded {
			return true
		}
	}
	return false
}

// publishable is ruling 1A, in one place.
//
// Committed subjects are released ONLY when every condition holds: the pair
// was admitted, every slot has exactly one winner of its own stated kind, the
// winners are distinct subjects, and no carried selection remains ambiguously
// bound. Anything less holds the WHOLE comparison -- there is no half-answer,
// because a half-answer cannot be completed: the shared projection drops a
// clarification unless the answer status is the clarification-required one, so
// the resolved side would arrive without the action that finishes it.
func (r comparisonResolutionRun) publishable() bool {
	if r.admission != contextfabric.ComparisonAdmittedNamedPair {
		return false
	}
	if r.unboundReceipts > 0 {
		return false
	}
	if len(r.slots) != comparisonSlotCount {
		return false
	}
	seen := make(map[string]struct{}, len(r.slots))
	for _, slot := range r.slots {
		if !slot.resolved() {
			return false
		}
		key := SubjectKey(slot.committed[0])
		if _, duplicate := seen[key]; duplicate {
			// ONE SUBJECT CANNOT BE BOTH OPERANDS. Publishing it twice would
			// present a comparison of a thing with itself as a completed
			// answer; dropping one side would silently turn a two-operand
			// question into a one-operand one.
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

// comparisonSlotCount is the operand count this cut publishes. It mirrors the
// classifier's own cut constant; both are checked because they are reached by
// different callers and a run can be assembled by a caller that never ran the
// classifier.
const comparisonSlotCount = 2

// publishComparisonResolution is the SOLE conversion from this internal run to
// the existing published resolution fields. There is no other writer, so there
// is no second place for the hold to be forgotten.
//
// ON A HOLD it publishes an EMPTY committed set and NO committed-subject
// digests, while preserving the authorized candidates and their real receipt
// identities -- the candidates are what a user selects from, and inventing or
// dropping receipt ids would break the follow-up that completes the answer.
func publishComparisonResolution(run comparisonResolutionRun, budget int) (contextfabric.SubjectResolution, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet) {
	resolution := contextfabric.SubjectResolution{
		Candidates: combineSlotCandidates(run, budget),
		Committed:  []contextfabric.SubjectRef{},
	}
	bases := contextfabric.CommitBasisSet{}
	digests := contextfabric.CommitDecisionDigestSet{}

	if !run.publishable() {
		// THE HOLD. One clarification, naming both operands, in the EXISTING
		// prompt field -- no new field, no new token.
		resolution.ClarificationPrompt = comparisonClarificationPrompt(run)
		return resolution, bases, digests
	}

	for _, slot := range run.slots {
		subject := slot.committed[0]
		resolution.Committed = append(resolution.Committed, subject)
		// FIRST WRITER WINS, BY SUBJECT KEY. The union is what stops the
		// second slot's proof map overwriting the first's for a subject both
		// happened to see; the published basis must describe the commit that
		// actually released the subject.
		key := contextfabric.SubjectMapKey(subject)
		if _, exists := bases[key]; !exists {
			bases.Record(subject, slot.bases.For(subject))
		}
		if _, exists := digests[key]; !exists {
			digests.Record(subject, slot.digests.For(subject))
		}
	}
	return resolution, bases, digests
}

// combineSlotCandidates flattens the per-slot candidate lists into the single
// published list, in SLOT ORDER, under the existing global candidate budget.
//
// THE BUDGET IS SPENT FAIRLY, ROUND-ROBIN ACROSS SLOTS, not first-slot-first.
// A first slot with many rivals would otherwise consume the whole budget and
// leave the second operand with no candidates at all -- and the user would be
// asked to choose for one side of a comparison whose other side had silently
// vanished from the offer.
//
// Within a slot the existing confidence/key total order is retained: this
// function never re-sorts a slot's list, it only interleaves them.
func combineSlotCandidates(run comparisonResolutionRun, budget int) []contextfabric.SubjectCandidate {
	combined := make([]contextfabric.SubjectCandidate, 0)
	if len(run.slots) == 0 {
		return combined
	}
	seen := make(map[string]struct{})
	longest := 0
	for _, slot := range run.slots {
		if len(slot.candidates) > longest {
			longest = len(slot.candidates)
		}
	}
	for depth := 0; depth < longest; depth++ {
		for _, slot := range run.slots {
			if depth >= len(slot.candidates) {
				continue
			}
			candidate := slot.candidates[depth]
			key := SubjectKey(candidate.Subject)
			if _, duplicate := seen[key]; duplicate {
				// One subject proposed by both slots appears ONCE. It is the
				// same subject and the same receipt identity; listing it twice
				// would offer the user the same choice under two entries.
				continue
			}
			seen[key] = struct{}{}
			combined = append(combined, candidate)
			if budget > 0 && len(combined) == budget {
				return combined
			}
		}
	}
	return combined
}

// ---------------------------------------------------------------------------
// THE CLARIFICATION
// ---------------------------------------------------------------------------

// comparisonPromptMaxRunes mirrors the EXISTING published bound on
// SubjectResolution.ClarificationPrompt, which the contract validates in RUNES
// (utf8.RuneCountInString), not bytes. This work does not widen it.
//
// Stated in runes here for the same reason the validator counts them: a
// byte-budgeted prompt would truncate a multibyte label mid-rune and produce a
// string the validator then rejects for a reason that looks unrelated.
const comparisonPromptMaxRunes = 2000

// comparisonOperandNameMaxRunes bounds ONE operand's name inside the prompt.
//
// Bounded per NAME, not just in total, and this is the load-bearing half. The
// action and both descriptions have to survive together; budgeting only the
// whole string would let one very long label consume the room the second
// operand's description and the action need, and the user would be shown half
// a question that fits.
const comparisonOperandNameMaxRunes = 120

// comparisonClarificationPrompt renders ONE clarification naming BOTH operands.
//
// A TEMPLATE, NOT FIXED PROSE. It names operand one and operand two, each with
// one bounded name -- a resolved side by its authorized canonical label, an
// unresolved side by its own current frame term -- each side's state, and the
// action required to complete the comparison. Where a side has no candidate at
// all the selection instruction becomes a RESTATE instruction, because there is
// nothing to select from and asking someone to choose from an empty set is
// worse than asking them to say it again.
//
// SPACE FOR BOTH DESCRIPTIONS AND THE ACTION IS RESERVED FIRST. Any optional
// explanation is appended only if it still fits, so the part that lets the user
// finish is never the part that gets truncated.
//
// THERE IS NO MACHINE-READABLE SLOT-TO-CANDIDATE MAPPING PROMISED HERE, and
// none is added: operand identity reaches the human through these descriptions.
// A consumer that needed to know which candidate belongs to which operand would
// need a wire change, which this work does not make.
func comparisonClarificationPrompt(run comparisonResolutionRun) string {
	descriptions := make([]string, 0, len(run.slots))
	for index, slot := range run.slots {
		descriptions = append(descriptions, comparisonSlotDescription(index, slot))
	}

	action := comparisonPromptAction(run)
	prompt := strings.TrimSpace(strings.Join(descriptions, " ") + " " + action)

	// The optional explanation, appended ONLY if the required part left room.
	if run.unboundReceipts > 0 {
		explanation := "Your previous selection could not be associated with exactly one of these, so it has not completed either."
		if utf8.RuneCountInString(prompt)+1+utf8.RuneCountInString(explanation) <= comparisonPromptMaxRunes {
			prompt = prompt + " " + explanation
		}
	}
	return truncateRunes(prompt, comparisonPromptMaxRunes)
}

// comparisonSlotDescription renders ONE operand: its ordinal, its one bounded
// name, and its state.
func comparisonSlotDescription(index int, slot operandSlotRun) string {
	ordinal := "The first"
	if index > 0 {
		ordinal = "The second"
	}
	name := comparisonSlotName(slot)
	switch slot.state() {
	case operandSlotResolved:
		return ordinal + " is " + name + "."
	case operandSlotScoped:
		return ordinal + " asks for a group under " + name + ", which cannot be compared in this form."
	case operandSlotNoCandidate:
		return ordinal + ", " + name + ", matched nothing."
	case operandSlotWrongKind:
		return ordinal + ", " + name + ", matched a subject of a different kind than the question asked for."
	case operandSlotOverCommitted:
		return ordinal + ", " + name + ", matched more than one subject at once."
	}
	return ordinal + ", " + name + ", matches more than one subject."
}

// comparisonSlotName is the ONE bounded name a side is described by: its
// authorized canonical label when it resolved, its own current frame term
// otherwise.
//
// NEVER THE WHOLE QUESTION, and never another slot's term. A description
// assembled from the question text would put provenance the slot never had
// into the sentence that names it.
func comparisonSlotName(slot operandSlotRun) string {
	if len(slot.committed) == 1 && strings.TrimSpace(slot.committed[0].Label) != "" {
		return quoteBounded(slot.committed[0].Label)
	}
	for _, term := range slot.slot.Terms {
		if strings.TrimSpace(term) != "" {
			return quoteBounded(term)
		}
	}
	return "the unnamed side"
}

// comparisonPromptAction is the required action, chosen deterministically from
// the slot states rather than from the first interesting one found.
func comparisonPromptAction(run comparisonResolutionRun) string {
	var states []operandSlotState
	for _, slot := range run.slots {
		states = append(states, slot.state())
	}
	has := func(want operandSlotState) bool {
		for _, state := range states {
			if state == want {
				return true
			}
		}
		return false
	}
	switch {
	case has(operandSlotScoped):
		return "Name a single subject for that side, or ask about the group on its own."
	case has(operandSlotNoCandidate):
		// RESTATE, not select: there is nothing to select from.
		return "Restate that side and ask again."
	case has(operandSlotWrongKind):
		return "Name a subject of the kind the question asks about, and ask again."
	case has(operandSlotAmbiguous), has(operandSlotOverCommitted):
		return "Say which one you mean for that side, and both will be compared together."
	}
	return "Ask again naming both subjects."
}

// quoteBounded renders one name, bounded, in quotes.
func quoteBounded(value string) string {
	return `"` + truncateRunes(strings.TrimSpace(value), comparisonOperandNameMaxRunes) + `"`
}

// truncateRunes cuts to a RUNE budget, never a byte budget, so a multibyte
// label cannot be split mid-rune into a string the contract validator then
// rejects.
func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit]))
}

// resolveNamedComparison resolves an admitted operand pair, each operand from
// ITS OWN terms, and decides whether publication may proceed.
//
// SLOT ISOLATION IS THE WHOLE MECHANISM. Every slot gets its own candidate
// map, its own observation maps, its own vector-similarity side map and its own
// identity-claim recorders. Nothing crosses: neither the whole-question term
// bag nor the other operand's terms can enter a slot's identity pool, which is
// exactly what stops two well-posed operands collapsing into one ambiguity.
//
// THE SIX SINGLETON COMMIT GATES STAY SINGLETON. This function does not widen
// them, delete them, or turn their assignments into appends. It calls the
// existing gate ONCE PER OPERAND, on that operand's own pool -- so each
// invocation still decides about exactly one subject, with every guard it has
// today intact, and the pair-ness lives out here instead of being pushed down
// into a gate that was never asked to hold two.
//
// NO READS HAPPEN HERE. The evidence round and the census callbacks are simply
// never invoked on this path, and that absence IS the no-read hold: a
// comparison that cannot publish must not have read anything to publish about.
func resolveNamedComparison(
	ctx context.Context,
	principal storage.Principal,
	request contextfabric.InvestigationRequest,
	deps ResolveDeps,
	comparison contextfabric.ComparisonOperands,
) (contextfabric.SubjectResolution, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	run := comparisonResolutionRun{admission: comparison.Admission}

	// THE SCOPED HOLD FIRES BEFORE ANY RETRIEVAL. Not after a failed attempt to
	// resolve the anchor, and not after reading anything: the pair is refused
	// on its VARIANT, which is knowable from the frame alone. Admitting it
	// would flip the whole answer into the absorbing degraded state, so a
	// limitation this resolver introduced would reach the user looking like a
	// data problem.
	if comparison.Admission == contextfabric.ComparisonHeldScopedOperand {
		for _, slot := range comparison.Slots {
			run.slots = append(run.slots, operandSlotRun{slot: slot})
		}
		resolution, bases, digests := publishComparisonResolution(run, request.Options.MaxSubjectCandidates)
		return resolution, bases, digests, nil
	}

	for _, slot := range comparison.Slots {
		if err := ctx.Err(); err != nil {
			return contextfabric.SubjectResolution{}, nil, nil, err
		}
		slotRun, err := resolveOneOperandSlot(ctx, principal, request, deps, slot)
		if err != nil {
			return contextfabric.SubjectResolution{}, nil, nil, err
		}
		run.slots = append(run.slots, slotRun)
	}

	// A LAST CANCELLATION CHECK BEFORE PUBLICATION. A context cancelled after
	// the final slot resolved but before anything was published must publish
	// NOTHING -- a partially assembled comparison escaping on a cancelled
	// request is the one outcome that would be both wrong and hard to see.
	if err := ctx.Err(); err != nil {
		return contextfabric.SubjectResolution{}, nil, nil, err
	}

	resolution, bases, digests := publishComparisonResolution(run, request.Options.MaxSubjectCandidates)
	return resolution, bases, digests, nil
}

// resolveOneOperandSlot retrieves and decides ONE operand, in isolation.
func resolveOneOperandSlot(
	ctx context.Context,
	principal storage.Principal,
	request contextfabric.InvestigationRequest,
	deps ResolveDeps,
	slot contextfabric.ComparisonOperandSlot,
) (operandSlotRun, error) {
	// FRESH STATE, PER SLOT. Allocated here rather than passed in, so there is
	// no way for a caller to accidentally share one operand's pool with the
	// other's -- the isolation is structural, not a convention someone has to
	// remember at the call site.
	candidatesBySubject := make(map[string]contextfabric.SubjectCandidate)
	observationParentKey := make(map[string]string)
	observationBlocked := make(map[string]bool)
	vectorArmSimilarity := make(map[string]float64)
	identity := identityClaimants{}
	identityTerms := identityMatchTerms{}

	// THIS SLOT'S OWN TERMS. Never SubjectTerms(request, interpreted) -- that
	// is the flat bag whose existence is the defect.
	retrieval, err := retrieveCandidatesForTerms(ctx, principal, request, deps, slot.Terms,
		candidatesBySubject, observationParentKey, observationBlocked, vectorArmSimilarity, identity, identityTerms)
	if err != nil {
		return operandSlotRun{}, err
	}

	gate := deps.CommitGatePolicy
	if gate == (CommitGatePolicy{}) {
		// Same reasoning as the single-subject path: a zero-valued policy means
		// "not overridden", and passing it straight through would run an
		// unconfigured backend on a zero-threshold auto-commit-everything gate.
		gate = DefaultCommitGatePolicy()
	}
	effectiveSearchLimit := request.Options.MaxSubjectCandidates
	if deps.MaxResultsCap > 0 && (effectiveSearchLimit <= 0 || effectiveSearchLimit > deps.MaxResultsCap) {
		effectiveSearchLimit = deps.MaxResultsCap
	}

	// aliasIdentityComplete is FALSE for a slot, deliberately and
	// conservatively. It is a claim that a keyed identity read enumerated the
	// whole population, and no such read has run for this operand's terms on
	// this path. False cannot make anything commit that otherwise would not --
	// it only withholds the identity fast path's completeness bump, which is
	// the safe direction for a claim nobody has proven here.
	//
	// evidenceCensusAttestedKey is "" and the census is never invoked: that is
	// the no-read hold, stated as an absence rather than a flag.
	//
	// reservedKinds is THIS SLOT'S OWN stated kind. The question stated it, so
	// a candidate of that kind must not vanish from this operand's own list
	// under truncation -- reserving the pair's other kind here would be
	// meaningless, since the other operand has its own invocation.
	resolution, bases, digests := ResolveFromMergedCandidatesWithGateAndBasis(
		candidatesBySubject, observationParentKey, observationBlocked,
		request.Options.MaxSubjectCandidates, request.Options.AllowClarification,
		retrieval.searchTruncated, vectorArmSimilarity, deps.VectorMarginCommitThreshold,
		retrieval.retrievalDegraded, effectiveSearchLimit, deps.CalibratedTopK,
		unscopedVisibilityFor(principal, request), gate, identity, identityTerms,
		false, deps.ResolutionTracer, request.RequestID, "", false, false,
		[]contextfabric.SubjectKind{slot.Kind},
	)

	return operandSlotRun{
		slot:              slot,
		candidates:        resolution.Candidates,
		committed:         resolution.Committed,
		bases:             bases,
		digests:           digests,
		retrievalDegraded: retrieval.retrievalDegraded,
	}, nil
}
