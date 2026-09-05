package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// allocationPlan is a plan carrying nothing but the ceiling, which is all the
// allocator reads. Built here rather than through PlanAnswer so a change in
// planning cannot silently change what these tests measure.
func allocationPlan(maxItems int) AnswerPlan {
	return AnswerPlan{Budget: AnswerPlanBudget{MaxItems: maxItems}}
}

// authorizedSpending is everything the published grants permit a producer to
// write, INCLUDING the mandatory rows the answer must carry.
//
// It is the number the whole seam is about: three rounds each found a different
// arrangement in which every published permission was respected and the answer
// still exceeded the ceiling.
func authorizedSpending(a ItemAllocation) int {
	return a.MemberRowCommitment + a.NarrationGrant + a.TotalGranted()
}

// TestJointlyAuthorizedSpendingCannotExceedTheCeiling is round 1's finding: the
// grants were disjoint claims on overlapping capacity, so every spender could
// respect its own permission and the answer still charged 34 against 30.
func TestJointlyAuthorizedSpendingCannotExceedTheCeiling(t *testing.T) {
	t.Parallel()
	const ceiling = 30
	allocation := AllocateItems(allocationPlan(ceiling), 2, 1)

	if got := allocation.Agreement(); got != AllocationAgrees {
		t.Fatalf("Agreement() = %q, want agreement", got)
	}
	if got := authorizedSpending(allocation); got != ceiling {
		t.Fatalf("jointly authorized spending = %d, want exactly %d "+
			"(commitment %d + narration %d + grants %d)", got, ceiling,
			allocation.MemberRowCommitment, allocation.NarrationGrant, allocation.TotalGranted())
	}
	// The positive control: narration is permitted SOMETHING, so this is not
	// passing by granting nothing to the claimant round 1 found unfunded.
	if allocation.NarrationGrant <= 0 {
		t.Fatalf("narration grant = %d; a zero grant would satisfy the ceiling by forbidding narration "+
			"entirely, which is not the property under test", allocation.NarrationGrant)
	}
	// And the recorded 34-item construction is no longer jointly authorized:
	// 1 member row + 27 global/group items + 3 narrated judgments + 3 minted
	// claims. Whatever the split, the permission cannot add to 34.
	if authorizedSpending(allocation) >= 34 {
		t.Fatalf("the grants still authorize %d items, and the recorded overrun was 34", authorizedSpending(allocation))
	}
}

// TestRowCommitmentsChangeTheGrants is round 3's finding: the allocator took a
// member count and never read it, so the allocation for zero members and for
// twenty-five members was byte-identical while the budget charged a row each.
func TestRowCommitmentsChangeTheGrants(t *testing.T) {
	t.Parallel()
	const ceiling = 30

	none := AllocateItems(allocationPlan(ceiling), 0, 0)
	ten := AllocateItems(allocationPlan(ceiling), 0, 10)
	many := AllocateItems(allocationPlan(ceiling), 0, 25)

	if none.Grants == ten.Grants && ten.Grants == many.Grants {
		t.Fatal("the grants are identical for 0, 10 and 25 member rows: the commitment is not being charged")
	}
	for _, testCase := range []struct {
		name  string
		rows  int
		alloc ItemAllocation
	}{
		{"no rows", 0, none},
		{"ten rows", 10, ten},
		{"twenty-five rows", 25, many},
	} {
		if testCase.alloc.MemberRowCommitment != testCase.rows {
			t.Errorf("%s: commitment = %d, want %d", testCase.name, testCase.alloc.MemberRowCommitment, testCase.rows)
		}
		if got := testCase.alloc.Agreement(); got != AllocationAgrees {
			t.Errorf("%s: Agreement() = %q", testCase.name, got)
		}
		if got := authorizedSpending(testCase.alloc); got != ceiling {
			t.Errorf("%s: authorized spending = %d, want %d", testCase.name, got, ceiling)
		}
	}
	// MONOTONE: more mandatory rows leave less discretionary capacity. A
	// commitment that moved the numbers around without reducing them would
	// satisfy the inequality above and still fund an overrun.
	if !(none.TotalGranted() > ten.TotalGranted() && ten.TotalGranted() > many.TotalGranted()) {
		t.Fatalf("discretionary grants are not decreasing in the row commitment: %d, %d, %d",
			none.TotalGranted(), ten.TotalGranted(), many.TotalGranted())
	}
	// The recorded 34: ten rows + nine candidates + nine member-attributed
	// items + six narration items. Under this allocation the candidate and
	// member grants cannot both be nine.
	if ten.Grant(contractsv1.ContextFabricItemBucketGlobal) >= 9 &&
		ten.PerMemberGrant() >= 9 &&
		ten.NarrationGrant >= 6 {
		t.Fatalf("the recorded 34-item combination is still fully authorized: global %d, per_member %d, narration %d, rows %d",
			ten.Grant(contractsv1.ContextFabricItemBucketGlobal), ten.PerMemberGrant(), ten.NarrationGrant, ten.MemberRowCommitment)
	}
}

