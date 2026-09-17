package contextfabric

// CHAOS-5639 (A3): a confirmed structure need persists across turns without
// re-echo. CHAOS-5734 completes the consumers for subject_candidate,
// subject_handle and window.
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
// WHAT A REMEMBERED CONFIRMATION DOES: exactly what the fresh receipt it
// stands in for does, and no more. The ledger exists so a caller never has to
// re-echo a receipt it already redeemed, so a remembered entry reaches the
// consumers that receipt reached on the turn it was redeemed:
//
//   - expected_kind narrows the pool, through Engine.resolveCarriedKind
//     (structure_axis_carry.go), so it passes the same gates a carried kind
//     does (statedExpectedKindThisTurn, applyCarryDrop).
//   - subject_anchor becomes the census anchor discriminator
//     (confirmedAnchorSelection).
//   - subject_candidate and subject_handle reach NO resolution parameter:
//     GraphReader.ResolveSubjects takes none for either member, so a fresh
//     candr_/handr_ receipt is disclosed, captured into this ledger, and
//     compared against a carried expected_kind (subjectAxisRedeemedKinds) --
//     nothing else. A remembered entry does those same three things
//     (composeCarriedNeedEntry, mergeConfirmedNeedsLedger,
//     kindCarryComparators). It is not applied as the resolved subject,
//     because a fresh receipt is not.
//   - window applies as the effective evidence window, decided where a fresh
//     winr_ redemption is decided -- request-side, before answer reuse -- so
//     it keys the saved result, meets the axis-conflict veto and stands the
//     window carry down exactly as the receipt does (confirmed_need_window.go).
//
// ADMISSION: every member passes exactly the checks a fresh carry or receipt
// already passes, never a ledger-specific substitute. The ledger as a whole is
// refused for a stale graph epoch, a different question, or a changed or
// incomparable identity. Then each member is reverified through the SAME
// verifier its fresh redemption uses (reverifyRememberedNeed): subject_anchor
// through reverifyAnchorClaim on the carrier's own schema_version,
// subject_candidate through CandidateVerifier, subject_handle through
// HandleVerifier with the offer's own pattern_id. A member that fails, or
// that this deployment cannot reverify, is dropped alone -- never the whole
// ledger -- and the drop is reported with its reason. expected_kind and
// window carry no live tampering vector ("the confirmed kind only narrows a
// pool, it never stands in for a fact"; a window is a time range), so they
// have no reverify, exactly as their fresh redemptions have none.
//
// SUPERSESSION: a same-member receipt this turn, or a same-member value the
// caller states explicitly this turn, always wins over a remembered one --
// in what applies to this turn (appliedNeedLedgerEntries, decideLedgerWindow)
// AND in the outgoing ledger every exit saves (mergeConfirmedNeedsLedger).
// Both read one authority, statedNeedMembers, so a remembered value the caller
// just replaced can never coexist with the replacement or come back on a later
// turn. A receipt whose save-time claim was refused is never persisted into
// the outgoing ledger (withoutSupersededConfirmedNeeds).
//
// A remembered expected_kind is checked FIRST inside Engine.resolveCarriedKind
// itself -- the kind axis's own gated producer -- so it flows through the
// exact gates a receipt-derived carry already has to survive: a kind the
// caller stated THIS turn is never argued with, and a subject-axis receipt
// naming a different kind stands a carried value down. A parallel precedence
// rule in a helper outside resolveCarriedKind would let a remembered kind
// override the caller's own explicit statement this turn -- checking the
// ledger inside resolveCarriedKind itself is what keeps that impossible.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

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

// ConfirmedNeedLedgerOutcomeVocabulary is the closed vocabulary, in
// declaration order, for the telemetry specification to read (CHAOS-5802).
func ConfirmedNeedLedgerOutcomeVocabulary() []ConfirmedNeedLedgerOutcome {
	return confirmedNeedLedgerOutcomes()
}

// ConfirmedNeedMemberDropReason is the closed vocabulary for why ONE member of
// an admitted ledger was dropped at redemption time while the rest of the
// ledger stayed admitted.
type ConfirmedNeedMemberDropReason string

