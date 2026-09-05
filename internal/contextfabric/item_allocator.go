package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The ONE item allocator: it publishes GRANTS from a single capacity account,
// before synthesis and narration spend anything.
//
// WHAT PROBLEM IT SOLVES, as arithmetic rather than as a complaint. `planBudget`
// sets MaxMembers = MaxItems - SynthesisHeadroom, and the grouped headroom is a
// CONSTANT 20:
//
//	ceiling 30 -> MaxMembers 10 -> 20 items left for everything else
//	ceiling 45 -> MaxMembers 25 -> 20 items left for everything else
//
// The non-member allowance is 20 at BOTH ceilings, so the per-group squeeze is a
// function of the constant headroom, not of the ceiling, and raising the ceiling
// never relieves it.
//
// WHAT IT DOES NOT DO, and must never start doing: it does not truncate, does
// not write a limitation, does not refuse, and does not measure the result.
// Enforcement is S7c's for every answer shape; measurement is the reconciler's
// (internal/contracts/v1, ReconcileContextFabricResultItems). When the mandatory
// commitments alone exceed the ceiling this REPORTS that fact and publishes
// zero grants -- an allocator that refused would be an enforcer.
//
// WHY THIS SHAPE, after three implementation rounds found the same two classes.
// The previous shape apportioned pools and let a `Remainder` field absorb
// whatever was left over. That is structurally unable to catch a wrong pool:
// any error moves into the remainder and the partition still balances, which is
// why a mutation under-allocating the member pool by one left both packages
// green. So there is no free remainder here. Every grant is derived by a stated
// formula, `Agreement` RE-DERIVES each one and compares, and the residue of the
// integer division is granted to `global` BY RULE and bounded by the number of
// active buckets -- not by the size of the bucket vocabulary, which was the
// bound the old guard used and the reason it had two items of slack in exactly
// the regime the defect lived in.

// MultiGroupChargeEveryGroup is the DECLARED pricing rule for an item naming
// several groups: it costs ONE item globally and contributes ONE usage
// occurrence to EACH group it names.
//
// Published on the allocation and emitted with the quota, because the counts
// alone cannot say which rule produced them and the alternative (a shared pool
// charged once) gives different answers to "does this fit". Introducing that
// alternative is an S7c policy decision, not a code change here.
const MultiGroupChargeEveryGroup = "every_group"

// ItemAllocation is the quota published for ONE request, and the single source
// every spender reads.
//
// ONE GRANT PER BUCKET, DERIVED FROM THE VOCABULARY. Grants is indexed by
// position in ContextFabricItemBucketVocabulary(), so a bucket cannot exist
// without a grant: that is the shape of the type, not a convention. Adding a
// fifth bucket to that vocabulary gives it a grant automatically.
type ItemAllocation struct {
	// MaxItems is the ceiling this allocation was computed against, carried
	// so a consumer cannot pair a quota with a different budget than the one
	// that produced it.
	MaxItems int
	// MemberRowCommitment is the capacity taken for the cohort member ROWS
	// before anything discretionary is granted.
	//
	// A COMMITMENT, not a reservation. The rows are charged by
	// CountContextFabricResultItems and debited by the ledger, so this is
	// capacity committed to a quantity that actually exists. Its predecessor
	// reserved two items for limitations and outcome rows, which the budget
	// does not charge at all -- capacity withheld for nothing, which is the
	// same class as a charged quantity with no pool, pointing the other way.
	MemberRowCommitment int
	// Grants is the allowance for each bucket, indexed by its position in
	// ContextFabricItemBucketVocabulary(). A bucket that cannot receive
	// items in this answer (group and multi_group on an ungrouped answer)
	// holds zero and its share goes to the buckets that can.
	//
	// The member grant is DISCRETIONARY and is what remains for drivers,
	// findings and claims about members: the rows were already committed
	// above, so nothing is charged to this grant twice.
	Grants [contractsv1.ContextFabricItemBucketCount]int
	// Groups is how many groups the group allowance is split across, zero on
	// an answer with no group axis.
	Groups int
	// NarrationGrant is how many ITEMS narration may spend. Narration is one
	// claimant alongside the buckets, never a second helping of a pool that
	// is already fully apportioned -- that was round 1's finding.
	NarrationGrant int
	// Residue is what integer division could not distribute evenly. It is
	// granted to `global` BY RULE and is recorded here so the rule is
	// auditable rather than implicit. It is NOT an absorber: Agreement
	// re-derives every grant and bounds this below the number of active
	// buckets.
	Residue int
	// Infeasible reports that the MANDATORY commitments alone meet or exceed
	// the ceiling, so every grant is zero. Reported, never refused: an
	// allocator that refused would be an enforcer, and S7c owns that.
	Infeasible bool
	// MultiGroupCharge is the declared pricing rule, always
	// MultiGroupChargeEveryGroup today. Published because the counts alone
	// cannot say which rule produced them.
	MultiGroupCharge string
}