// TestMemberAuthoredOutputIsFundedAndIsNetOfTheRows is round 2's finding (the
// member bucket had no grant at all) together with the prompt's claim: the
// number published as per_member must be what is LEFT after the rows, because
// that is the sentence the model is given.
func TestMemberAuthoredOutputIsFundedAndIsNetOfTheRows(t *testing.T) {
	t.Parallel()
	allocation := AllocateItems(allocationPlan(30), 1, 1)

	if allocation.PerMemberGrant() <= 0 {
		t.Fatal("member-attributed output has no grant: a producer may write items nothing funded")
	}
	if allocation.PerMemberGrant() != allocation.Grant(contractsv1.ContextFabricItemBucketMember) {
		t.Fatal("PerMemberGrant is not the member grant, so the prompt and the account disagree")
	}
	// NET OF THE ROWS, proved by construction rather than by reading the
	// comment: the rows are committed before any bucket is granted, so the
	// whole account -- rows included -- is the ceiling.
	if authorizedSpending(allocation) != 30 {
		t.Fatalf("authorized spending = %d, want 30", authorizedSpending(allocation))
	}
	rows := AllocateItems(allocationPlan(30), 1, 8)
	if rows.PerMemberGrant() >= allocation.PerMemberGrant() {
		t.Fatalf("per_member did not shrink when the row commitment grew: %d rows -> %d, %d rows -> %d",
			allocation.MemberRowCommitment, allocation.PerMemberGrant(), rows.MemberRowCommitment, rows.PerMemberGrant())
	}
}

// TestTheGroupAllowanceIncludesSharedFunding is round 3's multi-group finding
// from the GRANT side: an allowance derived from the direct-group grant alone
// makes a funded shared allowance vanish, and reports a breach for spending it
// itself authorized.
func TestTheGroupAllowanceIncludesSharedFunding(t *testing.T) {
	t.Parallel()
	allocation := AllocateItems(allocationPlan(30), 2, 1)

	direct := allocation.Grant(contractsv1.ContextFabricItemBucketGroup)
	shared := allocation.Grant(contractsv1.ContextFabricItemBucketMultiGroup)
	if direct <= 0 || shared <= 0 {
		t.Fatalf("this fixture needs both a direct and a shared grant to distinguish them: direct %d, shared %d", direct, shared)
	}
	if want := (direct + shared) / allocation.Groups; allocation.GroupAllowance() != want {
		t.Fatalf("GroupAllowance() = %d, want %d ((direct %d + shared %d) / %d groups)",
			allocation.GroupAllowance(), want, direct, shared, allocation.Groups)
	}
	// The defect, stated as the comparison it made: the direct-only
	// derivation is STRICTLY SMALLER here, so a fixture that produced the
	// same number both ways could not tell them apart.
	if directOnly := direct / allocation.Groups; directOnly >= allocation.GroupAllowance() {
		t.Fatalf("the direct-only derivation (%d) is not smaller than the published allowance (%d); "+
			"this fixture cannot witness the defect", directOnly, allocation.GroupAllowance())
	}
	// An answer with no group axis publishes ABSENCE (no groups), never a
	// per-group allowance of zero.
	flat := AllocateItems(allocationPlan(30), 0, 1)
	if flat.Groups != 0 || flat.GroupAllowance() != 0 {
		t.Fatalf("a flat answer published groups=%d allowance=%d", flat.Groups, flat.GroupAllowance())
	}
	if flat.Grant(contractsv1.ContextFabricItemBucketGroup) != 0 ||
		flat.Grant(contractsv1.ContextFabricItemBucketMultiGroup) != 0 {
		t.Fatal("a flat answer granted capacity to buckets that cannot receive items")
	}
}