const (
	// ConfirmedNeedMemberDropReverifyUnavailable: this deployment cannot
	// reverify the member -- no verifier wired for it, or the entry lacks the
	// input its verifier needs. Fail-closed, exactly as the member's fresh
	// redemption fails closed on an unwired verifier.
	ConfirmedNeedMemberDropReverifyUnavailable ConfirmedNeedMemberDropReason = "reverify_unavailable"
	// ConfirmedNeedMemberDropReverifyNotConfirmed: the verifier ran and did
	// not confirm the remembered value still holds for this principal at this
	// binding (no longer visible, no longer authorized, no longer resolving,
	// or not checkable against the live graph).
	ConfirmedNeedMemberDropReverifyNotConfirmed ConfirmedNeedMemberDropReason = "reverify_not_confirmed"
)

func confirmedNeedMemberDropReasons() []ConfirmedNeedMemberDropReason {
	return []ConfirmedNeedMemberDropReason{ConfirmedNeedMemberDropReverifyUnavailable, ConfirmedNeedMemberDropReverifyNotConfirmed}
}

// ValidConfirmedNeedMemberDropReason reports membership.
func ValidConfirmedNeedMemberDropReason(value ConfirmedNeedMemberDropReason) bool {
	for _, member := range confirmedNeedMemberDropReasons() {
		if member == value {
			return true
		}
	}
	return false
}

// ConfirmedNeedMemberDropReasonVocabulary is the closed vocabulary, in
// declaration order, for the telemetry specification to read.
func ConfirmedNeedMemberDropReasonVocabulary() []ConfirmedNeedMemberDropReason {
	return confirmedNeedMemberDropReasons()
}

// ConfirmedNeedMemberDrop is one dropped member and why.
type ConfirmedNeedMemberDrop struct {
	Member contractsv1.ContextFabricStructureNeedKind
	Reason ConfirmedNeedMemberDropReason
}

// confirmedNeedLedgerResult is resolveConfirmedNeedLedger's own return: the
// admission outcome and -- only on a hit -- the ledger's surviving entries
// (in the same shape a redeemed receipt produces), the members dropped at
// reverify, and the id of the result they came from, for disclosure.
type confirmedNeedLedgerResult struct {
	Outcome        ConfirmedNeedLedgerOutcome
	Entries        []confirmedStructureMember
	Dropped        []ConfirmedNeedMemberDrop
	SourceResultID string
}

// resolveConfirmedNeedLedger is the need-gate admission. It reads the ONE
// parent this request names (request.ParentResultID -- the general "this
// turn continues that one" bearer field, D-d's own phrase), and admits its
// ledger only when (a) the parent's snapshot decoded cleanly, (b) the parent
// was saved at THIS turn's own graph epoch, (c) this turn asks the SAME
// question the parent answered, and (d) this turn's recomputed request
// identity (SemanticRequestIdentityOf, the referenced exchange dropped exactly
// as D-a's own comparison drops it) equals the identity the parent's own
// snapshot recorded. Each admitted member is then reverified on its own.
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
	var dropped []ConfirmedNeedMemberDrop
	for _, entry := range stored.SemanticState.ConfirmedNeeds {
		if reason, ok := e.reverifyRememberedNeed(ctx, principal, request, binding, stored.Result.SchemaVersion, entry); !ok {
			dropped = append(dropped, ConfirmedNeedMemberDrop{Member: entry.Member, Reason: reason})
			continue
		}
		entries = append(entries, confirmedStructureMember{
			Member: entry.Member, AppliedKind: entry.AppliedKind, AppliedValue: entry.AppliedValue,
			MatchedTermHash: entry.MatchedTermHash, PatternID: entry.PatternID,
			WindowStart: cloneWindowBound(entry.WindowStart), WindowEnd: cloneWindowBound(entry.WindowEnd),
			Basis: entry.Basis,
		})
	}
	return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerHit, Entries: entries, Dropped: dropped, SourceResultID: parent}
}

