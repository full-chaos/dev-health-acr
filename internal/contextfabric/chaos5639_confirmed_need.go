package contextfabric

// CHAOS-5639 (A3): a confirmed structure need persists across turns without
// re-echo.
//
// THE DESIGN (dictation 1387, stacked on CHAOS-5465 M2): a per-need
// confirmation ledger keyed by M2's request-identity digest, invalidated by
// M2's identity signals, persisted on M2's semantic-state row (no second
// carrier), consulted by every need gate before re-raising. No wire field.
//
// WHAT THIS IS NOT. It is not CHAOS-5465's window-only continuation
// (chaos5465_window_continuation.go): that mechanism applies only to the
// narrow shape D-d defines -- one window receipt, no other prior-result
// reference, no added kind/anchor/handle/candidate selection -- and a turn
// that just redeemed a structure receipt is explicitly OUTSIDE that shape
// (D-d: "a window receipt riding along with a subject/kind/anchor/handle/
// candidate selection is that selection's turn, not a continuation"). This
// file computes its OWN admission, beside windowContinuationDecision and
// independent of it, using the SAME identity recipe (SemanticRequestIdentityOf,
// unchanged) PLUS the same-question containment every other carry mechanism
// in this package enforces (continuationQuestionIdentity,
// carryOriginSameQuestionVerdict) -- the digest alone covers scope, the seven
// answer-shaping options and the conversation, but never request.Question
// itself, so an equal digest alone proves nothing about whether this is the
// same question: a caller that passes no conversation array at all (an
// ordinary shape for a turn that only names a parent) reduces the digest to
// scope and options alone, which any result in the org could share. This
// mechanism applies to a different, broader set of turns than M2's own
// continuation: any turn that names a parent result via
// request.ParentResultID.
//
// ONE HOP, like D-a: only the DIRECTLY named parent's ledger is read, never
// an older ancestor.
//
// HOW A REMEMBERED CONFIRMATION TAKES EFFECT, and how it does NOT.
// appliedNeedLedgerEntries is the ONE function that decides what actually
// applies -- every consumer (telemetry, resolution, wire disclosure) reads
// its output rather than re-deriving "does this apply" independently, so none
// of them can report a different answer than the others: telemetry can never
// say "none" while a resolution parameter applies a value anyway.
//
// ONLY expected_kind AND subject_anchor ARE APPLIED (subject_handle,
// subject_candidate and window are persisted for completeness -- nothing
// consults them yet). BOTH are admitted through exactly the checks a fresh
// carry or receipt already passes, never a ledger-specific substitute:
// expected_kind carries no live tampering vector, so a stale-epoch-checked,
// same-question, identity-equal remembered value is exactly as trustworthy
// as a receipt (canonicalizeStructure's own reverify-field doc comment,
// structure.go -- "the confirmed kind only narrows a pool, it never stands
// in for a fact"). subject_anchor is different -- a rival can gain the same
// alias, or lose membership, between the offer and any later turn -- so
// resolveConfirmedNeedLedger reverifies it through reverifyAnchorClaim, the
// SAME dispatch (AnchorVerifier v1 / AnchorMembershipVerifier v2, on the
// carrier's OWN schema_version) canonicalizeStructure's own ancr_ redemption
// already uses, carrying the redeemed offer's own matched_term_hash forward
// in ConfirmedNeedEntry for exactly that replay. A claim that fails or
// cannot be reverified is dropped from the ledger -- never the whole
// ledger, only that member.
//
// A remembered expected_kind is NOT threaded into effectiveConfirmedKind the
// same way. It is checked FIRST inside Engine.resolveCarriedKind itself
// (structure_axis_carry.go) -- the kind axis's own gated producer -- so it
// flows through the exact gates a receipt-derived carry already has to
// survive: statedExpectedKindThisTurn (a kind the caller stated THIS turn, by
// receipt or explicitly, is never argued with, checked by resolveCarriedKind's
// caller before resolveCarriedKind is even invoked) and applyCarryDrop (a
// subject-axis receipt naming a different kind stands a carried value down,
// applied uniformly to whatever resolveCarriedKind returns). A parallel
// precedence rule ahead of both gates, in a helper outside resolveCarriedKind,
// would let a remembered kind override the caller's own explicit statement
// THIS turn and survive a disagreement the legacy carry mechanism would
// otherwise drop -- checking the ledger inside resolveCarriedKind itself is
// what keeps that impossible.

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ConfirmedNeedLedgerOutcome is the closed, content-safe vocabulary the
// per-need confirmation ledger's consult reports -- ONE decision per
// Investigate call, covering every member the ledger might hold at once: the
// identity comparison is a single admission, not a per-member one.
type ConfirmedNeedLedgerOutcome string