// TestABoundedZeroAllowanceIsARealQuota: on a ceiling the mandatory rows
// consume, a group's allowance is zero and that is a MEASURED zero with a
// positive group count -- distinct from an unbounded plan, which publishes no
// quota at all. Round 2 found the exposure skipping exactly this case.
func TestABoundedZeroAllowanceIsARealQuota(t *testing.T) {
	t.Parallel()
	bounded := AllocateItems(allocationPlan(1), 1, 1)

	if !bounded.InForce() {
		t.Fatal("a ceiling of 1 is a quota in force")
	}
	if !bounded.Infeasible {
		t.Fatal("one mandatory row against a ceiling of one is infeasible and must be reported as such")
	}
	if bounded.Groups != 1 {
		t.Fatalf("Groups = %d, want 1: the group axis exists even when nothing can be granted", bounded.Groups)
	}
	if bounded.GroupAllowance() != 0 {
		t.Fatalf("GroupAllowance() = %d, want 0", bounded.GroupAllowance())
	}
	if got := bounded.Agreement(); got != AllocationAgrees {
		t.Fatalf("an infeasible allocation must still be coherent; Agreement() = %q", got)
	}
	if bounded.TotalGranted() != 0 || bounded.NarrationGrant != 0 {
		t.Fatalf("an infeasible allocation granted capacity: grants %d, narration %d",
			bounded.TotalGranted(), bounded.NarrationGrant)
	}

	// The control: no ceiling means NO quota, never a quota of zero. A
	// consumer that cannot tell these apart tells the model to write nothing.
	unbounded := AllocateItems(allocationPlan(0), 1, 1)
	if unbounded.InForce() {
		t.Fatal("an unbounded plan published a quota")
	}
	if unbounded.Infeasible {
		t.Fatal("an unbounded plan reported an infeasible commitment")
	}
}