// Grant returns the allowance for one bucket, zero for a value outside the
// closed vocabulary.
func (a ItemAllocation) Grant(bucket contractsv1.ContextFabricItemBucket) int {
	for index, member := range contractsv1.ContextFabricItemBucketVocabulary() {
		if member == bucket {
			return a.Grants[index]
		}
	}
	return 0
}

// TotalGranted is every bucket's allowance summed.
func (a ItemAllocation) TotalGranted() int {
	total := 0
	for _, grant := range a.Grants {
		total += grant
	}
	return total
}

// InForce reports whether an item quota exists at all. An unbounded budget
// yields a zero allocation, which every consumer must read as "no quota", never
// as "a quota of zero" -- a model shown a quota of zero has been told to write
// nothing.
func (a ItemAllocation) InForce() bool { return a.MaxItems > 0 }

// PerMemberGrant is the allowance published for member-attributed output.
//
// It is the DISCRETIONARY member grant and nothing else: the member rows are
// committed off the top of the account before any bucket is granted, so this is
// what is LEFT for the drivers, findings and claims written about members.
// That is the sentence the model prompt states, and this method is why the
// sentence is true. Its predecessor published the full member pool while
// telling the model the rows had already been taken out of it.
func (a ItemAllocation) PerMemberGrant() int {
	return a.Grant(contractsv1.ContextFabricItemBucketMember)
}

// GroupAllowance is the per-group allowance under the declared every_group
// rule: the group and multi_group grants TOGETHER, divided across the groups.
//
// BOTH GRANTS, and that is the correction round 3 forced. Deriving it from the
// direct-group grant alone makes a funded shared allowance vanish: five items
// of shared permission, three of them spent, were reported as breaching an
// allowance of two -- a false overrun handed to enforcement as fact. An item
// naming several groups is funded from the multi_group grant and participates
// in each group it names, so the allowance a group is measured against must
// include the shared funding its usage can come from.
//
// ZERO IS A REAL ALLOWANCE when Groups is positive: on a budget too small to
// give a group anything, every charged item is over its allowance. Absence is
// Groups == 0, which is a different statement and has its own value.
func (a ItemAllocation) GroupAllowance() int {
	if a.Groups <= 0 {
		return 0
	}
	total := a.Grant(contractsv1.ContextFabricItemBucketGroup) +
		a.Grant(contractsv1.ContextFabricItemBucketMultiGroup)
	return total / a.Groups
}

// NarrationDriverAllowance converts the narration ITEM grant into the number of
// members narration may narrate.
//
// Each narrated member costs driversPerMember driver judgments AND one minted
// claim per driver, so a narrated member charges 2x its drivers against the item
// budget. That doubling is what the static-cap version never saw.
func (a ItemAllocation) NarrationDriverAllowance(driversPerMember int) int {
	if !a.InForce() || a.NarrationGrant <= 0 || driversPerMember <= 0 {
		return 0
	}
	return a.NarrationGrant / (driversPerMember * 2)
}

