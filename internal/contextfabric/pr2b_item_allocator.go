package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The ONE item allocator (CHAOS-4636 / S5, quota side).
//
// WHAT PROBLEM IT SOLVES, stated as the arithmetic rather than as a
// complaint. `planBudget` sets MaxMembers = MaxItems - SynthesisHeadroom, and
// the grouped headroom is a CONSTANT 20. So:
//
//	ceiling 30 -> MaxMembers 10 -> 20 items left for everything else
//	ceiling 45 -> MaxMembers 25 -> 20 items left for everything else
//
// The non-member allowance is 20 at BOTH ceilings. Raising MaxItems buys
// member slots and exactly zero extra items for drivers, claims, findings and
// candidates -- so the per-group squeeze is a function of the constant
// headroom, not of the ceiling, and no amount of raising the ceiling relieves
// it. That is why this allocator exists and why group-aware headroom is the
// only lever available.
//
// WHAT IT DOES NOT DO, and must never start doing: it does not truncate, it
// does not write a limitation, it does not refuse. Enforcement belongs to S7c
// for every answer shape, and the allowance arithmetic lives in exactly one
// place -- narrowCandidatesToBudget. This allocator decides the QUOTA and
// hands its numbers to that enforcement as INPUTS.
// TestTheAllowanceArithmeticIsComputedInExactlyOnePlace and
// TestTheAllocatorTruncatesNothingAndWritesNoLimitation pin both halves from
// source, so the boundary is structural rather than a convention someone has
// to remember.

// ItemAllocation is the quota this allocator published for one request, and
// the single source every spender reads.
//
// Every field is a DECISION with an arithmetic consequence, which is why they
// are published together rather than recomputed at each site: a second
// derivation of any one of them is how the budget came to have two spenders in
// the first place.
type ItemAllocation struct {
	// MaxItems is the effective ceiling this allocation was computed
	// against, carried so a consumer cannot pair a quota with a different
	// budget than the one that produced it.
	MaxItems int
	// Reserved is the deterministic, engine-authored output held back
	// before any model headroom: limitations, disclosures and narration
	// are charged here, not discovered later.
	Reserved int
	// Global is the allowance for items belonging to the answer rather
	// than to any group.
	Global int
	// Groups is how many groups the quota was split across, zero for an
	// ungrouped answer.
	Groups int
	// ItemsPerGroup is the published per-group quota -- the number the
	// model prompt states, so the model is asked for what will fit.
	ItemsPerGroup int
	// Remainder is what integer division could not distribute evenly. It
	// is published rather than silently dropped or silently given to the
	// first group.
	Remainder int
	// MultiGroupCharge is the declared rule for an item naming several
	// groups. Published because the two rules give different answers to
	// "does this fit" and the counts alone cannot say which one ran.
	MultiGroupCharge contractsv1.ContextFabricMultiGroupCharge
	// NarrationBudget is how many ITEMS narration may spend. This is the
	// CHAOS-5008 fix: narration used to charge against the static contract
	// caps (50 drivers / 250 claims), which it could satisfy while
	// spending more than twice the whole plan ceiling.
	NarrationBudget int
}

// allocatorReservedDeterministic is the item allowance held back for
// engine-authored output before any group quota is computed.
//
// A STARTING VALUE, not a derivation, and labelled as such for the same reason
// planSynthesisHeadroom labels its own table: the honest magnitude comes from
// the rig, and this is what moves when the measurement says so.
const allocatorReservedDeterministic = 2

