package contextfabric

import (
	"errors"
	"strconv"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Every test here exists because a mutation battery found the property it
// asserts was unreachable: the code was right and nothing held it to being
// right, so each guard could be deleted with the whole package still green.

// TestAnUnaccountedDocumentGetsNoQuotaStatement covers every arm of the
// availability decision, and the accounting-disagreement arm is why it is a
// direct test of the classifier rather than one driven through MeasureAttempt.
//
// Every InvestigationResult a test can construct RECONCILES by construction --
// the ledger is three walks over the same document, so a disagreement is a
// defect inside the contracts package, never something a caller hands in. Read
// from a real ledger the arm can therefore never fire in a test, which is
// exactly how it survived deletion. Taking `reconciled` as an argument makes a
// guard against someone else's defect assertable without one.
func TestAnUnaccountedDocumentGetsNoQuotaStatement(t *testing.T) {
	t.Parallel()
	// A grouped allocation with a positive allowance: on a reconciled
	// document this is the `bounded` arm, so any other answer below is
	// caused by the reconciled flag and nothing else.
	grouped := AllocateItems(allocationPlan(60), 2, 2)
	if grouped.GroupAllowance() <= 0 {
		t.Fatalf("fixture allocation has a %d group allowance; the arms below would not be "+
			"distinguishable", grouped.GroupAllowance())
	}

	cases := []struct {
		name       string
		reconciled bool
		allocation ItemAllocation
		allowance  int
		want       ItemQuotaAvailability
	}{
		{
			// THE ARM THE BATTERY FOUND UNTESTED. The allocation is in
			// force, groups exist and the allowance is positive -- every
			// condition for a real quota is met, and the account still
			// refuses to make a statement.
			name:       "an unreconciled account refuses a quota it could otherwise state",
			reconciled: false, allocation: grouped, allowance: grouped.GroupAllowance(),
			want: ItemQuotaAccountingDisagreement,
		},
		{
			name:       "no ceiling in force is unbounded, not a quota of zero",
			reconciled: true, allocation: ItemAllocation{}, allowance: 0,
			want: ItemQuotaUnbounded,
		},
		{
			name:       "a bounded answer with no group axis has no per-group quota",
			reconciled: true, allocation: AllocateItems(allocationPlan(60), 0, 2), allowance: 0,
			want: ItemQuotaUnavailable,
		},
		{
			// A REAL, MEASURED quota. Distinct from unavailable: a group
			// axis exists and every group-naming item breaches it.
			name:       "a zero allowance over a real group axis is bounded_zero",
			reconciled: true, allocation: grouped, allowance: 0,
			want: ItemQuotaBoundedZero,
		},
		{
			name:       "a positive allowance over a real group axis is bounded",
			reconciled: true, allocation: grouped, allowance: grouped.GroupAllowance(),
			want: ItemQuotaBounded,
		},
	}

	seen := map[ItemQuotaAvailability]bool{}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			got := classifyItemQuota(one.reconciled, one.allocation, one.allowance)
			if got != one.want {
				t.Fatalf("classifyItemQuota(reconciled=%v, groups=%d, allowance=%d) = %q, want %q",
					one.reconciled, one.allocation.Groups, one.allowance, got, one.want)
			}
			if !ValidItemQuotaAvailability(got) {
				t.Fatalf("%q is not a member of the closed vocabulary", got)
			}
		})
		seen[one.want] = true
	}

	// The vocabulary is CLOSED, so every member must be reachable. A member
	// no arm can produce is a value telemetry declares and nothing emits,
	// which is the same defect as an arm nothing tests.
	for _, member := range ItemQuotaAvailabilityVocabulary() {
		if !seen[member] {
			t.Errorf("no case produces %q, so that member of the vocabulary is unreachable", member)
		}
	}
}