const (
	// ConfirmedNeedLedgerMissNoReference: this request names no parent
	// result at all. The baseline population.
	ConfirmedNeedLedgerMissNoReference ConfirmedNeedLedgerOutcome = "miss_no_reference"
	// ConfirmedNeedLedgerMissUnloadable: the named parent could not be read
	// (a missing row, a store error, or no InvestigationResultStore at all).
	ConfirmedNeedLedgerMissUnloadable ConfirmedNeedLedgerOutcome = "miss_unloadable"
	// ConfirmedNeedLedgerMissEmpty: the parent loaded and its semantic state
	// is available, but its ledger holds no confirmed need at all.
	ConfirmedNeedLedgerMissEmpty ConfirmedNeedLedgerOutcome = "miss_empty"
	// ConfirmedNeedLedgerDroppedQuestionIndeterminate: one of the two
	// questions (this turn's, or the parent's) has no identity to compare --
	// a question consisting only of terminal punctuation canonicalizes to
	// the empty string, and every such question shares one hash. Mirrors
	// carryOriginIndeterminateQuestion (chaos4360_carry.go): not proven
	// same, not proven different.
	ConfirmedNeedLedgerDroppedQuestionIndeterminate ConfirmedNeedLedgerOutcome = "dropped_question_indeterminate"
	// ConfirmedNeedLedgerDroppedQuestionChanged: the parent answered a
	// DIFFERENT question. The request-identity digest never covers
	// request.Question itself (it exists to cover the inputs nothing else
	// records), so this check is required beside it, not implied by it.
	ConfirmedNeedLedgerDroppedQuestionChanged ConfirmedNeedLedgerOutcome = "dropped_question_changed"
	// ConfirmedNeedLedgerDroppedIdentityIncomparable: this turn's own
	// identity, or the carrier's recorded one, cannot be compared under
	// today's recipe (a version mismatch, or a snapshot that never computed
	// one). Fail-closed: never treated as a match.
	ConfirmedNeedLedgerDroppedIdentityIncomparable ConfirmedNeedLedgerOutcome = "dropped_identity_incomparable"
	// ConfirmedNeedLedgerDroppedIdentityChanged: both identities are
	// comparable and they disagree -- the caller changed something the
	// digest covers. The ledger is dropped whole; a changed question earns a
	// fresh need, not a partially-remembered one.
	ConfirmedNeedLedgerDroppedIdentityChanged ConfirmedNeedLedgerOutcome = "dropped_identity_changed"
	// ConfirmedNeedLedgerDroppedStaleGraphEpoch: the parent loaded and its
	// ledger is non-empty, but the parent carries a DIFFERENT graph epoch (or
	// none at all) than THIS turn's own binding -- the SAME CHAOS-3898 §2.2
	// ingress taint gate walkCarriedKind applies to the legacy chain walk,
	// reused here rather than re-implemented: a rebuild between turns can
	// change what a remembered kind or anchor even denotes, so a carrier from
	// another epoch is refused outright, exactly like every other carry axis
	// in this package.
	ConfirmedNeedLedgerDroppedStaleGraphEpoch ConfirmedNeedLedgerOutcome = "dropped_stale_graph_epoch"
	// ConfirmedNeedLedgerHit: the same question, with a comparable and equal
	// identity, at the SAME graph epoch. The ledger's entries are available
	// to this turn.
	ConfirmedNeedLedgerHit ConfirmedNeedLedgerOutcome = "hit"
)

func confirmedNeedLedgerOutcomes() []ConfirmedNeedLedgerOutcome {
	return []ConfirmedNeedLedgerOutcome{
		ConfirmedNeedLedgerMissNoReference, ConfirmedNeedLedgerMissUnloadable,
		ConfirmedNeedLedgerMissEmpty, ConfirmedNeedLedgerDroppedQuestionIndeterminate,
		ConfirmedNeedLedgerDroppedQuestionChanged, ConfirmedNeedLedgerDroppedIdentityIncomparable,
		ConfirmedNeedLedgerDroppedIdentityChanged, ConfirmedNeedLedgerDroppedStaleGraphEpoch,
		ConfirmedNeedLedgerHit,
	}
}

