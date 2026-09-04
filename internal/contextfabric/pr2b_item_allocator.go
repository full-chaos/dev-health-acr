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
// ONE POOL PER BUCKET, DERIVED FROM THE VOCABULARY. Pools is indexed by
// position in ContextFabricItemBucketVocabulary(), so a bucket cannot exist
// without a pool -- that is not a convention, it is the shape of the type.
//
// WHY IT IS BUILT THIS WAY, stated because two rounds of review found the same
// class here. The first version apportioned a hand-written list of claimants:
// a global pool and a per-group pool. Round 1 found that NARRATION was not in
// that list, so every spender together could claim 39 items against a ceiling
// of 30. I added narration. Round 2 then found that MEMBER-attributed items
// were not in it either -- members were charged one item each for their cohort
// row while member-attributed drivers and claims had no pool at all, measured
// at 34 items against 30. Two rounds, one class, each fix leaving the next,
// because the list was being re-derived by hand every time.
//
// So the list is gone. The buckets come from the closed vocabulary that
// already defines what an item can BE, and every active one gets a pool.
// Adding a fifth bucket to that vocabulary gives it a pool automatically; it
// cannot be forgotten, because there is no longer anywhere to forget it.
type ItemAllocation struct {
	// MaxItems is the effective ceiling this allocation was computed
	// against, carried so a consumer cannot pair a quota with a different
	// budget than the one that produced it.
	MaxItems int
	// Reserved is the deterministic, engine-authored output held back
	// before anything else: limitations and disclosures are charged here,
	// not discovered later.
	Reserved int
	// Pools is the allowance for each bucket, indexed by its position in
	// ContextFabricItemBucketVocabulary(). A bucket that cannot receive
	// items in this answer (group and multi_group on an ungrouped answer)
	// holds zero and its share goes to the buckets that can.
	Pools [contractsv1.ContextFabricItemBucketCount]int
	// Groups is how many groups the group pool is split across, zero for
	// an ungrouped answer.
	Groups int
	// ItemsPerGroup is the published per-group quota -- the group pool
	// divided across the groups. It is the number the model prompt states.
	//
	// ZERO IS A REAL QUOTA, not an absent one. On a budget too small to
	// give a group anything, every charged item is over quota, and round 2
	// found the exposure silently skipping exactly that case.
	ItemsPerGroup int
	// Remainder is what integer division could not distribute evenly. It is
	// published rather than handed to a bucket or a group, because either
	// would make the quota depend on an order this system does not promise
	// to keep stable.
	Remainder int
	// MultiGroupCharge is the declared rule for an item naming several
	// groups. Published because the two rules give different answers to
	// "does this fit" and the counts alone cannot say which one ran.
	MultiGroupCharge contractsv1.ContextFabricMultiGroupCharge
	// NarrationBudget is how many ITEMS narration may spend. Narration is a
	// claimant on the pool alongside the buckets, never a second helping of
	// it -- that was round 1's finding.
	NarrationBudget int
}

// Pool returns the allowance for one bucket, or zero for a value outside the
// closed vocabulary.
func (a ItemAllocation) Pool(bucket contractsv1.ContextFabricItemBucket) int {
	for index, member := range contractsv1.ContextFabricItemBucketVocabulary() {
		if member == bucket {
			return a.Pools[index]
		}
	}
	return 0
}

// TotalPooled is every bucket's allowance summed.
func (a ItemAllocation) TotalPooled() int {
	total := 0
	for _, pool := range a.Pools {
		total += pool
	}
	return total
}

// allocatorReservedDeterministic is the item allowance held back for
// engine-authored output before any bucket is apportioned.
//
// A STARTING VALUE, not a derivation, and labelled as such for the same reason
// planSynthesisHeadroom labels its own table: the honest magnitude comes from
// the rig, and this is what moves when the measurement says so.
const allocatorReservedDeterministic = 2

// activeBuckets are the buckets that can receive items in this answer.
//
// An ungrouped answer has no group and no multi_group items by construction,
// so giving those buckets a share would strand a third of the budget on
// collections that cannot be filled. Their share goes to the buckets that can
// receive items instead.
func activeBuckets(groups int) []contractsv1.ContextFabricItemBucket {
	active := []contractsv1.ContextFabricItemBucket{
		contractsv1.ContextFabricItemBucketGlobal,
		contractsv1.ContextFabricItemBucketMember,
	}
	if groups > 0 {
		active = append(active,
			contractsv1.ContextFabricItemBucketGroup,
			contractsv1.ContextFabricItemBucketMultiGroup)
	}
	return active
}

// AllocateItems computes the ONE quota for a request.
//
// groups is the number of group entities the cohort carries (zero when the
// answer has no group axis); members is the cohort size the plan admitted, and
// is used only to bound the member pool's own floor -- member ROWS are charged
// to the member bucket like every other member-attributed item, which is the
// unification round 2's finding forced.
//
// The invariant, checked by TestEverySpenderFitsInsideTheCeiling:
//
//	Reserved + NarrationBudget + TotalPooled() + Remainder  ==  MaxItems
//
// An unbounded budget (MaxItems <= 0) yields a zero allocation, which every
// consumer reads as "no quota in force" -- never as "a quota of zero".
func AllocateItems(plan AnswerPlan, groups, members int) ItemAllocation {
	maxItems := plan.Budget.MaxItems
	if maxItems <= 0 {
		return ItemAllocation{MultiGroupCharge: contractsv1.ContextFabricMultiGroupChargeEveryGroup}
	}
	if groups < 0 {
		groups = 0
	}

	allocation := ItemAllocation{
		MaxItems: maxItems,
		Reserved: allocatorReservedDeterministic,
		Groups:   groups,
		// EVERY_GROUP cannot under-count what a group is responsible for:
		// a shared pool can hide a cross-cutting item from every group's
		// quota and let the total overrun while each per-group number
		// still looks compliant.
		MultiGroupCharge: contractsv1.ContextFabricMultiGroupChargeEveryGroup,
	}
	if allocation.Reserved > maxItems {
		allocation.Reserved = maxItems
	}

	afterReserved := maxItems - allocation.Reserved
	active := activeBuckets(groups)

	// Narration is ONE CLAIMANT ALONGSIDE THE BUCKETS, which is what makes
	// the invariant hold: it is carved from the same pool, never added to a
	// pool already fully apportioned.
	allocation.NarrationBudget = afterReserved / (len(active) + 1)
	spendable := afterReserved - allocation.NarrationBudget

	base := spendable / len(active)
	for _, bucket := range active {
		for index, member := range contractsv1.ContextFabricItemBucketVocabulary() {
			if member == bucket {
				allocation.Pools[index] = base
			}
		}
	}
	allocation.Remainder = spendable - (base * len(active))

	if groups > 0 {
		allocation.ItemsPerGroup = allocation.Pool(contractsv1.ContextFabricItemBucketGroup) / groups
	}
	return allocation
}

// NarrationDriverAllowance converts the allocator's ITEM budget into the
// number of members narration may narrate.
//
// Each narrated member costs driversPerMember driver judgments AND one minted
// claim per driver, so a narrated member charges 2x its drivers against the
// item budget. That doubling is what the static-cap version never saw.
func (a ItemAllocation) NarrationDriverAllowance(driversPerMember int) (membersToNarrate int) {
	if a.MaxItems <= 0 || a.NarrationBudget <= 0 || driversPerMember <= 0 {
		return 0
	}
	return a.NarrationBudget / (driversPerMember * 2)
}
