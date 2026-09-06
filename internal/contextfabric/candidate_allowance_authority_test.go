package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The candidate allowance has ONE authority: the allocation's own ledger.
//
// These exist because a keystone review measured the consequence of it having
// two. The allocator apportions MaxItems; `narrowCandidatesToBudget` then
// derived its own allowance from `measurement.Items.Budgeted()` and the raw
// ceiling, without reference to the allocation at all. The PR2B design of
// record (2026-09-04) names that row as the double-count to rebase and warned
// no compiler would catch it.
//
// The allowance is the REMAINDER, not a predicted share: the non-member
// allowance is one shared headroom (rulings 2026-09-04), so candidates get what
// the ceiling has left after every other bucket's REAL spend, with members
// floored at their committed rows. Bounding candidates by the allocator's
// predicted global grant was tried and rejected — it zeroed a regime that
// serves thirteen candidates.

// candidateResult is a result carrying `declared` resolution candidates.
func candidateResult(declared int) InvestigationResult {
	proposed := outcomeAssemblyCandidates(declared)
	return InvestigationResult{
		SubjectResolution: SubjectResolution{Candidates: proposed, Committed: []SubjectRef{}},
	}
}

// TestTheCandidateAllowanceComesFromTheAllocationLedger is the kill target for
// the battery arm, and it is written at the ONE place the two arithmetics
// provably diverge.
//
// `ContextFabricItemAttribution.Total()` is defined to equal
// `ContextFabricResultItemCounts.Budgeted()` for the same result, so summing
// the ledger's buckets and reading `Budgeted()` agree by construction — a
// mutant swapping one for the other is invisible almost everywhere. The MEMBER
// FLOOR is what separates them: the rows are committed off the top whether or
// not the document carries that many yet, so when the measured member count is
// BELOW the commitment, the ledger-based allowance is smaller and the
// `Budgeted()`-based one hands candidates capacity the allocator already spent.
func TestTheCandidateAllowanceComesFromTheAllocationLedger(t *testing.T) {
	t.Parallel()
	const ceiling = 30
	const rows = 10
	const declared = 12

	allocation := AllocateItems(allocationPlan(ceiling), 0, rows)
	if allocation.MemberRowCommitment != rows {
		t.Fatalf("MemberRowCommitment = %d, want %d", allocation.MemberRowCommitment, rows)
	}

	// The document carries only 2 member items so far against 10 COMMITTED
	// rows, 14 global items of which 12 are candidates, and 9 group-attributed
	// items. The member shortfall is the divergence, and the group spend is
	// what makes the remainder actually bind:
	//
	//   LEDGER    30 - (2 non-candidate global + max(2 measured, 10 committed) + 5 + 4) =  9  -> serves 9
	//   Budgeted  30 - (25 budgeted - 12 declared)                                      = 17  -> serves all 12
	measurement := ResponseMeasurement{
		Items:       contractsv1.ContextFabricResultItemCounts{Candidates: declared, ClaimedFacts: 13},
		Attribution: contractsv1.ContextFabricItemAttribution{Global: 14, Member: 2, Group: 5, MultiGroup: 4},
	}

	nonCandidateGlobal := measurement.Attribution.Global - declared
	floored := measurement.Attribution.Member
	if floored < allocation.MemberRowCommitment {
		floored = allocation.MemberRowCommitment
	}
	ledgerAllowance := ceiling - (nonCandidateGlobal + floored +
		measurement.Attribution.Group + measurement.Attribution.MultiGroup)
	budgetedAllowance := ceiling - (measurement.Items.Budgeted() - declared)

	if ledgerAllowance == budgetedAllowance {
		t.Fatalf("fixture is not discriminating: both arithmetics give %d, so a mutant swapping the "+
			"ledger for Budgeted() would survive this test", ledgerAllowance)
	}
	if budgetedAllowance <= ledgerAllowance {
		t.Fatalf("the Budgeted() form gives %d and the ledger form %d; the whole point is that ignoring "+
			"the member floor hands candidates MORE capacity than the allocator left",
			budgetedAllowance, ledgerAllowance)
	}
	// And the ledger allowance must BIND, or the reduction returns at its
	// would-not-reduce guard and the served count never exercises either form.
	if ledgerAllowance >= declared {
		t.Fatalf("the ledger allowance is %d against %d declared: it does not bind, so this fixture "+
			"cannot see which arithmetic ran", ledgerAllowance, declared)
	}

	_, narrowing, declinedReason := narrowCandidatesToBudget(
		candidateResult(declared), ResponseBudget{MaxItems: ceiling}, allocation, measurement,
		contractsv1.ContextFabricBudgetOverrunItems)
	if declinedReason != OutcomeReductionNotApplicable {
		t.Fatalf("declined = %q, want the reduction to have run", declinedReason)
	}

	want := ledgerAllowance
	if want > declared {
		want = declared
	}
	if narrowing.Served != want {
		t.Errorf("served %d of %d, want %d. The allowance must be the ceiling less every non-candidate "+
			"bucket's spend READ FROM THE ALLOCATION'S LEDGER, with members floored at the %d committed "+
			"rows (%d) -- not the ceiling less Budgeted()-minus-declared, which would allow %d and "+
			"spend capacity the allocator already committed.",
			narrowing.Served, narrowing.Declared, want,
			allocation.MemberRowCommitment, ledgerAllowance, budgetedAllowance)
	}
}