// reverifyRememberedNeed replays, for ONE remembered member, the redemption-time
// reverify that member's fresh receipt passes in canonicalizeStructure
// (structure.go) -- the same verifier, the same inputs, the same fail-closed
// rule on an unwired verifier. ok=false names why the member is dropped.
func (e *Engine) reverifyRememberedNeed(ctx context.Context, principal storage.Principal, request InvestigationRequest, binding ResolvedGraphBinding, schemaVersion string, entry ConfirmedNeedEntry) (ConfirmedNeedMemberDropReason, bool) {
	switch entry.Member {
	case contractsv1.ContextFabricStructureNeedSubjectAnchor:
		// CHAOS-5788: an engine-committed anchor carries no offer's own
		// matched_term_hash to replay -- there was never a census claim to
		// re-check, only a resolution to re-run. It is not a live tampering
		// vector the way a caller-picked offer is (that offer's own doc
		// comment's reasoning): the SAME turn that consults this entry also
		// re-resolves the frame's anchor terms from scratch through the
		// ordinary fast path, and only a subject that resolution itself
		// re-proves can ever reach Committed. This entry never stands in for
		// that re-resolution; it only widens the pool it is allowed to
		// survive into (decideAnchorPoolKindScope, graphrank). No reverify,
		// exactly as expected_kind and window carry none (this function's
		// own default case, below).
		if entry.Basis == ConfirmedNeedBasisEngineCommitted {
			return "", true
		}
		// The carrier's OWN schema_version selects the verifier, exactly as an
		// ancr_ redemption dispatches on the issuing result's schema_version.
		if !e.anchorReverifierWired(schemaVersion) {
			return ConfirmedNeedMemberDropReverifyUnavailable, false
		}
		if !e.reverifyAnchorClaim(ctx, principal, request.RequestedScope, binding, schemaVersion, entry.AppliedKind, entry.AppliedValue, entry.MatchedTermHash) {
			return ConfirmedNeedMemberDropReverifyNotConfirmed, false
		}
		return "", true
	case contractsv1.ContextFabricStructureNeedSubjectCandidate:
		// candr_'s own reverify: the (kind, canonical_id) still exists as a
		// real, authorized node at THIS turn's pinned binding.
		if e.candidateVerifier == nil {
			return ConfirmedNeedMemberDropReverifyUnavailable, false
		}
		ok, reason := e.candidateVerifier(ctx, principal, request.RequestedScope, binding, entry.AppliedKind, entry.AppliedValue)
		if !ok || reason != CandidateVerificationValid {
			return ConfirmedNeedMemberDropReverifyNotConfirmed, false
		}
		return "", true
	case contractsv1.ContextFabricStructureNeedSubjectHandle:
		// handr_'s own reverify: the value's grammar and keyed source row, for
		// the offer's own pattern. An entry with no pattern cannot be replayed.
		if e.handleVerifier == nil || entry.PatternID == "" {
			return ConfirmedNeedMemberDropReverifyUnavailable, false
		}
		ok, reason := e.handleVerifier(ctx, principal.OrgID, entry.AppliedKind, entry.PatternID, entry.AppliedValue)
		if !ok || reason != HandleVerificationValid {
			return ConfirmedNeedMemberDropReverifyNotConfirmed, false
		}
		return "", true
	default:
		// expected_kind and window: their fresh redemptions carry no reverify.
		return "", true
	}
}

// anchorReverifierWired reports whether reverifyAnchorClaim has a verifier to
// run for schemaVersion -- the SAME dispatch, asked only so an unwired
// deployment is reported apart from a claim the verifier refused.
func (e *Engine) anchorReverifierWired(schemaVersion string) bool {
	switch schemaVersion {
	case InvestigationResultSchemaV1:
		return e.anchorVerifier != nil
	case InvestigationResultSchemaV2:
		return e.anchorMembershipVerifier != nil
	default:
		return false
	}
}