// activeItemBuckets are the buckets that can receive items in this answer.
//
// An ungrouped answer has no group and no multi_group items by construction, so
// granting to those buckets would strand capacity on collections that cannot be
// filled. Their share goes to the buckets that can receive items instead.
func activeItemBuckets(groups int) []contractsv1.ContextFabricItemBucket {
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
// groups is how many group entities the cohort carries (zero with no group
// axis); memberRows is how many cohort member ROWS the answer will carry, and
// it is CHARGED -- committed off the top of the account before anything
// discretionary is granted. Its predecessor took the same parameter and never
// read it, which is how an answer could satisfy every published pool and still
// exceed the ceiling by exactly the number of rows.
//
// An unbounded budget (MaxItems <= 0) yields a zero allocation, which every
// consumer reads as "no quota in force" -- never as "a quota of zero".
func AllocateItems(plan AnswerPlan, groups, memberRows int) ItemAllocation {
	maxItems := plan.Budget.MaxItems
	if maxItems <= 0 {
		return ItemAllocation{MultiGroupCharge: MultiGroupChargeEveryGroup}
	}
	if groups < 0 {
		groups = 0
	}
	if memberRows < 0 {
		memberRows = 0
	}

	allocation := ItemAllocation{
		MaxItems:            maxItems,
		MemberRowCommitment: memberRows,
		Groups:              groups,
		MultiGroupCharge:    MultiGroupChargeEveryGroup,
	}
	if memberRows >= maxItems {
		// The mandatory rows alone meet or exceed the ceiling. Every grant
		// is zero and the commitment is clamped to the ceiling, so the
		// account still balances and the fact is REPORTED rather than
		// absorbed. Refusing here would make the allocator an enforcer.
		allocation.MemberRowCommitment = maxItems
		allocation.Infeasible = true
		return allocation
	}

	afterCommitment := maxItems - allocation.MemberRowCommitment
	active := activeItemBuckets(groups)

	// Narration is ONE CLAIMANT ALONGSIDE THE BUCKETS, which is what makes
	// the account balance: it is carved from the same capacity, never added
	// on top of buckets that are already fully granted.
	allocation.NarrationGrant = afterCommitment / (len(active) + 1)
	spendable := afterCommitment - allocation.NarrationGrant

	base := spendable / len(active)
	allocation.Residue = spendable - (base * len(active))
	for _, bucket := range active {
		for index, member := range contractsv1.ContextFabricItemBucketVocabulary() {
			if member != bucket {
				continue
			}
			allocation.Grants[index] = base
			// THE RESIDUE GOES TO GLOBAL, BY RULE. Publishing it as its
			// own free field is what let a wrong pool hide: any error
			// moved into it and the partition still balanced. Granted to
			// a named bucket, it is checkable -- Agreement re-derives
			// global as base+residue and every other active bucket as
			// base.
			if bucket == contractsv1.ContextFabricItemBucketGlobal {
				allocation.Grants[index] += allocation.Residue
			}
		}
	}
	return allocation
}

// AllocationDisagreement is the CLOSED vocabulary of ways the published grants
// can fail to describe one coherent permission to spend.
type AllocationDisagreement string

const (
	// AllocationAgrees is the zero value: the grants, the commitment and the
	// ceiling describe the same permitted spending.
	AllocationAgrees AllocationDisagreement = ""
	// AllocationGrantFormula: a grant, the narration grant or the residue
	// does not equal its declared derivation. This is the check the old
	// partition could not make: it SUMMED the parts instead of re-deriving
	// them, so an error that moved into the remainder still balanced.
	AllocationGrantFormula AllocationDisagreement = "grant_formula"
	// AllocationGrantedInactiveBucket: a bucket that cannot receive items in
	// this answer shape holds a positive grant, so capacity is stranded on a
	// collection that cannot be filled.
	AllocationGrantedInactiveBucket AllocationDisagreement = "granted_inactive_bucket"
	// AllocationCommitmentUnfunded: the member-row commitment is negative or
	// exceeds the ceiling.
	AllocationCommitmentUnfunded AllocationDisagreement = "commitment_unfunded"
)

// WHAT IS DELIBERATELY NOT A MEMBER HERE, because a guard that cannot fire is
// not an assertion:
//
//   - "the account does not add up". Once every grant equals its derivation,
//     commitment + narration + Σ grants == MaxItems FOLLOWS -- the derivations
//     are all functions of the ceiling and the commitment. The property is real
//     and is asserted directly, over a sweep, in the tests; it is not a runtime
//     token that could never be reached.
//   - "the residue is too large". The residue equals spendable − base×active by
//     derivation, which is smaller than the active count by construction. Its
//     predecessor bounded the remainder by the size of the bucket VOCABULARY
//     (four) rather than by the ACTIVE buckets (two, ungrouped) -- two items of
//     slack in exactly the regime the defect lived in -- and that whole class of
//     error is now a formula disagreement instead of a loose bound.
//   - "the group allowance disagrees". GroupAllowance is derived by ONE method
//     from the same grants, so there is no second derivation to disagree with.
//     The single-authority property is proved by the symbol test that no other
//     site computes a per-group allowance, not by comparing an expression to
//     itself.
var allocationDisagreements = [4]AllocationDisagreement{
	AllocationAgrees,
	AllocationGrantFormula,
	AllocationGrantedInactiveBucket,
	AllocationCommitmentUnfunded,
}

// AllocationDisagreementCount is the closed vocabulary's size.
const AllocationDisagreementCount = len(allocationDisagreements)

// AllocationDisagreementVocabulary returns it in published order.
func AllocationDisagreementVocabulary() [AllocationDisagreementCount]AllocationDisagreement {
	return allocationDisagreements
}

// Agreement checks that the grants, the commitment and the ceiling describe ONE
// permitted spending -- the third of the ledger's three checks.
//
// It RE-DERIVES rather than sums. A check that only asserted
// `commitment + narration + Σ grants == MaxItems` passes for every arrangement
// of the same total, which is how a member grant short by one survived a sweep
// over six ceilings, five group counts and eleven member counts with both
// packages green. Here each grant is recomputed from the formula it is supposed
// to follow and compared, so an error has nowhere to move to.
//
// An allocation with no quota in force agrees vacuously: there is nothing to
// disagree about, and reporting a disagreement for an unbounded answer would
// make the check fire on the majority path.
func (a ItemAllocation) Agreement() AllocationDisagreement {
	if !a.InForce() {
		return AllocationAgrees
	}
	if a.MemberRowCommitment < 0 || a.MemberRowCommitment > a.MaxItems {
		return AllocationCommitmentUnfunded
	}

	active := activeItemBuckets(a.Groups)
	isActive := map[contractsv1.ContextFabricItemBucket]bool{}
	for _, bucket := range active {
		isActive[bucket] = true
	}

	if a.Infeasible {
		// The mandatory rows meet or exceed the ceiling: every grant is
		// zero and the commitment is the whole ceiling. Checked, not
		// skipped -- an infeasible allocation that still published grants
		// would be permission to spend capacity that does not exist.
		for _, bucket := range contractsv1.ContextFabricItemBucketVocabulary() {
			if a.Grant(bucket) != 0 {
				return AllocationGrantFormula
			}
		}
		if a.NarrationGrant != 0 || a.Residue != 0 {
			return AllocationGrantFormula
		}
		if a.MemberRowCommitment != a.MaxItems {
			// The commitment is CLAMPED to the ceiling on this arm, which
			// is itself a derivation: an infeasible allocation whose
			// commitment says something else is not describing the
			// account it claims to.
			return AllocationGrantFormula
		}
		return AllocationAgrees
	}

	afterCommitment := a.MaxItems - a.MemberRowCommitment
	wantNarration := afterCommitment / (len(active) + 1)
	if a.NarrationGrant != wantNarration {
		return AllocationGrantFormula
	}
	spendable := afterCommitment - wantNarration
	base := spendable / len(active)
	wantResidue := spendable - (base * len(active))
	if a.Residue != wantResidue {
		return AllocationGrantFormula
	}

	for _, bucket := range contractsv1.ContextFabricItemBucketVocabulary() {
		granted := a.Grant(bucket)
		if !isActive[bucket] {
			if granted != 0 {
				return AllocationGrantedInactiveBucket
			}
			continue
		}
		want := base
		if bucket == contractsv1.ContextFabricItemBucketGlobal {
			want += a.Residue
		}
		if granted != want {
			return AllocationGrantFormula
		}
		// A bucket that CAN receive items is funded whenever there is
		// anything to distribute: `want` is base, and base is positive
		// whenever spendable reaches the active count. So "an active
		// bucket with no grant" -- class A, named -- is a formula
		// disagreement here rather than a token of its own, and the case
		// where every active grant is legitimately zero is exactly the
		// answer whose ceiling the mandatory rows nearly consumed.
	}
	return AllocationAgrees
}