// TestTheUnboundedArmIsUntouchedByTheRebase is condition 1: no ceiling, no
// change. The unbounded arm returns at its own precondition, before any
// allowance is computed — asserted rather than reasoned about, because
// "unreachable" is a claim like any other.
func TestTheUnboundedArmIsUntouchedByTheRebase(t *testing.T) {
	t.Parallel()
	const declared = 12
	unbounded := AllocateItems(allocationPlan(0), 0, 0)
	if unbounded.InForce() {
		t.Fatal("the fixture allocation is in force; this test is about the arm where none is")
	}

	result, narrowing, declinedReason := narrowCandidatesToBudget(
		candidateResult(declared), ResponseBudget{MaxItems: 0}, unbounded,
		ResponseMeasurement{Items: contractsv1.ContextFabricResultItemCounts{Candidates: declared}},
		contractsv1.ContextFabricBudgetOverrunItems)

	if declinedReason != OutcomeReductionNoItemBudget {
		t.Errorf("declined = %q, want %q -- the unbounded arm must return at its own precondition",
			declinedReason, OutcomeReductionNoItemBudget)
	}
	if narrowing.Narrowed {
		t.Error("the unbounded arm narrowed something; with no ceiling there is nothing to narrow to")
	}
	if narrowing.Served != declared || narrowing.Declared != declared {
		t.Errorf("served/declared = %d/%d, want %d/%d untouched",
			narrowing.Served, narrowing.Declared, declared, declared)
	}
	if got := len(result.SubjectResolution.Candidates); got != declared {
		t.Errorf("the candidate list holds %d entries, want the %d it was given untouched", got, declared)
	}
}

// TestCandidatesAreReducibleAndNotCommitted is the DESIGN OF RECORD, pinned.
//
// A keystone review reported it as a defect: the allocator commits member rows
// off the top but never receives a candidate count, so a prompt-compliant
// synthesis spends its grants and the engine then trims candidates to fit. That
// is not a defect. It is the design (2026-09-04): the allocator owns the QUOTA
// and passes it into narrowing as an input; `narrowCandidatesToBudget` is the
// SOLE ENFORCER and reduces candidates; candidates sit in the shared
// non-member headroom, discretionary and reducible.
//
// Committing them off the top would create a second authority over the
// allowance — the risk the design forbids. So this asserts the trimming HAPPENS
// and that `Agreement()` stays green while it does, and it names why: Agreement
// is over the COMMITTED basis, and candidates are not in it.
func TestCandidatesAreReducibleAndNotCommitted(t *testing.T) {
	t.Parallel()
	const ceiling = 30
	const rows = 6
	const declared = 12

	// The allocation is IDENTICAL whether or not candidates exist, because
	// candidates are not part of its basis. That is the design, as an assertion.
	allocation := AllocateItems(allocationPlan(ceiling), 0, rows)
	if allocation != AllocateItems(allocationPlan(ceiling), 0, rows) {
		t.Fatal("AllocateItems is not a function of (plan, groups, rows) alone")
	}
	if got := allocation.Agreement(); got != AllocationAgrees {
		t.Fatalf("Agreement() = %q on the honest allocation, want agreement", got)
	}
	if allocation.MemberRowCommitment != rows {
		t.Fatalf("MemberRowCommitment = %d, want the %d mandatory rows charged off the top",
			allocation.MemberRowCommitment, rows)
	}

	// Enough non-candidate spend that the remainder genuinely binds: 4
	// non-candidate global items and 18 member items leaves 8 for 12 declared.
	measurement := ResponseMeasurement{
		Items:       contractsv1.ContextFabricResultItemCounts{Candidates: declared, ClaimedFacts: 22},
		Attribution: contractsv1.ContextFabricItemAttribution{Global: declared + 4, Member: 18},
	}
	wantServed := ceiling - (4 + 18)

	_, narrowing, declinedReason := narrowCandidatesToBudget(
		candidateResult(declared), ResponseBudget{MaxItems: ceiling}, allocation, measurement,
		contractsv1.ContextFabricBudgetOverrunItems)

	if declinedReason != OutcomeReductionNotApplicable {
		t.Fatalf("declined = %q, want the enforcer to have run", declinedReason)
	}
	// THE ENFORCER TRIMS. That is the expected outcome, not a defect.
	if !narrowing.Narrowed {
		t.Error("the enforcer narrowed nothing; candidates are the one thing it exists to trim")
	}
	if narrowing.Served != wantServed {
		t.Errorf("served %d of %d candidates, want %d (the ceiling less the 4 non-candidate global and "+
			"18 member items)", narrowing.Served, narrowing.Declared, wantServed)
	}
	if narrowing.Served >= narrowing.Declared {
		t.Errorf("served/declared = %d/%d is not a reduction", narrowing.Served, narrowing.Declared)
	}
	// AND THE ACCOUNT STILL AGREES, because candidates were never in its basis.
	// An allocation that had committed them would have to change when they were
	// trimmed; this one does not.
	if got := allocation.Agreement(); got != AllocationAgrees {
		t.Errorf("Agreement() = %q after the enforcer trimmed candidates, want agreement -- the "+
			"allocator's basis is committed rows, narration and the bucket grants, and candidates are "+
			"not part of it", got)
	}
}