// ValidConfirmedNeedLedgerOutcome reports membership.
func ValidConfirmedNeedLedgerOutcome(value ConfirmedNeedLedgerOutcome) bool {
	for _, member := range confirmedNeedLedgerOutcomes() {
		if member == value {
			return true
		}
	}
	return false
}

// confirmedNeedLedgerResult is resolveConfirmedNeedLedger's own return: the
// admission outcome, and -- only on a hit -- the ledger's entries (in the
// same shape a redeemed receipt produces) and the id of the result they came
// from, for disclosure.
type confirmedNeedLedgerResult struct {
	Outcome        ConfirmedNeedLedgerOutcome
	Entries        []confirmedStructureMember
	SourceResultID string
}

// resolveConfirmedNeedLedger is CHAOS-5639's own need-gate admission. It
// reads the ONE parent this request names (request.ParentResultID -- the
// general "this turn continues that one" bearer field, D-d's own phrase),
// and admits its ledger only when (a) the parent's snapshot decoded cleanly,
// (b) the parent was saved at THIS turn's own graph epoch, (c) this turn asks
// the SAME question the parent answered, and (d) this turn's recomputed
// request identity (SemanticRequestIdentityOf, the referenced exchange
// dropped exactly as D-a's own comparison drops it) equals the identity the
// parent's own snapshot recorded.
//
// Uses carryLoadResult (chaos4360_carry.go) so a call already made for the
// SAME result id within this Investigate call costs no second store round
// trip; window-only continuations name a DIFFERENT id (D-d requires
// ParentResultID empty for that shape), so the two mechanisms never share a
// cache hit, only the cache itself.
func (e *Engine) resolveConfirmedNeedLedger(ctx context.Context, principal storage.Principal, request InvestigationRequest, binding ResolvedGraphBinding) confirmedNeedLedgerResult {
	parent := carryParentSeed(request)
	if parent == "" {
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerMissNoReference}
	}
	if e.results == nil {
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerMissUnloadable}
	}
	stored, err := carryLoadResult(ctx, e.results, principal, parent)
	if err != nil {
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerMissUnloadable}
	}
	// UNLOADABLE (malformed/oversized/unsupported/unreported) is a DIFFERENT
	// fact than EMPTY (a clean read that simply has no ledger): an operator
	// who can only see "empty" for both cannot tell a live storage/decode
	// defect apart from the ordinary "nothing confirmed yet" case. Only a
	// clean, available read falls through to the empty check below.
	if stored.SemanticStateRead != SemanticStateReadAvailable {
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerMissUnloadable}
	}
	if stored.SemanticState == nil || len(stored.SemanticState.ConfirmedNeeds) == 0 {
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerMissEmpty}
	}
	// CHAOS-3898 §2.2 ingress taint gate -- IDENTICAL check walkCarriedKind
	// applies to the legacy chain walk (structure_axis_carry.go). A rebuild
	// between turns can change what a remembered kind or anchor even
	// denotes, so a carrier from another epoch is refused outright rather
	// than trusted partially.
	if stored.GraphEpoch == nil || *stored.GraphEpoch != binding.Epoch {
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerDroppedStaleGraphEpoch}
	}
	// THE SAME-QUESTION CONTAINMENT: the digest below never covers
	// request.Question, so equal digests prove nothing about whether this is
	// the same question. Enforced through the ONE choke point every other
	// carry axis in this package uses (carryOriginSameQuestionVerdict,
	// chaos4360_carry.go) rather than a second, hand-rolled comparison -- a
	// duplicated check is a second place for the two to drift apart. Reads
	// through the SAME per-request load memo `stored` above already
	// populated, so this costs no second store round trip.
	switch e.carryOriginSameQuestionVerdict(ctx, principal, request, parent) {
	case carryOriginSameQuestion:
	case carryOriginIndeterminateQuestion:
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerDroppedQuestionIndeterminate}
	case carryOriginDrifted:
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerDroppedQuestionChanged}
	default: // carryOriginUnverifiable
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerMissUnloadable}
	}
	identity := SemanticRequestIdentityOf(request, stored.Result.Question)
	carried := stored.SemanticState.RequestIdentity
	if !identity.Comparable() || !carried.Comparable() {
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerDroppedIdentityIncomparable}
	}
	if !identity.Equal(carried) {
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerDroppedIdentityChanged}
	}
	entries := make([]confirmedStructureMember, 0, len(stored.SemanticState.ConfirmedNeeds))
	for _, entry := range stored.SemanticState.ConfirmedNeeds {
		// SUBJECT_ANCHOR REDEMPTION-TIME REVERIFY: a remembered anchor gets
		// no other check the way a fresh ancr_ receipt gets one from
		// canonicalizeStructure's own reverify wiring -- this IS that check,
		// replayed through the SAME reverifyAnchorClaim dispatch (structure.go)
		// on the SAME (kind, canonical_id, matched_term_hash) triple, against
		// the carrier's OWN schema_version. A claim that fails, or that this
		// deployment has no verifier wired for, is dropped from the ledger --
		// never the whole ledger, only this one member, exactly as a fresh
		// redemption's own failure vetoes only its own member.
		if entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			if !e.reverifyAnchorClaim(ctx, principal, request.RequestedScope, binding, stored.Result.SchemaVersion, entry.AppliedKind, entry.AppliedValue, entry.MatchedTermHash) {
				continue
			}
		}
		entries = append(entries, confirmedStructureMember{
			Member: entry.Member, AppliedKind: entry.AppliedKind, AppliedValue: entry.AppliedValue,
			MatchedTermHash: entry.MatchedTermHash,
		})
	}
	return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerHit, Entries: entries, SourceResultID: parent}
}

