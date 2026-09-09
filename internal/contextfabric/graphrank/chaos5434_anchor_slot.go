package graphrank

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// THE DECIDED SCOPE ANCHOR'S OWN SLOT THROUGH PHASE-4 TRUNCATION.
//
// The design already promised this. Invariant I11 (CHAOS-4452 §13.5.2's
// phase-B table) says that on a children_of_scope frame "the RESOLVED
// anchor's kind != MemberKind ... the anchor's kind is unknown until the term
// resolves", and defines RESOLVED as "the graph committed the anchor" -- so
// the anchor is a subject this resolution must be ABLE to commit, and a
// returned pool holding ninety members and zero anchors makes I11
// unsatisfiable on the frames it governs. The mechanism is the CHAOS-4271
// ruling's own: "ScopeAnchorKind as a third hint source into the hinted arm
// (real pool) + a bounded per-kind reserved slot through truncation" (shipped
// as #411, docs/design/context-fabric-intent-engine-build-state.md). And
// reservedPrefix states the guarantee in its own words: a kind the frame or
// receipt declared "does not disappear from the offered list purely because a
// lexically noisier kind filled the budget first."
//
// WHAT WAS ACTUALLY DELIVERED. reservedPrefix's victim rule refuses to take a
// slot from a candidate of ANY reserved kind. On a scope-anchored frame the
// MEMBER kind is reserved too, so once the in-budget prefix is saturated with
// members there is no eligible victim, the loop breaks, and the promised slot
// is silently not delivered -- measured identically for the receipt source,
// the confirmed-anchor fallback and a resolution with no confirmed kind at
// all (CHAOS-5393's own pin recorded this in prose because it could not
// assert it). This file makes the existing promise hold for the ONE claimant
// the design names as the subject to commit, and nothing else.
//
// WHAT THIS IS NOT. The anchor still competes in the MEMBERS' contest. That
// shared contest is CHAOS-5445's to separate (the anchor deciding in its own
// slot, two cuts, the CHAOS-5222 rig-visibility contract renegotiated); this
// change is the narrow reserved-slot guarantee 5445 can subsume unchanged.
type anchorReservedSlot struct {
	// Kind is the kind the scope anchor is allowed to resolve under, as
	// decided ONCE by decideAnchorPoolKindScope before retrieval. Empty
	// means no anchor was decided on this resolution -- every request that
	// is not scope-anchored, and every scope-anchored one whose kind
	// neither the receipt nor a redeemed anchor selection supplied. An
	// empty Kind makes every function in this file inert, so the cut is
	// byte-identical to the pre-ticket prefix.
	Kind contextfabric.SubjectKind
	// Source is anchorPoolKindScope's own token (receipt / confirmed_anchor
	// / none), carried rather than re-derived because the two sources fail
	// independently: an operator who cannot tell them apart cannot tell a
	// model that stopped emitting scope_anchor_kind from a caller that
	// stopped redeeming anchor receipts.
	Source string
}

// anchorSlotOutcome is what the cut reports about its slot decision. Every
// field is populated on EVERY pass through the cut, including the passes that
// reserve nothing, so a missing measurement and a measured zero can never
// read alike (the explicit-zeros rule this engine's telemetry runs on).
type anchorSlotOutcome struct {
	// Reserved is the kind token a slot was held for, or the explicit
	// "none" token. It reports the DECISION, not whether the slot had to
	// fire: an anchor that ranked inside the budget on its own merits still
	// held a slot.
	Reserved string
	// Source is anchorReservedSlot.Source, or the explicit "none" token.
	Source string
	// Displaced is 1 when the anchor's slot evicted an in-budget candidate
	// that ranking had earned, 0 otherwise. It is an int rather than a bool
	// because kindReserveSlotsPerKind is a bound the design may raise, and
	// a count stays truthful when it does.
	Displaced int
	// DisplacedSubject names the evicted candidate so the displacement is
	// never silent. Nil exactly when Displaced == 0.
	DisplacedSubject *contextfabric.SubjectRef
	// PoolTruncatedN is how many candidates this cut dropped. It is what
	// says whether a slot could have mattered at all: a cut that dropped
	// nothing cannot have starved the anchor, so an absent admission on
	// such a pass is not evidence of a regression.
	PoolTruncatedN int
}

// anchorSlotNone is the explicit token both string fields carry when nothing
// was reserved. It is deliberately the SAME token anchorPoolKindScope already
// publishes on the decision summary, so an operator reads one vocabulary
// across the two lines rather than two spellings of "no".
const anchorSlotNone = anchorPoolKindScopeNone

// observable renders the pair the ranked-cut summary carries, never "".
func (s anchorReservedSlot) observable() (reserved string, source string) {
	if s.Kind == "" {
		return anchorSlotNone, anchorSlotNone
	}
	if s.Source == "" {
		return string(s.Kind), anchorSlotNone
	}
	return string(s.Kind), s.Source
}

// anchorSlotVictim picks the candidate the anchor's slot takes, under the
// WIDENED eligibility this ticket adds, and returns -1 when none qualifies.
//
// It runs ONLY after reservedPrefix's ordinary victim search has already
// failed, so the ordinary rule keeps its exact meaning for every other kind
// and every other frame: nothing here can change a cut the old rule could
// already take.
//
// Each clause is load-bearing:
//   - TAIL FIRST. j walks down from max-1, so the cost is paid by the
//     LOWEST-ranked survivor. The anchor never takes a slot from a member
//     ranked above one that would have served.
//   - TIERS 0 AND 1 ARE UNTOUCHABLE, exactly as in the ordinary rule. The
//     phase list promises a committed subject can never be dropped by
//     truncation, and tier 1 exists so a document's answer-bearing parent is
//     not crowded out; a reserve that could evict either trades one
//     starvation for another.
//   - NEVER THE ANCHOR'S OWN KIND. present[anchor] is zero whenever this runs,
//     so no such victim exists today; the clause is written anyway because a
//     future kindReserveSlotsPerKind > 1 would otherwise let the anchor evict
//     itself, and a guard that is currently unreachable is cheaper than the
//     bug it prevents.
//   - A RESERVED KIND KEEPS ITS OWN SLOT. A reserved victim qualifies only
//     while more than kindReserveSlotsPerKind of its kind sit in the budget,
//     so the LAST member of a reserved kind is never taken. This is what
//     keeps two ordinary reserved kinds (a grouped frame's group and member
//     axes) from fighting over one slot -- the property
//     TestReservedPrefix_OneReservedKindNeverEvictsAnother exists for, which
//     stays green because a kind with exactly one in-budget candidate is not
//     eligible under a strict >.
//
// The budget is never exceeded: this returns an index to be un-kept in
// exchange for one that is kept, so the retained count is identical with and
// without the slot -- phase 4's contract is that a reserve displaces rather
// than grows the budget.
func anchorSlotVictim(ordered []contextfabric.SubjectCandidate, orderedTier []int, kept []bool, max int, reserved map[contextfabric.SubjectKind]bool, present map[contextfabric.SubjectKind]int, anchorKind contextfabric.SubjectKind) int {
	for j := max - 1; j >= 0; j-- {
		if !kept[j] || orderedTier[j] != 2 {
			continue
		}
		kind := ordered[j].Subject.Kind
		if kind == anchorKind {
			continue
		}
		if reserved[kind] && present[kind] <= kindReserveSlotsPerKind {
			continue
		}
		return j
	}
	return -1
}