// statedNeedMembers is the SINGLE authority for "this turn states this member
// itself": by a receipt this request confirmed (confirmedThisTurn, window
// included), or by the caller's own explicit field -- request.ExpectedKinds,
// request.SubjectHandles (singular or plural) or
// request.TimeContext.EvidenceWindow. What the caller says this turn always
// wins over a remembered value and is never argued with, so every reader of
// the remembered ledger -- what applies (appliedNeedLedgerEntries,
// decideLedgerWindow) and what is saved forward (mergeConfirmedNeedsLedger) --
// asks this one function.
func statedNeedMembers(request InvestigationRequest, confirmedThisTurn []confirmedStructureMember) map[contractsv1.ContextFabricStructureNeedKind]bool {
	stated := map[contractsv1.ContextFabricStructureNeedKind]bool{}
	for _, c := range confirmedThisTurn {
		stated[c.Member] = true
	}
	if len(request.ExpectedKinds) > 0 {
		stated[contractsv1.ContextFabricStructureNeedExpectedKind] = true
	}
	if len(request.SubjectHandles) > 0 {
		stated[contractsv1.ContextFabricStructureNeedSubjectHandle] = true
	}
	if request.TimeContext.EvidenceWindow != nil {
		stated[contractsv1.ContextFabricStructureNeedWindow] = true
	}
	return stated
}

// appliedNeedLedgerEntries is the SINGLE authority for "does this remembered
// entry apply this turn" for the four structure members -- every consumer
// (Engine.resolveCarriedKind, confirmedAnchorSelection, kindCarryComparators,
// composeCarriedNeedEntry, telemetry) reads this map rather than re-deriving
// the check, so none of them can disagree about what applied: a member
// excluded here is excluded everywhere, including telemetry. window has its
// own consumer (decideLedgerWindow), decided where a fresh winr_ redemption
// is.
//
// An entry applies when it names expected_kind, subject_anchor,
// subject_candidate or subject_handle; it carries a non-empty value; and this
// turn does not state that member itself (statedNeedMembers). An explicit
// member is also echoed on the wire, and a second entry for the same member
// is one the v1 result validator refuses.
func appliedNeedLedgerEntries(remembered []confirmedStructureMember, confirmedThisTurn []confirmedStructureMember, request InvestigationRequest) map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember {
	stated := statedNeedMembers(request, confirmedThisTurn)
	out := map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember{}
	for _, r := range remembered {
		switch r.Member {
		case contractsv1.ContextFabricStructureNeedExpectedKind, contractsv1.ContextFabricStructureNeedSubjectAnchor,
			contractsv1.ContextFabricStructureNeedSubjectCandidate, contractsv1.ContextFabricStructureNeedSubjectHandle:
		default:
			continue
		}
		if r.AppliedValue == "" || stated[r.Member] {
			continue
		}
		out[r.Member] = r
	}
	return out
}