// appliedNeedLedgerEntries is CHAOS-5639's SINGLE authority for "does this
// remembered entry actually apply this turn" -- every consumer
// (Engine.resolveCarriedKind, confirmedAnchorSelection, composeCarriedNeedEntry,
// telemetry) reads this map rather than re-deriving the check, so none of
// them can disagree about what applied: a member excluded here is excluded
// everywhere, including telemetry, and one included here is included in
// whatever narrows resolution.
//
// An entry applies when: it names one of the two members with a resolution
// parameter to reach (expected_kind, subject_anchor -- subject_handle,
// subject_candidate and window are stored for completeness but nothing
// consults them yet); it carries a non-empty value; and this turn's OWN
// receipts (confirmedThisTurn) did not already confirm that member -- a real
// receipt this turn always wins and is never argued with.
//
// BOTH members are safe to admit here because BOTH already passed every
// check a fresh carry or receipt passes before ever reaching `remembered`:
// resolveConfirmedNeedLedger (this file) drops a stale-epoch entry outright,
// and reverifies subject_anchor specifically through reverifyAnchorClaim --
// the SAME verifier dispatch canonicalizeStructure's own ancr_ redemption
// uses -- dropping it alone (never the whole ledger) on a failed or
// unverifiable claim. expected_kind needs no such reverify: it carries no
// live tampering vector (canonicalizeStructure's own reverify-field doc
// comment, structure.go -- "the confirmed kind only narrows a pool, it never
// stands in for a fact").
func appliedNeedLedgerEntries(remembered []confirmedStructureMember, confirmedThisTurn []confirmedStructureMember) map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember {
	thisTurn := map[contractsv1.ContextFabricStructureNeedKind]bool{}
	for _, c := range confirmedThisTurn {
		thisTurn[c.Member] = true
	}
	out := map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember{}
	for _, r := range remembered {
		switch r.Member {
		case contractsv1.ContextFabricStructureNeedExpectedKind, contractsv1.ContextFabricStructureNeedSubjectAnchor:
		default:
			continue
		}
		if r.AppliedValue == "" || thisTurn[r.Member] {
			continue
		}
		out[r.Member] = r
	}
	return out
}

// appliedNeedLedgerMembers renders applied's keys for telemetry, in the
// StructureNeedKind vocabulary's own fixed order (never map order, for a
// deterministic log line).
func appliedNeedLedgerMembers(applied map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember) []contractsv1.ContextFabricStructureNeedKind {
	var out []contractsv1.ContextFabricStructureNeedKind
	for _, member := range contractsv1.ContextFabricStructureNeedKindVocabulary() {
		if _, ok := applied[member]; ok {
			out = append(out, member)
		}
	}
	return out
}