// TestEveryAllocationOverTheSweepAgrees is the breadth check: the same sweep
// the previous shape passed while carrying a one-item error, now run against a
// check that RE-DERIVES each grant instead of summing them.
func TestEveryAllocationOverTheSweepAgrees(t *testing.T) {
	t.Parallel()
	checked := 0
	for _, ceiling := range []int{1, 2, 5, 30, 45, 300} {
		for groups := 0; groups <= 4; groups++ {
			for members := 0; members <= 10; members++ {
				allocation := AllocateItems(allocationPlan(ceiling), groups, members)
				if got := allocation.Agreement(); got != AllocationAgrees {
					t.Errorf("ceiling %d groups %d members %d: Agreement() = %q (%+v)",
						ceiling, groups, members, got, allocation)
				}
				if got := authorizedSpending(allocation); got != ceiling {
					t.Errorf("ceiling %d groups %d members %d: authorized %d, want %d",
						ceiling, groups, members, got, ceiling)
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("the sweep ran zero cases")
	}
	t.Logf("swept %d allocations", checked)
}

// TestTheAgreementRefusesEveryWayTheGrantsCanBeWrong reaches every member of
// the disagreement vocabulary.
//
// A token nothing can produce is a token that will never fire in production
// either, and a check whose failure modes are untested is a check nobody has
// seen fail. Each case tampers with ONE property of an otherwise coherent
// allocation, which is also what makes the tokens distinguishable.
func TestTheAgreementRefusesEveryWayTheGrantsCanBeWrong(t *testing.T) {
	t.Parallel()
	grouped := func() ItemAllocation { return AllocateItems(allocationPlan(30), 2, 1) }
	if grouped().Agreement() != AllocationAgrees {
		t.Fatal("the baseline allocation does not agree; every case below would be meaningless")
	}

	memberIndex, globalIndex, groupIndex := -1, -1, -1
	for index, bucket := range contractsv1.ContextFabricItemBucketVocabulary() {
		switch bucket {
		case contractsv1.ContextFabricItemBucketMember:
			memberIndex = index
		case contractsv1.ContextFabricItemBucketGlobal:
			globalIndex = index
		case contractsv1.ContextFabricItemBucketGroup:
			groupIndex = index
		}
	}
	if memberIndex < 0 || globalIndex < 0 || groupIndex < 0 {
		t.Fatal("the bucket vocabulary no longer carries global, member and group")
	}

	cases := []struct {
		name   string
		tamper func(ItemAllocation) ItemAllocation
		want   AllocationDisagreement
	}{
		{
			// The exact mutation the previous shape survived: one item
			// short in the member bucket, the difference parked in the
			// residue so the partition still balanced.
			name: "a member grant one short, absorbed by the residue",
			tamper: func(a ItemAllocation) ItemAllocation {
				a.Grants[memberIndex]--
				a.Grants[globalIndex]++
				return a
			},
			want: AllocationGrantFormula,
		},
		{
			name: "a bucket that can receive items has no grant",
			tamper: func(a ItemAllocation) ItemAllocation {
				a.Grants[memberIndex] = 0
				return a
			},
			want: AllocationGrantFormula,
		},
		{
			name: "capacity granted to a bucket that cannot be filled",
			tamper: func(a ItemAllocation) ItemAllocation {
				flat := AllocateItems(allocationPlan(30), 0, 1)
				flat.Grants[groupIndex] = 3
				return flat
			},
			want: AllocationGrantedInactiveBucket,
		},
		{
			name: "the residue is not smaller than the active bucket count",
			tamper: func(a ItemAllocation) ItemAllocation {
				a.Residue = len(activeItemBuckets(a.Groups))
				return a
			},
			want: AllocationGrantFormula,
		},
		{
			name: "the ceiling is moved under a fixed set of grants",
			tamper: func(a ItemAllocation) ItemAllocation {
				a.MaxItems++
				return a
			},
			want: AllocationGrantFormula,
		},
		{
			name: "the narration grant is taken from the buckets' share",
			tamper: func(a ItemAllocation) ItemAllocation {
				a.NarrationGrant++
				return a
			},
			want: AllocationGrantFormula,
		},
		{
			name: "the commitment exceeds the ceiling",
			tamper: func(a ItemAllocation) ItemAllocation {
				a.MemberRowCommitment = a.MaxItems + 1
				return a
			},
			want: AllocationCommitmentUnfunded,
		},
		{
			name: "an infeasible allocation still publishes grants",
			tamper: func(a ItemAllocation) ItemAllocation {
				infeasible := AllocateItems(allocationPlan(1), 1, 4)
				infeasible.Grants[globalIndex] = 1
				return infeasible
			},
			want: AllocationGrantFormula,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := testCase.tamper(grouped()).Agreement(); got != testCase.want {
				t.Fatalf("Agreement() = %q, want %q", got, testCase.want)
			}
		})
	}

	// EVERY member of the vocabulary is produced by a case above. A token
	// nothing can reach is a token that will never fire in production, and
	// this file is where that is caught rather than in a review.
	produced := map[AllocationDisagreement]struct{}{AllocationAgrees: {}}
	for _, testCase := range cases {
		produced[testCase.want] = struct{}{}
	}
	for _, member := range AllocationDisagreementVocabulary() {
		if _, reached := produced[member]; !reached {
			t.Errorf("disagreement %q is declared but no case in this test can produce it", member)
		}
	}
}

// TestTheDisagreementVocabularyIsClosedAndComplete keeps a token from shipping
// unnamed, and pins that the empty value means agreement rather than an unset
// check.
func TestTheDisagreementVocabularyIsClosedAndComplete(t *testing.T) {
	t.Parallel()
	vocabulary := AllocationDisagreementVocabulary()
	if len(vocabulary) != AllocationDisagreementCount {
		t.Fatalf("vocabulary has %d members, count says %d", len(vocabulary), AllocationDisagreementCount)
	}
	if vocabulary[0] != AllocationAgrees {
		t.Errorf("the first member is %q; agreement is the zero value and is published first", vocabulary[0])
	}
	seen := map[AllocationDisagreement]struct{}{}
	for _, member := range vocabulary {
		if _, duplicate := seen[member]; duplicate {
			t.Errorf("member %q appears twice", member)
		}
		seen[member] = struct{}{}
	}
	if len(seen) != AllocationDisagreementCount {
		t.Errorf("%d distinct members, count says %d", len(seen), AllocationDisagreementCount)
	}
}

// TestNarrationIsOneClaimantAlongsideTheBuckets pins round 1's shape: narration
// is carved from the same capacity as the buckets, never added on top of a
// capacity that is already fully granted.
func TestNarrationIsOneClaimantAlongsideTheBuckets(t *testing.T) {
	t.Parallel()
	allocation := AllocateItems(allocationPlan(30), 2, 1)

	if allocation.NarrationGrant <= 0 {
		t.Fatal("narration has no grant, so this fixture cannot see the overlap")
	}
	if allocation.MemberRowCommitment+allocation.TotalGranted()+allocation.NarrationGrant != allocation.MaxItems {
		t.Fatalf("narration is not inside the account: commitment %d + grants %d + narration %d != %d",
			allocation.MemberRowCommitment, allocation.TotalGranted(), allocation.NarrationGrant, allocation.MaxItems)
	}
	// The item grant converts to MEMBERS to narrate at two items each --
	// one judgment and one minted claim -- which is the doubling the
	// static-cap version never saw.
	const driversPerMember = 3
	if got, want := allocation.NarrationDriverAllowance(driversPerMember), allocation.NarrationGrant/(driversPerMember*2); got != want {
		t.Fatalf("NarrationDriverAllowance(%d) = %d, want %d", driversPerMember, got, want)
	}
	if AllocateItems(allocationPlan(0), 2, 1).NarrationDriverAllowance(driversPerMember) != 0 {
		t.Fatal("an unbounded plan produced a narration allowance; it must fall back to the static caps instead")
	}
}