// groupIncidenceResult builds a result whose FIRST declared group is named by
// groupItems drivers and whose SECOND is named by none.
//
// That asymmetry is the whole fixture. A summing comparison and a per-group one
// agree whenever usage is spread evenly; they disagree exactly when one group
// carries usage another group's unused allowance could pay for -- which is the
// shape the shared-pool arithmetic hid.
func groupIncidenceResult(groupItems int) InvestigationResult {
	cohort := &Cohort{
		Kind: SubjectTeam, Rationale: "fixture",
		Members: []CohortMember{
			{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "team_a"},
				Rank: 1, InclusionReasons: []string{"matched"}},
		},
		Groups: []contractsv1.ContextFabricCohortGroup{
			{Subject: attributionGroupRef(0), MemberCanonicalIDs: []string{"team_a"}, Complete: true, Total: 1},
			{Subject: attributionGroupRef(1), MemberCanonicalIDs: []string{}, Complete: true, Total: 0},
		},
	}
	drivers := make([]DriverJudgment, 0, groupItems)
	for index := 0; index < groupItems; index++ {
		drivers = append(drivers, DriverJudgment{
			DriverID: "d_group_" + strconv.Itoa(index), Standing: DriverPrincipal, Category: "relationship",
			Title: "Group fixture driver", Summary: "A driver naming exactly one group.",
			AffectedSubjects: []SubjectRef{attributionGroupRef(0)},
			EvidenceRefIDs:   []string{attributionEvidenceRef},
			Derivation:       DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved,
			Confidence: 0.9, Current: true,
		})
	}
	return InvestigationResult{
		Status: InvestigationComplete, DirectJudgment: "Fine.", CurrentState: "Nominal.",
		StrongestPressures: []string{}, Drivers: drivers, RemainingWork: []Finding{},
		ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
		Limitations: []string{}, EvidenceRefIDs: []string{attributionEvidenceRef},
		ClaimedFacts: []ClaimedFact{}, Cohort: cohort,
		Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		DeterministicAnswer: "Fine, based on available context.", Warnings: []string{},
		Versions: VersionSet{
			Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
			InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
		},
	}
}

// TestGroupUsageIsMeasuredPerGroupAndNeverSummed is the named defect, asserted.
//
// One group carries one item MORE than its own allowance; the other carries
// nothing. Per group that is one group over quota. Summed against an aggregate
// capacity of allowance x groups it is allowance+1 against 2xallowance -- under
// quota, reported as zero over. Eighteen drivers each naming both of two groups
// measured 18 <= 18 and reported zero over quota while each group carried all
// eighteen; this is the same arithmetic at the smallest size that shows it.
func TestGroupUsageIsMeasuredPerGroupAndNeverSummed(t *testing.T) {
	t.Parallel()
	const ceiling = 60
	const groups = 2
	allocation := AllocateItems(allocationPlan(ceiling), groups, 1)
	allowance := allocation.GroupAllowance()
	if allowance < 1 {
		t.Fatalf("fixture allowance = %d; the summed and per-group comparisons only diverge for a "+
			"positive allowance", allowance)
	}

	result := groupIncidenceResult(allowance + 1)
	attempt, err := MeasureAttempt(allocation, result, ResponseBudget{MaxItems: ceiling})
	if err != nil {
		t.Fatalf("MeasureAttempt() error = %v", err)
	}

	// PREMISES, asserted: a document that did not reconcile, or a quota that
	// was not bounded, would make every count below a well-formed zero.
	if !attempt.Reconciled() {
		t.Fatalf("the fixture document does not reconcile: %q %s",
			attempt.Ledger.Status, attempt.Ledger.Disagreement)
	}
	if attempt.Availability != ItemQuotaBounded {
		t.Fatalf("availability = %q, want %q", attempt.Availability, ItemQuotaBounded)
	}
	// BOTH declared groups are measured, including the one nothing names: a
	// group with a quota and no usage is a measured zero, not an absent key,
	// and it is that group's unused allowance the summed comparison spends.
	if attempt.GroupsMeasured != groups {
		t.Fatalf("GroupsMeasured = %d, want %d -- the empty group must be a measured zero",
			attempt.GroupsMeasured, groups)
	}

	// THE ASSERTION. One group is over its own allowance.
	if attempt.GroupsOverAllowance != 1 {
		t.Fatalf("GroupsOverAllowance = %d, want 1: one group carries %d items against an allowance "+
			"of %d, and the other carries none",
			attempt.GroupsOverAllowance, allowance+1, allowance)
	}

	// And the discriminator stated as arithmetic, so a reader can see that
	// the two rules genuinely disagree on this fixture rather than take it
	// on trust: summed usage is under the aggregate capacity, which is the
	// answer the shared-pool rule would have given.
	summed := allowance + 1
	if aggregate := allowance * groups; summed > aggregate {
		t.Fatalf("fixture is not discriminating: summed usage %d already exceeds the aggregate "+
			"capacity %d, so a summing rule would report an overrun too", summed, aggregate)
	}
}