// mergeConfirmedNeedsLedger builds the OUTGOING per-need ledger for a
// result's own semantic_state (CHAOS-5639): this turn's own receipt-confirmed
// members win over an inherited entry for the SAME member; every other
// inherited member (the ledger this turn's own admission check admitted)
// carries forward unchanged. Iterated in the StructureNeedKind vocabulary's
// own fixed order, never map order, so the canonical encoding
// (EncodeSemanticState) is deterministic across two calls that resolve to the
// same set.
func mergeConfirmedNeedsLedger(remembered []confirmedStructureMember, confirmedThisTurn []confirmedStructureMember) []ConfirmedNeedEntry {
	byMember := map[contractsv1.ContextFabricStructureNeedKind]ConfirmedNeedEntry{}
	for _, r := range remembered {
		byMember[r.Member] = ConfirmedNeedEntry{Member: r.Member, AppliedKind: r.AppliedKind, AppliedValue: r.AppliedValue, MatchedTermHash: r.MatchedTermHash}
	}
	for _, c := range confirmedThisTurn {
		byMember[c.Member] = ConfirmedNeedEntry{Member: c.Member, AppliedKind: c.AppliedKind, AppliedValue: c.AppliedValue, MatchedTermHash: c.MatchedTermHash}
	}
	var out []ConfirmedNeedEntry
	for _, member := range contractsv1.ContextFabricStructureNeedKindVocabulary() {
		if entry, ok := byMember[member]; ok {
			out = append(out, entry)
		}
	}
	return out
}