// kindCarryComparators is the member list applyCarryDrop compares a carried
// kind against: this turn's own receipts, plus the remembered subject-axis
// members that apply -- the same comparator a fresh candr_/handr_ receipt
// joins on the turn it is redeemed.
//
// EXCEPT when the carried kind is itself remembered. A remembered
// expected_kind (resolveCarriedKind returns it first whenever it applies)
// stands in for a kindr_ receipt, and a remembered candidate or handle stands
// in for its receipt; had the caller re-echoed all of them, the kind would be
// a confirmation on this turn rather than a carry, and no subject-axis
// receipt drops a confirmation. Comparing the two remembered values would
// drop on this turn a kind the redeeming turn applied, so only this turn's
// own receipts are compared then.
func kindCarryComparators(confirmedThisTurn []confirmedStructureMember, applied map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember) []confirmedStructureMember {
	if _, rememberedKind := applied[contractsv1.ContextFabricStructureNeedExpectedKind]; rememberedKind {
		return confirmedThisTurn
	}
	out := make([]confirmedStructureMember, 0, len(confirmedThisTurn)+2)
	out = append(out, confirmedThisTurn...)
	for _, member := range []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectCandidate, contractsv1.ContextFabricStructureNeedSubjectHandle} {
		if entry, ok := applied[member]; ok {
			out = append(out, entry)
		}
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

// confirmedNeedEntryOf is the persisted shape of one confirmed member.
func confirmedNeedEntryOf(member confirmedStructureMember) ConfirmedNeedEntry {
	return ConfirmedNeedEntry{
		Member: member.Member, AppliedKind: member.AppliedKind, AppliedValue: member.AppliedValue,
		MatchedTermHash: member.MatchedTermHash, PatternID: member.PatternID,
		WindowStart: cloneWindowBound(member.WindowStart), WindowEnd: cloneWindowBound(member.WindowEnd),
		Basis: member.Basis,
	}
}

// mergeConfirmedNeedsLedger builds the OUTGOING per-need ledger for a
// result's own semantic_state: this turn's own receipt-confirmed members
// (window included) win over an inherited entry for the SAME member; an
// inherited member this turn states explicitly is RETIRED -- an explicit
// value is the caller speaking now, it is not a confirmation the ledger
// keeps, and the remembered value it replaced must not come back on a later
// turn that names this result as parent (statedNeedMembers); every other
// inherited member (the ledger this turn's own admission admitted) carries
// forward unchanged. Iterated in the StructureNeedKind vocabulary's own fixed
// order, never map order, so the canonical encoding (EncodeSemanticState) is
// deterministic across two calls that resolve to the same set.
func mergeConfirmedNeedsLedger(remembered []confirmedStructureMember, confirmedThisTurn []confirmedStructureMember, request InvestigationRequest) []ConfirmedNeedEntry {
	stated := statedNeedMembers(request, confirmedThisTurn)
	byMember := map[contractsv1.ContextFabricStructureNeedKind]ConfirmedNeedEntry{}
	for _, r := range remembered {
		if stated[r.Member] {
			continue
		}
		byMember[r.Member] = confirmedNeedEntryOf(r)
	}
	for _, c := range confirmedThisTurn {
		byMember[c.Member] = confirmedNeedEntryOf(c)
	}
	var out []ConfirmedNeedEntry
	for _, member := range contractsv1.ContextFabricStructureNeedKindVocabulary() {
		if entry, ok := byMember[member]; ok {
			out = append(out, entry)
		}
	}
	return out
}

// axisConflictConfirmedNeeds is the outgoing ledger for the axis-conflict
// window veto. That terminal echoes none of this turn's receipt
// confirmations (windowVetoResult receives only the explicit members), so
// Save claims none of them and none is a confirmation this turn won: only the
// admitted remembered ledger carries forward, less what this turn states
// explicitly.
func axisConflictConfirmedNeeds(remembered []confirmedStructureMember, request InvestigationRequest) []ConfirmedNeedEntry {
	return mergeConfirmedNeedsLedger(remembered, nil, request)
}

// withoutSupersededConfirmedNeeds drops every member the atomic (org,
// prior_result_id, member) supersession claim just REFUSED (CHAOS-3927 P4:
// ErrStructureOfferSuperseded.Members, ports.go) from the ledger about to be
// captured onto the veto terminal Save persists instead.
//
// WHY THIS EXISTS. The outgoing ledger is computed before Save is attempted,
// from the members this turn's OWN receipts claimed. When Save's atomic claim
// on one of those members loses the race, the veto terminal persists a
// capture built from that SAME ledger: recordStructureConfirmationOutcome's
// own doc comment states the invariant this must not violate --
// "structureCanon.Confirmed genuinely won its claim" is proved ONLY by Save
// succeeding. A receipt whose claim just lost is caller authority nothing won;
// persisting it into the ledger unchanged would let a LATER turn, naming this
// veto result as parent, admit that member as if the claim it was refused for
// had actually been confirmed. Applied once, inside
// structureSupersessionVetoResult, so every Save site that can lose the race
// shares it (semanticStateCapture.withoutSupersededNeeds).
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

// withoutSupersededNeeds is the capture-level form of
// withoutSupersededConfirmedNeeds: the same capture with the refused members
// removed from its ledger and the snapshot re-encoded. A capture with no
// snapshot, or whose ledger holds none of the refused members, is returned
// unchanged.
func (c semanticStateCapture) withoutSupersededNeeds(superseded []contractsv1.ContextFabricStructureNeedKind) semanticStateCapture {
	if c.Write.State == nil || len(superseded) == 0 {
		return c
	}
	kept := withoutSupersededConfirmedNeeds(c.Write.State.ConfirmedNeeds, superseded)
	if len(kept) == len(c.Write.State.ConfirmedNeeds) {
		return c
	}
	state := *c.Write.State
	state.ConfirmedNeeds = kept
	encoded, err := EncodeSemanticState(&state)
	if err != nil {
		return semanticStateCapture{Write: SemanticStateAbsent(SemanticStateAbsenceSnapshotInvalid)}
	}
	return semanticStateCapture{Write: SemanticStateOf(&state), EncodedBytes: len(encoded)}
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
		ConfirmedNeeds:  mergeConfirmedNeedsLedger(remembered, confirmedThisTurn, request),
	})
}

