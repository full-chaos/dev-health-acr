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
// file computes its OWN identity comparison, beside windowContinuationDecision
// and independent of it, using the SAME recipe (SemanticRequestIdentityOf,
// unchanged) so the two mechanisms can never disagree about what "the same
// question" means, while applying to a different, broader set of turns: any
// turn that names a parent result via request.ParentResultID.
//
// ONE HOP, like D-a: only the DIRECTLY named parent's ledger is read, never
// an older ancestor.
//
// HOW A REMEMBERED CONFIRMATION TAKES EFFECT. It is threaded into
// effectiveConfirmedKind and confirmedAnchorSelection ALONGSIDE (never
// merged into) structureCanon.Confirmed, so it narrows and commits
// resolution exactly as the original receipt did, WITHOUT joining
// structureCanon.Confirmed itself -- that slice is what reuseBypassReason
// (answer_reuse.go) keys the tryReuse bypass on (design brief DP11), and a
// turn carrying only a remembered need, with no receipt of its own, must not
// take that bypass. structureCanon.Confirmed still wins over a remembered
// entry for the same member: the caller's own receipt this turn is always
// authoritative.

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
	// ConfirmedNeedLedgerMissUnloadable: the named parent could not be read.
	ConfirmedNeedLedgerMissUnloadable ConfirmedNeedLedgerOutcome = "miss_unloadable"
	// ConfirmedNeedLedgerMissEmpty: the parent loaded and its semantic state
	// is available, but its ledger holds no confirmed need at all.
	ConfirmedNeedLedgerMissEmpty ConfirmedNeedLedgerOutcome = "miss_empty"
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
	// ConfirmedNeedLedgerHit: the identities match. The ledger's entries are
	// available to this turn.
	ConfirmedNeedLedgerHit ConfirmedNeedLedgerOutcome = "hit"
)

func confirmedNeedLedgerOutcomes() []ConfirmedNeedLedgerOutcome {
	return []ConfirmedNeedLedgerOutcome{
		ConfirmedNeedLedgerMissNoReference, ConfirmedNeedLedgerMissUnloadable,
		ConfirmedNeedLedgerMissEmpty, ConfirmedNeedLedgerDroppedIdentityIncomparable,
		ConfirmedNeedLedgerDroppedIdentityChanged, ConfirmedNeedLedgerHit,
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
// admission outcome, and -- only on a hit -- the ledger's entries in the same
// shape a redeemed receipt produces.
type confirmedNeedLedgerResult struct {
	Outcome ConfirmedNeedLedgerOutcome
	Entries []confirmedStructureMember
}

// resolveConfirmedNeedLedger is CHAOS-5639's own need-gate admission. It
// reads the ONE parent this request names (request.ParentResultID -- the
// general "this turn continues that one" bearer field, D-d's own phrase),
// and admits its ledger only when this turn's recomputed request identity
// (SemanticRequestIdentityOf, the referenced exchange dropped exactly as
// D-a's own comparison drops it) equals the identity the parent's own
// snapshot recorded.
//
// Uses carryLoadResult (chaos4360_carry.go) so a call already made for the
// SAME result id within this Investigate call costs no second store round
// trip; window-only continuations name a DIFFERENT id (D-d requires
// ParentResultID empty for that shape), so the two mechanisms never share a
// cache hit, only the cache itself.
func (e *Engine) resolveConfirmedNeedLedger(ctx context.Context, principal storage.Principal, request InvestigationRequest) confirmedNeedLedgerResult {
	parent := carryParentSeed(request)
	if parent == "" || e.results == nil {
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerMissNoReference}
	}
	stored, err := carryLoadResult(ctx, e.results, principal, parent)
	if err != nil {
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerMissUnloadable}
	}
	if stored.SemanticStateRead != SemanticStateReadAvailable || stored.SemanticState == nil || len(stored.SemanticState.ConfirmedNeeds) == 0 {
		return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerMissEmpty}
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
		entries = append(entries, confirmedStructureMember{
			Member: entry.Member, AppliedKind: entry.AppliedKind, AppliedValue: entry.AppliedValue,
		})
	}
	return confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerHit, Entries: entries}
}

// appliedRememberedMembers reports which of remembered's members this turn
// will actually apply -- every member structureCanon.Confirmed did NOT
// itself confirm this turn (a real receipt always wins), restricted to the
// two members a remembered entry can currently reach a resolution parameter
// for (effectiveConfirmedKind, confirmedAnchorSelection). Used only for the
// ledger's own telemetry line; effectiveConfirmedKind/confirmedAnchorSelection
// each make this same "own confirmed wins" check independently, so the two
// can never disagree about what is reported vs. what is applied.
func appliedRememberedMembers(remembered []confirmedStructureMember, confirmedThisTurn []confirmedStructureMember) []contractsv1.ContextFabricStructureNeedKind {
	thisTurn := map[contractsv1.ContextFabricStructureNeedKind]bool{}
	for _, c := range confirmedThisTurn {
		thisTurn[c.Member] = true
	}
	var out []contractsv1.ContextFabricStructureNeedKind
	for _, r := range remembered {
		switch r.Member {
		case contractsv1.ContextFabricStructureNeedExpectedKind, contractsv1.ContextFabricStructureNeedSubjectAnchor:
		default:
			continue
		}
		if r.AppliedValue == "" {
			// Mirrors effectiveConfirmedKind's own guard: an entry with no
			// value never actually applies, whatever member it names.
			continue
		}
		if thisTurn[r.Member] {
			continue
		}
		out = append(out, r.Member)
	}
	return out
}

// rememberedMember returns remembered's entry for member, nil when it holds
// none -- the shared lookup effectiveConfirmedKind and confirmedAnchorSelection
// both use, so neither can drift from the other's idea of what the ledger
// said.
func rememberedMember(remembered []confirmedStructureMember, member contractsv1.ContextFabricStructureNeedKind) *confirmedStructureMember {
	for i := range remembered {
		if remembered[i].Member == member {
			return &remembered[i]
		}
	}
	return nil
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
		byMember[r.Member] = ConfirmedNeedEntry{Member: r.Member, AppliedKind: r.AppliedKind, AppliedValue: r.AppliedValue}
	}
	for _, c := range confirmedThisTurn {
		byMember[c.Member] = ConfirmedNeedEntry{Member: c.Member, AppliedKind: c.AppliedKind, AppliedValue: c.AppliedValue}
	}
	out := make([]ConfirmedNeedEntry, 0, len(byMember))
	for _, member := range contractsv1.ContextFabricStructureNeedKindVocabulary() {
		if entry, ok := byMember[member]; ok {
			out = append(out, entry)
		}
	}
	return out
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
// Investigate call: the admission outcome, and -- only on a hit -- which
// members actually applied to this turn's resolution (value source =
// remembered, by construction: this list is never populated any other way).
func (e *Engine) recordConfirmedNeedLedger(ctx context.Context, principal storage.Principal, ledger confirmedNeedLedgerResult, appliedMembers []contractsv1.ContextFabricStructureNeedKind) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.RecordConfirmedNeedLedger(ctx, principal, ledger.Outcome, appliedMembers)
}