// TestACertifiedFitIsNotMerelyTheAbsenceOfAnOverrun is the third property the
// battery found unheld.
//
// `CertifiedFit()` could be made to return true unconditionally with the whole
// package still green, because every assertion about it ran on a fixture where
// it was true anyway. It is NOT the negation of Overrun, and the two are
// deliberately both carried: an unbounded answer and an unreconciled one are
// each "not overrun", and neither is certified. A dashboard reading only Fits
// cannot tell "we checked it against a ceiling" from "there was no ceiling to
// check".
func TestACertifiedFitIsNotMerelyTheAbsenceOfAnOverrun(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		capacity contractsv1.ContextFabricCapacityVerdict
		want     bool
	}{
		{"a real certificate", contractsv1.ContextFabricCapacityCertifiedFit, true},
		{"an overdraw", contractsv1.ContextFabricCapacityOverdraw, false},
		{"an unbounded budget", contractsv1.ContextFabricCapacityUnbounded, false},
		{"an accounting defect", contractsv1.ContextFabricCapacityAccountingDefect, false},
	}
	certified := 0
	for _, one := range cases {
		attempt := MeasuredAttempt{Capacity: one.capacity}
		if got := attempt.CertifiedFit(); got != one.want {
			t.Errorf("%s: CertifiedFit() = %v, want %v (capacity %q)", one.name, got, one.want, one.capacity)
		}
		if one.want {
			certified++
		}
	}
	// A negative control on the TEST: if every case expected true, an
	// unconditional `return true` would satisfy all of them.
	if certified != 1 {
		t.Fatalf("%d cases expect a certificate; exactly one must, or the cases cannot discriminate an "+
			"unconditional true", certified)
	}

	// And the two axes are independent: an unbounded answer is NOT overrun
	// and is NOT certified, which is the pair a single boolean loses.
	unbounded := MeasuredAttempt{
		Capacity: contractsv1.ContextFabricCapacityUnbounded,
		Overrun:  contractsv1.ContextFabricBudgetFits,
	}
	if unbounded.CertifiedFit() {
		t.Error("an unbounded answer reports a certified fit; there was no ceiling to certify against")
	}
	if unbounded.Overrun != contractsv1.ContextFabricBudgetFits {
		t.Error("the fixture no longer holds the two axes apart")
	}
}

// unreconciledLedger is a ledger that did not reconcile.
//
// Built as a literal because it cannot be PRODUCED: the reconciler is three
// walks over one document, so a disagreement is a defect inside the contracts
// package and never something a caller can hand in. That is precisely why the
// refusal below needs a synthetic one. A guard against someone else's defect is
// still a guard, and it is still worth holding to being right.
func unreconciledLedger() contractsv1.ContextFabricItemLedger {
	return contractsv1.ContextFabricItemLedger{
		Status:       contractsv1.ContextFabricLedgerCollectionDisagreement,
		Disagreement: "drivers:4!=5",
	}
}

// TestAnAccountingDisagreementIsAnInternalErrorAndNeverA413 is the acceptance
// criterion, asserted rather than described.
//
// The caller's question was not too big; the server could not account for its
// own answer. Telling them otherwise blames a correct request for a server
// defect, and the two are rendered by different status codes.
func TestAnAccountingDisagreementIsAnInternalErrorAndNeverA413(t *testing.T) {
	t.Parallel()
	const stage = "assembled_result"

	// A reconciled ledger raises nothing. Without this the test would pass
	// against a constructor that refused everything.
	if err := itemAccountingErrorForLedger(stage, contractsv1.ContextFabricItemLedger{
		Status: contractsv1.ContextFabricLedgerReconciled,
	}); err != nil {
		t.Fatalf("a reconciled ledger raised %v, want no error", err)
	}

	ledger := unreconciledLedger()
	err := itemAccountingErrorForLedger(stage, ledger)
	if err == nil {
		t.Fatal("an unreconciled ledger raised no error at all: the account is allowed not to add up")
	}
	if !errors.Is(err, ErrItemAccounting) {
		t.Errorf("errors.Is(err, ErrItemAccounting) = false for %v; alerting cannot find it", err)
	}
	if !errors.Is(err, ErrInvalidResult) {
		t.Errorf("errors.Is(err, ErrInvalidResult) = false for %v; the route would not classify it as a "+
			"server-side result defect", err)
	}
	// THE ONE THAT MATTERS.
	if errors.Is(err, ErrAnswerExceedsBudget) {
		t.Errorf("errors.Is(err, ErrAnswerExceedsBudget) = true for %v: an accounting defect would reach "+
			"the caller as \"your question was too big\"", err)
	}
	var typed ItemAccountingError
	if !errors.As(err, &typed) {
		t.Fatalf("errors.As(err, &ItemAccountingError{}) = false for %v", err)
	}
	if typed.Stage != stage || typed.Status != ledger.Status || typed.Disagreement != ledger.Disagreement {
		t.Errorf("the typed error = %+v, want it to carry stage %q and the ledger's own verdict", typed, stage)
	}

	// The event is built from the SAME ledger by the SAME pair, so the
	// emitted line and the raised error cannot describe different documents.
	event := itemAccountingEventFor(stage, ledger, 30)
	if event.Stage != typed.Stage || event.Status != typed.Status || event.Disagreement != typed.Disagreement {
		t.Errorf("event = %+v disagrees with the error %+v about the same ledger", event, typed)
	}
	if event.MaxItems != 30 {
		t.Errorf("event.MaxItems = %d, want 30", event.MaxItems)
	}
}