// composeCarriedNeedEntry composes the wire disclosure for a remembered
// subject_anchor, subject_candidate or subject_handle this turn actually
// applied -- the analogue of composeCarriedKindEntry/composeCarriedWindowEntry,
// so a remembered confirmation is never a "silent" carry, the discipline both
// of those functions' own doc comments hold. Source=carried naming the parent
// the ledger was admitted from; provenance clarification_confirmed, because
// every entry the ledger holds was confirmed by a caller redeeming an offer.
// Applied expected_kind uses composeCarriedKindEntry. An early structure
// veto uses this helper for every remembered structure member, including
// expected_kind, then assigns the veto disposition. The caller excludes
// members already disclosed so each member has exactly one entry.
func composeCarriedNeedEntry(member contractsv1.ContextFabricStructureNeedKind, applied map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember, sourceResultID string) *contractsv1.ContextFabricConfirmedStructureEntry {
	entry, ok := applied[member]
	if !ok {
		return nil
	}
	return &contractsv1.ContextFabricConfirmedStructureEntry{
		Member: member, AppliedValue: entry.AppliedValue,
		Source:        contractsv1.ContextFabricStructureSourceCarried,
		PriorResultID: sourceResultID,
		// Every member before this axis existed, and every member redeemed
		// from a receipt, renders the ORIGINAL token
		// (provenanceForConfirmedNeedBasis's own zero-value rule) -- only a
		// subject_anchor the engine bound with no offer ever raised renders
		// the new one.
		Provenance:  provenanceForConfirmedNeedBasis(entry.Basis),
		Disposition: contractsv1.ContextFabricStructureDispositionApplied,
	}
}

// appendVetoedRememberedNeeds discloses reverified confirmations when the
// structure batch ends before its consumers run. The value is remembered,
// but its disposition is the batch veto, never applied. A member supplied
// on this request, or already echoed by canonicalization, keeps that entry.
// Window has its own request-side decision and is not part of this batch.
func appendVetoedRememberedNeeds(echo []ConfirmedStructureEntry, ledger confirmedNeedLedgerResult, request InvestigationRequest, veto structureVetoReason) []ConfirmedStructureEntry {
	var disposition contractsv1.ContextFabricStructureDisposition
	switch veto {
	case structureVetoConfirmationUnresolved:
		disposition = contractsv1.ContextFabricStructureDispositionVetoedUnresolved
	case structureVetoConfirmationConflict:
		disposition = contractsv1.ContextFabricStructureDispositionVetoedConflict
	case structureVetoStaleSupersededOffer:
		disposition = contractsv1.ContextFabricStructureDispositionVetoedStale
	default:
		return echo
	}
	var supplied []confirmedStructureMember
	for _, entry := range echo {
		supplied = append(supplied, confirmedStructureMember{Member: entry.Member})
	}
	for member, receipts := range map[contractsv1.ContextFabricStructureNeedKind][]BoundSubjectReceipt{
		contractsv1.ContextFabricStructureNeedExpectedKind:     request.PriorKindReceipts,
		contractsv1.ContextFabricStructureNeedSubjectAnchor:    request.PriorAnchorReceipts,
		contractsv1.ContextFabricStructureNeedSubjectHandle:    request.PriorHandleReceipts,
		contractsv1.ContextFabricStructureNeedSubjectCandidate: request.PriorCandidateReceipts,
	} {
		if len(receipts) > 0 {
			supplied = append(supplied, confirmedStructureMember{Member: member})
		}
	}
	remembered := appliedNeedLedgerEntries(ledger.Entries, supplied, request)
	for _, member := range appliedNeedLedgerMembers(remembered) {
		entry := composeCarriedNeedEntry(member, remembered, ledger.SourceResultID)
		entry.Disposition = disposition
		echo = append(echo, *entry)
	}
	return echo
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

// observableConfirmedNeedDrops renders dropped for the log line as
// member:reason pairs in ledger order, or "none".
func observableConfirmedNeedDrops(dropped []ConfirmedNeedMemberDrop) string {
	if len(dropped) == 0 {
		return "none"
	}
	rendered := ""
	for index, drop := range dropped {
		if index > 0 {
			rendered += ","
		}
		rendered += string(drop.Member) + ":" + string(drop.Reason)
	}
	return rendered
}

// confirmedNeedValueHash is the log-safe form of an applied canonical id or
// handle value: SHA-256, first 6 bytes, hex -- the same construction the
// projection coordinator uses for an org id. Empty in, empty out, so a member
// that did not apply logs no hash.
func confirmedNeedValueHash(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:6])
}