// withoutSupersededConfirmedNeeds drops every member the atomic (org,
// prior_result_id, member) supersession claim just REFUSED (CHAOS-3927 P4:
// ErrStructureOfferSuperseded.Members, ports.go) from the ledger about to be
// captured onto the veto terminal Save persists instead.
//
// WHY THIS EXISTS. confirmedNeedsForCapture is computed once, before Save is
// even attempted, from structureCanon.Confirmed -- the members this turn's
// OWN receipts claimed. When Save's atomic claim on one of those members
// loses the race, structureSupersessionVetoResult persists the SAME
// captureAcceptedReading value the decisive path would have: recordStructureConfirmationOutcome's
// own doc comment states the invariant this must not violate --
// "structureCanon.Confirmed genuinely won its claim" is proved ONLY by Save
// succeeding. A receipt whose claim just lost is caller authority nothing won;
// persisting it into the ledger unchanged would let a LATER turn, naming this
// veto result as parent, admit expected_kind (or subject_anchor) as if the
// claim it was refused for had actually been confirmed.
func withoutSupersededConfirmedNeeds(entries []ConfirmedNeedEntry, superseded []contractsv1.ContextFabricStructureNeedKind) []ConfirmedNeedEntry {
	if len(superseded) == 0 {
		return entries
	}
	drop := map[contractsv1.ContextFabricStructureNeedKind]bool{}
	for _, member := range superseded {
		drop[member] = true
	}
	var out []ConfirmedNeedEntry
	for _, entry := range entries {
		if drop[entry.Member] {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// captureConfirmedNeedLedgerOnly builds the semantic-state capture for a
// turn that ends BEFORE Interpret (a window veto, the explicit-window
// confirmation-required gate, or a structure veto): a minimal snapshot
// carrying this turn's own request identity and outgoing per-need ledger,
// so a later turn naming one of THESE exits as its direct parent -- the
// ordinary shape for a window-confirmation redemption -- still finds a
// ledger to inherit, exactly as it would naming any other parent.
//
// WHY THIS EXISTS. resolveConfirmedNeedLedger's own ONE HOP rule (this
// file's header comment) requires the DIRECT parent to carry an available
// semantic state; a parent whose own state is absent gives the next turn
// nothing to inherit, whatever it once confirmed. A mechanical
// window-confirmation step is not a new question -- persisting only an
// absence there would break the chain on a turn the caller never changed
// anything about.
//
// WHY THIS IS SAFE UNDER CHAOS-4040. Family=QuestionFamilyUnclassified,
// Source=QuestionFamilySourceNone is the SAME zero-cost, honest "nothing was
// classified" value the package already persists for a turn that ran no
// real interpretation (carriablePlan's own doc comment, chaos4636_plan_carry.go,
// treats an unclassified family exactly as "nothing to carry") -- building
// it here does none of the interpreter/graph/fact/synthesis work CHAOS-4040's
// own run-3 acceptance bar forbids at these three gates: it is a request-shape
// computation (SemanticRequestIdentityOf) and a slice merge
// (mergeConfirmedNeedsLedger), nothing more.
func (e *Engine) captureConfirmedNeedLedgerOnly(request InvestigationRequest, remembered []confirmedStructureMember, confirmedThisTurn []confirmedStructureMember) semanticStateCapture {
	return captureSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(request, ""),
		ConfirmedNeeds:  mergeConfirmedNeedsLedger(remembered, confirmedThisTurn),
	})
}

// composeCarriedNeedEntry composes the wire disclosure for a remembered
// subject_anchor this turn actually applied -- the anchor-axis analogue of
// composeCarriedKindEntry/composeCarriedWindowEntry, so a remembered
// confirmation is never a "silent" carry, the discipline both of those
// functions' own doc comments hold. NOT used for expected_kind: a remembered
// kind is checked inside Engine.resolveCarriedKind itself and disclosed
// through composeCarriedKindEntry already -- composing a second entry for
// the same member here would violate the v1 result validator's "one entry
// per member" rule.
func composeCarriedNeedEntry(member contractsv1.ContextFabricStructureNeedKind, applied map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember, sourceResultID string) *contractsv1.ContextFabricConfirmedStructureEntry {
	entry, ok := applied[member]
	if !ok {
		return nil
	}
	return &contractsv1.ContextFabricConfirmedStructureEntry{
		Member: member, AppliedValue: entry.AppliedValue,
		Source:        contractsv1.ContextFabricStructureSourceCarried,
		PriorResultID: sourceResultID,
		Provenance:    contractsv1.ContextFabricStructureClarificationConfirmed,
		Disposition:   contractsv1.ContextFabricStructureDispositionApplied,
	}
}

// observableAppliedNeedMembers renders appliedMembers for the log line, with
// the explicit "none" token a zero value would otherwise leave
// indistinguishable from a key nobody wrote (mirrors
// declaredKindDecision.ObservableDeclaredKinds' own convention,
// chaos5660_declared_kind_terminal.go).
func observableAppliedNeedMembers(appliedMembers []contractsv1.ContextFabricStructureNeedKind) string {
	if len(appliedMembers) == 0 {
		return "none"
	}
	rendered := ""
	for index, member := range appliedMembers {
		if index > 0 {
			rendered += ","
		}
		rendered += string(member)
	}
	return rendered
}

// recordConfirmedNeedLedger reports CHAOS-5639's own gate decision, once per
// Investigate call: the admission outcome, the parent result id it consulted
// (the SAME correlation handle window_continuation_decision already
// discloses for its own referenced result, never a canonical id or free
// text), and -- only for a member that actually applied -- the closed
// subject-kind value it applied. Both applied kinds are closed, content-safe
// vocabularies (mirrors RecordKindCarry's own carried_kind/redeemed_kind
// pair): "a drop reported without both sides is a decision an operator
// cannot check" applies here exactly as it does there.
func (e *Engine) recordConfirmedNeedLedger(ctx context.Context, principal storage.Principal, ledger confirmedNeedLedgerResult, applied map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember) {
	if e.telemetry == nil {
		return
	}
	var appliedExpectedKind, appliedAnchorKind contractsv1.ContextFabricSubjectKind
	if entry, ok := applied[contractsv1.ContextFabricStructureNeedExpectedKind]; ok {
		appliedExpectedKind = contractsv1.ContextFabricSubjectKind(entry.AppliedValue)
	}
	if entry, ok := applied[contractsv1.ContextFabricStructureNeedSubjectAnchor]; ok {
		appliedAnchorKind = entry.AppliedKind
	}
	e.telemetry.RecordConfirmedNeedLedger(ctx, principal, ledger.Outcome, ledger.SourceResultID, appliedNeedLedgerMembers(applied), appliedExpectedKind, appliedAnchorKind)
}