// AllocateItems computes the ONE quota for a request.
//
// groups is the number of group entities the cohort carries (zero when the
// answer has no group axis); members is the cohort size the plan admitted.
//
// The invariant it maintains, checked by TestTheAllocatorRespectsTheInvariant:
//
//	Reserved + Global + (Groups * ItemsPerGroup) + Remainder
//	         + NarrationBudget + members  <=  MaxItems
//
// NarrationBudget is INSIDE the invariant, and its absence from the first
// version is precisely what let the allocator permit an overrun.
//
// An unbounded budget (MaxItems <= 0) yields a zero allocation, which every
// consumer reads as "no quota in force" -- never as "a quota of zero".
func AllocateItems(plan AnswerPlan, groups, members int) ItemAllocation {
	maxItems := plan.Budget.MaxItems
	if maxItems <= 0 {
		return ItemAllocation{MultiGroupCharge: contractsv1.ContextFabricMultiGroupChargeEveryGroup}
	}
	if members < 0 {
		members = 0
	}
	if groups < 0 {
		groups = 0
	}

	allocation := ItemAllocation{
		MaxItems: maxItems,
		Reserved: allocatorReservedDeterministic,
		Groups:   groups,
		// EVERY_GROUP is the default because it cannot under-count what a
		// group is responsible for: a shared pool can hide a cross-cutting
		// item from both groups' quotas and let the total overrun while
		// every per-group number still looks compliant.
		MultiGroupCharge: contractsv1.ContextFabricMultiGroupChargeEveryGroup,
	}

	// Members are charged before anything else, because they are: one item
	// per member is spent by the cohort row itself before a single driver
	// or claim exists.
	available := maxItems - allocation.Reserved - members
	if available < 0 {
		available = 0
	}

	// NARRATION IS CARVED OUT OF THE POOL FIRST, NOT ADDED ON TOP.
	//
	// The first version of this function assigned the WHOLE pool to
	// Global + per-group + remainder and THEN handed narration half of the
	// per-group pool -- so every spender together could claim more than the
	// ceiling while each individual number looked compliant. At MaxItems=30
	// with two groups and one member that reached 39 against 30.
	//
	// That is CHAOS-5008's own shape reintroduced by the repair for it: a
	// spender the others cannot see. It is fixed by making narration a
	// FIRST-CLASS CLAIMANT on the pool rather than a second helping of it,
	// and by putting narration INSIDE the published invariant
	// (TestTheAllocatorDoesNotDoubleReserveNarration sums every spender),
	// so the same omission cannot recur invisibly.
	allocation.NarrationBudget = narrationItemBudget(available)
	available -= allocation.NarrationBudget

	if groups == 0 {
		allocation.Global = available
		return allocation
	}

	// Global first, then the rest splits across groups. A grouped answer
	// still carries candidates and unattributed findings, and a quota that
	// forgot them would be apportioning items that are already spent.
	allocation.Global = available / (groups + 1)
	perGroupPool := available - allocation.Global
	allocation.ItemsPerGroup = perGroupPool / groups
	// Remainder is published, not distributed. Handing it to the first
	// group would make the quota depend on group ORDER, and group order is
	// not a property this system promises to be stable.
	allocation.Remainder = perGroupPool - (allocation.ItemsPerGroup * groups)
	return allocation
}

// narrationItemBudget is narration's OWN share of the spendable allowance, in
// ITEMS, carved out before anything else is apportioned.
//
// A THIRD of the pool, not a half. Narration is one claimant among three --
// itself, the global pool, and the groups -- and handing it half left too
// little for the axis a grouped answer is actually about. The fraction is a
// starting value the rig is expected to move, like planSynthesisHeadroom's own
// table; what is NOT negotiable is that it comes OUT of the pool rather than
// being added to an already-full one.
func narrationItemBudget(available int) int {
	if available <= 0 {
		return 0
	}
	return available / 3
}

// NarrationDriverAllowance converts the allocator's ITEM budget into the two
// counts the narration composer actually needs.
//
// Each narrated member costs driversPerNarratedMember driver judgments AND one
// minted claim per driver, so a narrated member charges 2x its drivers against
// the item budget. That doubling is what the static-cap version never saw.
func (a ItemAllocation) NarrationDriverAllowance(driversPerMember int) (membersToNarrate int) {
	if a.MaxItems <= 0 || a.NarrationBudget <= 0 || driversPerMember <= 0 {
		return 0
	}
	costPerMember := driversPerMember * 2
	return a.NarrationBudget / costPerMember
}