// ConfirmedNeedLedgerEvent is everything RecordConfirmedNeedLedger reports for
// one Investigate call. Kinds are closed subject-kind values; values are
// hashed (confirmedNeedValueHash), never logged raw; each is empty when its
// member did not apply.
type ConfirmedNeedLedgerEvent struct {
	Outcome                   ConfirmedNeedLedgerOutcome
	SourceResultID            string
	AppliedMembers            []contractsv1.ContextFabricStructureNeedKind
	AppliedExpectedKind       contractsv1.ContextFabricSubjectKind
	AppliedAnchorKind         contractsv1.ContextFabricSubjectKind
	AppliedAnchorValueHash    string
	AppliedCandidateKind      contractsv1.ContextFabricSubjectKind
	AppliedCandidateValueHash string
	AppliedHandleKind         contractsv1.ContextFabricSubjectKind
	AppliedHandleValueHash    string
	Dropped                   []ConfirmedNeedMemberDrop
	// AppliedAnchorBasis is the applied subject_anchor entry's own closed
	// basis (ConfirmedNeedBasis) -- "" for an ordinary receipt-redeemed
	// entry, "engine_committed" for one the engine bound with no offer ever
	// raised. Empty whenever AppliedAnchorKind is empty too (no anchor
	// applied this turn).
	AppliedAnchorBasis ConfirmedNeedBasis
	// AnchorAgreement compares THIS turn's own applied subject_anchor entry
	// (of either basis above) against what this turn's own resolution
	// independently committed -- see ConfirmedAnchorAgreement's own doc
	// comment for the closed vocabulary. This is the one field on this line
	// that can turn "applied" into a proven-wrong claim: a disagree here
	// means the entry was VETOED rather than disclosed as applied, and
	// AppliedMembers/AppliedAnchorKind/AppliedAnchorValueHash above already
	// reflect that (they read the post-veto ledger, not the pre-veto one).
	AnchorAgreement ConfirmedAnchorAgreement
	// AnchorDisposition is the wire disposition (ContextFabricStructureDisposition)
	// this turn's own applied subject_anchor entry disclosed on its
	// carried-structure echo, read from the SAME decision
	// (anchorLedgerDisposition) that echo itself renders: "" when no entry
	// applied this turn at all, "applied" when it survived, and one of the
	// three drop dispositions (superseded_by_caller/vetoed_conflict/
	// vetoed_unresolved) when it did not -- always the post-decision value,
	// never the pre-decision "applied" a dropped entry started this turn
	// with.
	AnchorDisposition contractsv1.ContextFabricStructureDisposition
	// CaptureDecision is THIS turn's own capture-gate scope decision
	// (engineCommittedAnchorForCapture's underlying CountPopulationScopeDecision):
	// "anchor_committed" means this turn's own resolution bound a NEW
	// engine-committed anchor for a LATER turn to inherit; any other value
	// is the honest reason it did not (frame_absent/organization_scope --
	// not applicable; anchor_unresolved/anchor_ambiguous -- nothing strong
	// enough bound). Empty only when the turn never reached the check
	// (an error before resolution, or a work_item_tuple turn, which has no
	// scope anchor of this kind at all).
	CaptureDecision CountPopulationScopeDecision
	// CaptureSkipReason (CHAOS-5825) discloses WHY CaptureDecision is empty
	// for the one early exit this axis names today (the CHAOS-4234
	// class-default window gate) -- see CaptureSkipReason's own doc
	// comment. NotApplicable when CaptureDecision carries a real decision,
	// or the turn ended on an exit this axis does not yet name.
	CaptureSkipReason CaptureSkipReason
}

// confirmedNeedLedgerEventOf builds the event from the admission result and
// the single applied map, so the line can never report a member the map did
// not apply. captureDecision and anchorAgreement are the two post-resolution
// facts appliedNeedLedgerEntries' own pre-resolution map cannot carry --
// captured from the SAME (frame, resolution) pair this turn's own capture
// gate and carry-agreement check already computed, never re-derived here.
func confirmedNeedLedgerEventOf(ledger confirmedNeedLedgerResult, applied map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember, captureDecision CountPopulationScopeDecision, captureSkipReason CaptureSkipReason, anchorAgreement ConfirmedAnchorAgreement, anchorDisposition contractsv1.ContextFabricStructureDisposition) ConfirmedNeedLedgerEvent {
	event := ConfirmedNeedLedgerEvent{
		Outcome: ledger.Outcome, SourceResultID: ledger.SourceResultID,
		AppliedMembers: appliedNeedLedgerMembers(applied), Dropped: ledger.Dropped,
		CaptureDecision: captureDecision, CaptureSkipReason: captureSkipReason,
		AnchorAgreement: anchorAgreement, AnchorDisposition: anchorDisposition,
	}
	if entry, ok := applied[contractsv1.ContextFabricStructureNeedExpectedKind]; ok {
		event.AppliedExpectedKind = contractsv1.ContextFabricSubjectKind(entry.AppliedValue)
	}
	if entry, ok := applied[contractsv1.ContextFabricStructureNeedSubjectAnchor]; ok {
		event.AppliedAnchorKind, event.AppliedAnchorValueHash = entry.AppliedKind, confirmedNeedValueHash(entry.AppliedValue)
		event.AppliedAnchorBasis = entry.Basis
	}
	if entry, ok := applied[contractsv1.ContextFabricStructureNeedSubjectCandidate]; ok {
		event.AppliedCandidateKind, event.AppliedCandidateValueHash = entry.AppliedKind, confirmedNeedValueHash(entry.AppliedValue)
	}
	if entry, ok := applied[contractsv1.ContextFabricStructureNeedSubjectHandle]; ok {
		event.AppliedHandleKind, event.AppliedHandleValueHash = entry.AppliedKind, confirmedNeedValueHash(entry.AppliedValue)
	}
	return event
}

// recordConfirmedNeedLedger reports the gate decision, once per Investigate
// call and on every exit (Investigate defers it above its first return, so an
// early window or structure gate never hides an admitted ledger; applied is
// empty on an exit that ended the turn before any consumer ran): the
// admission outcome, the parent result id it consulted (the SAME
// correlation handle window_continuation_decision already discloses for its
// own referenced result), each member that applied with its closed kind and
// hashed value, and each member dropped at reverify with its reason -- "a
// drop reported without both sides is a decision an operator cannot check"
// applies here exactly as it does to RecordKindCarry.
func (e *Engine) recordConfirmedNeedLedger(ctx context.Context, principal storage.Principal, ledger confirmedNeedLedgerResult, applied map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember, captureDecision CountPopulationScopeDecision, captureSkipReason CaptureSkipReason, anchorAgreement ConfirmedAnchorAgreement, anchorDisposition contractsv1.ContextFabricStructureDisposition) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.RecordConfirmedNeedLedger(ctx, principal, confirmedNeedLedgerEventOf(ledger, applied, captureDecision, captureSkipReason, anchorAgreement, anchorDisposition))
}
