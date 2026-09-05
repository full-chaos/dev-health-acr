package v1

import (
	"reflect"
	"testing"
)

// ledgerGroupedFixture is a grouped answer in which every charged collection is
// non-empty and every bucket is reached, so a fixture that quietly stopped
// covering one is visible in the assertions rather than silently reducing what
// this file tests.
func ledgerGroupedFixture() ContextFabricInvestigationResult {
	memberA := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_a", Label: "A"}
	memberB := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_b", Label: "B"}
	groupOne := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_one", Label: "One"}
	groupTwo := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_two", Label: "Two"}
	stranger := ContextFabricSubjectRef{Kind: ContextFabricSubjectRepository, CanonicalID: "repo_x", Label: "X"}

	return ContextFabricInvestigationResult{
		SubjectResolution: ContextFabricSubjectResolution{
			Candidates: []ContextFabricSubjectCandidate{
				{ReceiptID: "receipt_a", Subject: memberA},
				{ReceiptID: "receipt_b", Subject: memberB},
			},
		},
		Cohort: &ContextFabricCohort{
			Members: []ContextFabricCohortMember{{Subject: memberA}, {Subject: memberB}},
			Groups: []ContextFabricCohortGroup{
				{Subject: groupOne, MemberCanonicalIDs: []string{"project_a"}},
				{Subject: groupTwo, MemberCanonicalIDs: []string{"project_b"}},
			},
		},
		Drivers: []ContextFabricDriverJudgment{
			{DriverID: "driver_member", AffectedSubjects: []ContextFabricSubjectRef{memberA}},
			{DriverID: "driver_group_one", AffectedSubjects: []ContextFabricSubjectRef{groupOne}},
			{DriverID: "driver_both_groups", AffectedSubjects: []ContextFabricSubjectRef{groupOne, groupTwo}},
			{DriverID: "driver_global", AffectedSubjects: []ContextFabricSubjectRef{stranger}},
		},
		// TWO remaining-work findings and ONE readiness gap, deliberately
		// unequal: with equal counts, swapping the two fields in the census
		// projection is invisible to every comparison here.
		RemainingWork: []ContextFabricFinding{
			{FindingID: "finding_group_two", Subjects: []ContextFabricSubjectRef{groupTwo}},
			{FindingID: "finding_group_two_more", Subjects: []ContextFabricSubjectRef{groupTwo}},
		},
		ReadinessGaps: []ContextFabricFinding{{FindingID: "finding_global"}},
		// THREE conflicts against ONE readiness gap and TWO remaining-work
		// findings: every finding collection carries a different count, so
		// swapping any pair of them in the census projection breaks a
		// comparison rather than cancelling out.
		Conflicts: []ContextFabricFinding{
			{FindingID: "finding_member_b", Subjects: []ContextFabricSubjectRef{memberB}},
			{FindingID: "finding_member_b_more", Subjects: []ContextFabricSubjectRef{memberB}},
			{FindingID: "finding_member_b_third", Subjects: []ContextFabricSubjectRef{memberB}},
		},
		ClaimedFacts:  []ContextFabricClaimedFact{{ClaimID: "claim_member_b", Subject: memberB}},
		// Paths are charged by Total() and NOT by Budgeted(), so they must
		// mint no debit. Present in the fixture so that exclusion is
		// exercised rather than assumed.
		Paths: []ContextFabricRelationshipPath{{PathID: "path_one"}, {PathID: "path_two"}},
	}
}

// TestEveryChargedOccurrenceHasExactlyOneDebit is the accounting check itself:
// a bijection between charged occurrences and debits, proved against the two
// derivations this package already publishes rather than against the ledger's
// own walk.
//
// Their independence is PARTIAL and saying so matters: the walk and
// AttributeContextFabricResultItems share contextFabricSubjectsBucket, and the
// per-collection comparison shares CountContextFabricResultItems. What this
// catches is a walk that stops agreeing with the summaries, or a ledger edited
// after the fact -- not a bug inside a shared helper.
func TestEveryChargedOccurrenceHasExactlyOneDebit(t *testing.T) {
	t.Parallel()
	result := ledgerGroupedFixture()
	ledger := ReconcileContextFabricResultItems(result)

	if !ledger.Reconciled() {
		t.Fatalf("a well-formed result did not reconcile: status=%q disagreement=%q", ledger.Status, ledger.Disagreement)
	}
	// The fixture's own claim FIRST: an empty walk trivially satisfies every
	// comparison below.
	if ledger.Total() == 0 {
		t.Fatal("the fixture produced no debits at all -- the fixture changed, not the invariant")
	}
	if ledger.Total() != ledger.Counts.Budgeted() {
		t.Fatalf("ledger charges %d debits but the census budgets %d", ledger.Total(), ledger.Counts.Budgeted())
	}

	perCollection := map[ContextFabricChargedCollection]int{}
	for _, debit := range ledger.Debits {
		if !ValidContextFabricChargedCollection(debit.Collection) {
			t.Fatalf("debit %+v names a collection outside the closed vocabulary", debit)
		}
		if !ValidContextFabricItemBucket(debit.Bucket) {
			t.Fatalf("debit %+v names a bucket outside the closed vocabulary", debit)
		}
		perCollection[debit.Collection]++
	}
	for collection, want := range contextFabricCensusByCollection(ledger.Counts) {
		if perCollection[collection] != want {
			t.Errorf("collection %q: %d debits, census says %d", collection, perCollection[collection], want)
		}
	}

	// The OCCURRENCE key, not just the count: every (collection, ordinal)
	// pair appears exactly once, and the ordinals of a collection are the
	// dense range 0..n-1. A walk that skipped an item and charged another
	// twice keeps the count and breaks this.
	seen := map[ContextFabricChargedCollection]map[int]int{}
	for _, debit := range ledger.Debits {
		if seen[debit.Collection] == nil {
			seen[debit.Collection] = map[int]int{}
		}
		seen[debit.Collection][debit.Ordinal]++
	}
	for collection, ordinals := range seen {
		for ordinal := 0; ordinal < perCollection[collection]; ordinal++ {
			if ordinals[ordinal] != 1 {
				t.Errorf("collection %q ordinal %d charged %d times, want exactly 1", collection, ordinal, ordinals[ordinal])
			}
		}
	}

	// And the bucket split matches the shipped attribution, so the ledger
	// and the published split cannot describe the same answer differently.
	got := ContextFabricItemAttribution{}
	for _, debit := range ledger.Debits {
		got.charge(debit.Bucket)
	}
	if got != ledger.Attribution {
		t.Errorf("ledger bucket split %+v disagrees with AttributeContextFabricResultItems %+v", got, ledger.Attribution)
	}
}

// TestPathsMintNoDebit pins the one deliberate exclusion. Paths are charged by
// Total() and excluded from Budgeted() (CHAOS-4523); a ledger that debited them
// would enforce a different quantity than the budget does.
func TestPathsMintNoDebit(t *testing.T) {
	t.Parallel()
	result := ledgerGroupedFixture()
	if len(result.Paths) == 0 {
		t.Fatal("the fixture carries no paths, so this exclusion is not exercised")
	}
	before := ReconcileContextFabricResultItems(result)

	result.Paths = append(result.Paths, ContextFabricRelationshipPath{PathID: "path_three"})
	after := ReconcileContextFabricResultItems(result)

	if after.Total() != before.Total() {
		t.Fatalf("adding a path moved the charged total from %d to %d", before.Total(), after.Total())
	}
	if !after.Reconciled() {
		t.Fatalf("adding a path broke reconciliation: status=%q", after.Status)
	}
	if after.Counts.Total() != before.Counts.Total()+1 {
		t.Fatalf("adding a path did not move Total(): %d then %d", before.Counts.Total(), after.Counts.Total())
	}
}

// TestEveryCensusFieldIsWalkedOrDeclaredExcluded is the guard that makes a
// FUTURE charged collection impossible to add on one side only.
//
// It walks ContextFabricResultItemCounts by reflection, sets one field at a
// time, and requires that exactly one charged collection moves -- or that the
// field is named in the excluded table with a reason. A new count field with
// neither is the "the census charges it and the ledger does not" defect, caught
// here rather than in production as an unreconcilable answer.
func TestEveryCensusFieldIsWalkedOrDeclaredExcluded(t *testing.T) {
	t.Parallel()
	const sentinel = 7

	counts := reflect.TypeOf(ContextFabricResultItemCounts{})
	if counts.NumField() == 0 {
		t.Fatal("ContextFabricResultItemCounts has no fields -- the reflection walk proves nothing")
	}
	walked := 0
	for index := 0; index < counts.NumField(); index++ {
		field := counts.Field(index)
		if reason, excluded := contextFabricCensusExcludedFields[field.Name]; excluded {
			if reason == "" {
				t.Errorf("census field %q is excluded with no reason recorded", field.Name)
			}
			// AND THE EXCLUSION MUST BE REAL, not merely declared. A future
			// change could charge a new quantity in Budgeted() and add its
			// field to this table, and the walk above would skip it while
			// the ledger never debits it -- an undercount that reconciles.
			// So an excluded field is required to move Total() and NOT
			// move Budgeted(), which is exactly what "excluded from the
			// item budget" means.
			probe := reflect.New(counts).Elem()
			probe.Field(index).SetInt(sentinel)
			value := probe.Interface().(ContextFabricResultItemCounts)
			if value.Budgeted() != 0 {
				t.Errorf("census field %q is declared excluded (%s) but moves Budgeted() to %d: the budget "+
					"charges it and the ledger will not debit it", field.Name, reason, value.Budgeted())
			}
			if value.Total() != sentinel {
				t.Errorf("census field %q is declared excluded but does not move Total() either (%d): it is "+
					"counted by nothing and the declaration is stale", field.Name, value.Total())
			}
			continue
		}
		if field.Type.Kind() != reflect.Int {
			t.Errorf("census field %q is not an int count; the projection cannot be checked", field.Name)
			continue
		}
		probe := reflect.New(counts).Elem()
		probe.Field(index).SetInt(sentinel)
		projection := contextFabricCensusByCollection(probe.Interface().(ContextFabricResultItemCounts))

		moved := []ContextFabricChargedCollection{}
		for collection, value := range projection {
			if value != 0 {
				moved = append(moved, collection)
			}
			if value != 0 && value != sentinel {
				t.Errorf("census field %q moved collection %q to %d, want %d", field.Name, collection, value, sentinel)
			}
		}
		switch len(moved) {
		case 1:
			walked++
		case 0:
			t.Errorf("census field %q is charged by the budget but reaches NO charged collection, and is not "+
				"declared in contextFabricCensusExcludedFields: the ledger would under-count every result carrying it", field.Name)
		default:
			t.Errorf("census field %q reaches %d charged collections %v, want exactly one", field.Name, len(moved), moved)
		}
	}
	if walked != ContextFabricChargedCollectionCount {
		t.Errorf("%d census fields map to charged collections, but the vocabulary declares %d members: "+
			"a vocabulary member with no census field can never be debited", walked, ContextFabricChargedCollectionCount)
	}
}

// TestEveryAttributionFieldIsReadableByItsBucket closes the default arm of
// contextFabricAttributionBucket: a bucket field no vocabulary member can read
// would compare zero against zero forever, and the bucket check would pass for
// exactly the bucket nobody had wired up.
func TestEveryAttributionFieldIsReadableByItsBucket(t *testing.T) {
	t.Parallel()
	const sentinel = 5

	attribution := reflect.TypeOf(ContextFabricItemAttribution{})
	if attribution.NumField() == 0 {
		t.Fatal("ContextFabricItemAttribution has no fields -- the reflection walk proves nothing")
	}
	if attribution.NumField() != ContextFabricItemBucketCount {
		t.Fatalf("the attribution carries %d fields and the bucket vocabulary %d members; one of them has "+
			"a quantity the other cannot name", attribution.NumField(), ContextFabricItemBucketCount)
	}
	for index := 0; index < attribution.NumField(); index++ {
		probe := reflect.New(attribution).Elem()
		probe.Field(index).SetInt(sentinel)
		value := probe.Interface().(ContextFabricItemAttribution)

		readers := []ContextFabricItemBucket{}
		for _, bucket := range ContextFabricItemBucketVocabulary() {
			if contextFabricAttributionBucket(value, bucket) == sentinel {
				readers = append(readers, bucket)
			}
		}
		if len(readers) != 1 {
			t.Errorf("attribution field %q is read by %d buckets %v, want exactly one",
				attribution.Field(index).Name, len(readers), readers)
		}
	}
}

// TestADroppedRowPairedWithADoubledDebitIsVisible is the defect a matching
// TOTAL cannot see, and the reason this ledger is occurrence-level.
//
// One cohort member row is dropped and one extra driver debit is added: the
// debit count is unchanged, the old identity still balances, and the
// per-collection comparison names cohort_members.
func TestADroppedRowPairedWithADoubledDebitIsVisible(t *testing.T) {
	t.Parallel()
	reconciled := ReconcileContextFabricResultItems(ledgerGroupedFixture())
	if !reconciled.Reconciled() {
		t.Fatalf("fixture did not reconcile to begin with: %q", reconciled.Status)
	}

	tampered := reconciled
	tampered.Debits = nil
	dropped := false
	for _, debit := range reconciled.Debits {
		if debit.Collection == ContextFabricChargedCohortMembers && !dropped {
			dropped = true
			continue
		}
		tampered.Debits = append(tampered.Debits, debit)
	}
	if !dropped {
		t.Fatal("the fixture carries no cohort member row to drop")
	}
	tampered.Debits = append(tampered.Debits, ContextFabricItemDebit{
		Collection: ContextFabricChargedDrivers,
		Ordinal:    len(reconciled.Debits),
		DeclaredID: "driver_phantom",
		Bucket:     ContextFabricItemBucketGlobal,
	})

	if tampered.Total() != reconciled.Total() {
		t.Fatalf("the tamper changed the total (%d then %d); it must not, or this test is not testing "+
			"what a total cannot see", reconciled.Total(), tampered.Total())
	}
	// The OCCURRENCE KEY reports it, and names which occurrence went missing.
	// The collection count is also wrong here, but the ordinal check runs
	// first and is the more precise diagnosis: it says WHICH item, where a
	// count says only how many.
	status, disagreement := contextFabricReconcile(tampered)
	if status != ContextFabricLedgerDuplicateOccurrence {
		t.Fatalf("status = %q, want %q", status, ContextFabricLedgerDuplicateOccurrence)
	}
	if disagreement != string(ContextFabricChargedCohortMembers)+"#0" {
		t.Errorf("disagreement = %q, want the missing cohort_members occurrence", disagreement)
	}
}

// TestARepeatedDeclaredIDStillCharesTwoItems is the FALSE POSITIVE this file
// briefly shipped, pinned so it cannot come back.
//
// An earlier revision refused an answer whose driver, finding or claim ids
// repeated, on the reasoning that two charged items then look like one to any
// id-keyed consumer. Executing it found a real engine fixture that assembles
// two claimed facts sharing one id, so every answer of that shape became a
// server error on an account that balanced perfectly. An accounting defect is a
// 500; a false one costs a served answer. A repeated id is a producer defect
// for validation to reject.
func TestARepeatedDeclaredIDStillChargesTwoItems(t *testing.T) {
	t.Parallel()
	member := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_a"}
	result := ContextFabricInvestigationResult{
		Cohort: &ContextFabricCohort{Members: []ContextFabricCohortMember{{Subject: member}}},
		ClaimedFacts: []ContextFabricClaimedFact{
			{ClaimID: "claim_repeated", Subject: member},
			{ClaimID: "claim_repeated", Subject: member},
		},
	}
	ledger := ReconcileContextFabricResultItems(result)

	if !ledger.Reconciled() {
		t.Fatalf("a repeated claim id was reported as an accounting defect (%q, %q): that is a false "+
			"positive, and it costs a served answer", ledger.Status, ledger.Disagreement)
	}
	// BOTH are charged: the repeat collapses nothing.
	claims := 0
	ordinals := map[int]int{}
	for _, debit := range ledger.Debits {
		if debit.Collection != ContextFabricChargedClaimedFacts {
			continue
		}
		claims++
		ordinals[debit.Ordinal]++
	}
	if claims != 2 {
		t.Fatalf("%d claim debits, want 2: a repeated id collapsed two charged items into one", claims)
	}
	if ordinals[0] != 1 || ordinals[1] != 1 {
		t.Errorf("the two claims do not carry distinct occurrence keys: %v", ordinals)
	}
}

// TestACohortCarryingOneMemberTwiceChargesTwoRows is the same property for the
// collection whose rows carry no id of their own: two rows are two occurrences
// and two debits, keyed by ordinal, whatever their subjects say.
func TestACohortCarryingOneMemberTwiceChargesTwoRows(t *testing.T) {
	t.Parallel()
	member := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_a"}
	result := ContextFabricInvestigationResult{
		Cohort: &ContextFabricCohort{Members: []ContextFabricCohortMember{{Subject: member}, {Subject: member}}},
	}
	ledger := ReconcileContextFabricResultItems(result)

	if !ledger.Reconciled() {
		t.Fatalf("status = %q disagreement = %q, want a reconciled account", ledger.Status, ledger.Disagreement)
	}
	if ledger.Total() != 2 {
		t.Fatalf("total = %d, want 2", ledger.Total())
	}
}

// TestADroppedCandidatePairedWithADuplicateIsVisible is the review's own
// repro: the shape that survives a matching collection count, a matching
// bucket split AND a matching total, and is visible only because the
// OCCURRENCE KEY is checked rather than merely documented.
func TestADroppedCandidatePairedWithADuplicateIsVisible(t *testing.T) {
	t.Parallel()
	original := ReconcileContextFabricResultItems(ledgerGroupedFixture())
	if !original.Reconciled() {
		t.Fatalf("fixture did not reconcile: %q", original.Status)
	}

	tampered := original
	tampered.Debits = nil
	var duplicate ContextFabricItemDebit
	for _, debit := range original.Debits {
		if debit.Collection == ContextFabricChargedCandidates && debit.Ordinal == 0 {
			continue
		}
		tampered.Debits = append(tampered.Debits, debit)
		if debit.Collection == ContextFabricChargedCandidates && debit.Ordinal == 1 {
			duplicate = debit
		}
	}
	if duplicate.DeclaredID == "" {
		t.Fatal("the fixture carries no second candidate to duplicate")
	}
	tampered.Debits = append(tampered.Debits, duplicate)

	if tampered.Total() != original.Total() {
		t.Fatalf("the tamper moved the total (%d then %d); it must not, or this is not the case a "+
			"total cannot see", original.Total(), tampered.Total())
	}
	status, disagreement := contextFabricReconcile(tampered)
	if status == ContextFabricLedgerReconciled {
		t.Fatal("a dropped candidate paired with a duplicated one reconciled")
	}
	if disagreement == "" {
		t.Error("the disagreement was reported without naming what disagreed")
	}
}

// TestSparseOrdinalsAreAnAccountingDefect covers the other half of the
// occurrence key: uniqueness alone admits {0, 2} for a two-item collection,
// which is one occurrence dropped and another invented.
func TestSparseOrdinalsAreAnAccountingDefect(t *testing.T) {
	t.Parallel()
	original := ReconcileContextFabricResultItems(ledgerGroupedFixture())
	tampered := original
	tampered.Debits = append([]ContextFabricItemDebit{}, original.Debits...)
	moved := false
	for index := range tampered.Debits {
		if tampered.Debits[index].Collection == ContextFabricChargedCandidates && tampered.Debits[index].Ordinal == 0 {
			tampered.Debits[index].Ordinal = 7
			moved = true
			break
		}
	}
	if !moved {
		t.Fatal("no candidate debit to move")
	}
	if status, _ := contextFabricReconcile(tampered); status != ContextFabricLedgerDuplicateOccurrence {
		t.Fatalf("status = %q, want %q for a sparse ordinal range", status, ContextFabricLedgerDuplicateOccurrence)
	}
}

// TestACertificateIsNotIssuedForALedgerEditedAfterReconciliation is the
// review's second repro. Status and Debits are both exported, so a caller can
// hold a real ledger, drop a debit and leave the status saying `reconciled`.
// A certificate that trusted that field would be the forgeable thing the type
// exists not to be.
func TestACertificateIsNotIssuedForALedgerEditedAfterReconciliation(t *testing.T) {
	t.Parallel()
	ledger := ReconcileContextFabricResultItems(ledgerGroupedFixture())
	if !ledger.Reconciled() {
		t.Fatalf("fixture did not reconcile: %q", ledger.Status)
	}
	total := ledger.Total()

	ledger.Debits = ledger.Debits[:total-1]
	if ledger.Status != ContextFabricLedgerReconciled {
		t.Fatal("the tamper changed the status field; the point of this test is that it does NOT")
	}
	certificate, verdict := CertifyContextFabricCapacity(ledger, ContextFabricResponseBudget{MaxItems: total - 1})
	if certificate.Certified() || verdict != ContextFabricCapacityAccountingDefect {
		t.Fatalf("a ledger edited after reconciliation was certified: verdict %q, certified %v",
			verdict, certificate.Certified())
	}
}

// TestAnItemNamingOneGroupTwiceParticipatesOnce pins the DISTINCT half of
// group incidence: participation is per named group, not per mention, exactly
// as the bucket precedence refuses to promote such an item to multi_group.
func TestAnItemNamingOneGroupTwiceParticipatesOnce(t *testing.T) {
	t.Parallel()
	group := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_one"}
	result := ContextFabricInvestigationResult{
		Cohort: &ContextFabricCohort{Groups: []ContextFabricCohortGroup{{Subject: group}}},
		Drivers: []ContextFabricDriverJudgment{{
			DriverID:         "driver_repeats_its_group",
			AffectedSubjects: []ContextFabricSubjectRef{group, group},
		}},
	}
	ledger := ReconcileContextFabricResultItems(result)

	if !ledger.Reconciled() {
		t.Fatalf("status = %q", ledger.Status)
	}
	if got := ledger.GroupIncidenceCounts()[contextFabricSubjectBucketKey(group)]; got != 1 {
		t.Fatalf("incidence = %d, want 1: a group named twice by one item is participated in once", got)
	}
	if len(ledger.Debits) != 1 || len(ledger.Debits[0].GroupIncidence) != 1 {
		t.Fatalf("debit carries %d incidences, want 1: %+v", len(ledger.Debits[0].GroupIncidence), ledger.Debits[0])
	}
	if ledger.Debits[0].Bucket != ContextFabricItemBucketGroup {
		t.Errorf("bucket = %q, want %q: a duplicate mention must not promote the item to multi_group",
			ledger.Debits[0].Bucket, ContextFabricItemBucketGroup)
	}
}

// TestCandidateDebitsAreKeyedOnTheirReceipt pins WHICH id a candidate debit
// declares. Two candidates can legitimately name the same subject; their
// receipts are what the validator requires to be distinct.
func TestCandidateDebitsAreKeyedOnTheirReceipt(t *testing.T) {
	t.Parallel()
	result := ledgerGroupedFixture()
	ledger := ReconcileContextFabricResultItems(result)

	found := 0
	for _, debit := range ledger.Debits {
		if debit.Collection != ContextFabricChargedCandidates {
			continue
		}
		want := result.SubjectResolution.Candidates[debit.Ordinal].ReceiptID
		if want == "" {
			t.Fatal("the fixture's candidates carry no receipt, so this assertion tests nothing")
		}
		if debit.DeclaredID != want {
			t.Errorf("candidate %d declares %q, want its receipt %q", debit.Ordinal, debit.DeclaredID, want)
		}
		found++
	}
	if found == 0 {
		t.Fatal("no candidate debits were examined")
	}
}

// TestACensusInflatedBeyondTheWalkIsAVisibleDisagreement is the runtime half
// of the reflection guard's honest limit: a new charged quantity FOLDED INTO an
// existing census field leaves the reflected shape unchanged, so the test-time
// guard cannot see it -- but the per-collection comparison can, and does.
func TestACensusInflatedBeyondTheWalkIsAVisibleDisagreement(t *testing.T) {
	t.Parallel()
	ledger := ReconcileContextFabricResultItems(ledgerGroupedFixture())
	if !ledger.Reconciled() {
		t.Fatalf("fixture did not reconcile: %q", ledger.Status)
	}
	// Exactly what "charge limitations to the candidates field" would look
	// like to this function: the census counts more than the walk debited.
	ledger.Counts.Candidates += 2
	status, disagreement := contextFabricReconcile(ledger)
	if status != ContextFabricLedgerCollectionDisagreement {
		t.Fatalf("status = %q, want %q", status, ContextFabricLedgerCollectionDisagreement)
	}
	if disagreement != string(ContextFabricChargedCandidates) {
		t.Errorf("disagreement = %q, want %q", disagreement, ContextFabricChargedCandidates)
	}
}

// TestGroupIncidenceIsNeverAPhysicalDebit is round 1's multi-group false zero,
// stated as the two units it confused: eighteen drivers naming BOTH of two
// groups cost eighteen items globally and eighteen incidences to EACH group.
//
// The defect it pins reported zero groups over quota because it compared
// `Group + MultiGroup` against an AGGREGATE capacity, which is shared-pool
// arithmetic under a declared every_group rule.
func TestGroupIncidenceIsNeverAPhysicalDebit(t *testing.T) {
	t.Parallel()
	groupOne := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_one"}
	groupTwo := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_two"}
	member := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_a"}

	result := ContextFabricInvestigationResult{
		Cohort: &ContextFabricCohort{
			Members: []ContextFabricCohortMember{{Subject: member}},
			Groups: []ContextFabricCohortGroup{
				{Subject: groupOne, MemberCanonicalIDs: []string{"project_a"}},
				{Subject: groupTwo},
			},
		},
	}
	for index := 0; index < 18; index++ {
		result.Drivers = append(result.Drivers, ContextFabricDriverJudgment{
			DriverID:         "driver_" + string(rune('a'+index)),
			AffectedSubjects: []ContextFabricSubjectRef{groupOne, groupTwo},
		})
	}
	ledger := ReconcileContextFabricResultItems(result)

	if !ledger.Reconciled() {
		t.Fatalf("status = %q disagreement = %q", ledger.Status, ledger.Disagreement)
	}
	// PHYSICAL: one member row + eighteen drivers.
	if ledger.Total() != 19 {
		t.Fatalf("physical total = %d, want 19 (1 member row + 18 drivers)", ledger.Total())
	}
	incidence := ledger.GroupIncidenceCounts()
	for _, group := range []ContextFabricSubjectRef{groupOne, groupTwo} {
		key := contextFabricSubjectBucketKey(group)
		if incidence[key] != 18 {
			t.Errorf("group %q incidence = %d, want 18: every_group charges a multi-group item to each "+
				"group it names", group.CanonicalID, incidence[key])
		}
	}
	// And the two units are NOT interchangeable: summing incidence gives 36,
	// which is not what the answer cost.
	sum := 0
	for _, used := range incidence {
		sum += used
	}
	if sum == ledger.Total() {
		t.Fatal("incidence sums to the physical total, so this fixture cannot tell the two units apart")
	}
}

// TestSharedPermissionIsChargedOncePhysically is round 3's mirror-image
// finding: three drivers naming both groups cost THREE items, not six, and
// contribute three incidences to each group. The allowance they are compared
// against is the allocator's business (a separate seam); what this pins is that
// the ledger never inflates physical cost by group participation.
func TestSharedPermissionIsChargedOncePhysically(t *testing.T) {
	t.Parallel()
	groupOne := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_one"}
	groupTwo := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_two"}

	result := ContextFabricInvestigationResult{
		Cohort: &ContextFabricCohort{Groups: []ContextFabricCohortGroup{{Subject: groupOne}, {Subject: groupTwo}}},
		Drivers: []ContextFabricDriverJudgment{
			{DriverID: "shared_one", AffectedSubjects: []ContextFabricSubjectRef{groupOne, groupTwo}},
			{DriverID: "shared_two", AffectedSubjects: []ContextFabricSubjectRef{groupTwo, groupOne}},
			{DriverID: "shared_three", AffectedSubjects: []ContextFabricSubjectRef{groupOne, groupTwo}},
		},
	}
	ledger := ReconcileContextFabricResultItems(result)

	if !ledger.Reconciled() {
		t.Fatalf("status = %q disagreement = %q", ledger.Status, ledger.Disagreement)
	}
	if ledger.Total() != 3 {
		t.Fatalf("physical total = %d, want 3", ledger.Total())
	}
	if ledger.Attribution.MultiGroup != 3 {
		t.Fatalf("multi_group attribution = %d, want 3", ledger.Attribution.MultiGroup)
	}
	incidence := ledger.GroupIncidenceCounts()
	for _, group := range []ContextFabricSubjectRef{groupOne, groupTwo} {
		key := contextFabricSubjectBucketKey(group)
		if incidence[key] != 3 {
			t.Errorf("group %q incidence = %d, want 3", group.CanonicalID, incidence[key])
		}
	}
}

// TestADeclaredGroupNamedByNothingIsAMeasuredZero: a group with an allowance
// and no usage must read as zero, never as an absent key. The whole class-B
// history of this seam is a zero that meant "never computed" being read as a
// zero that meant "measured none".
func TestADeclaredGroupNamedByNothingIsAMeasuredZero(t *testing.T) {
	t.Parallel()
	quiet := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_quiet"}
	loud := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_loud"}
	result := ContextFabricInvestigationResult{
		Cohort:  &ContextFabricCohort{Groups: []ContextFabricCohortGroup{{Subject: quiet}, {Subject: loud}}},
		Drivers: []ContextFabricDriverJudgment{{DriverID: "driver_loud", AffectedSubjects: []ContextFabricSubjectRef{loud}}},
	}
	incidence := ReconcileContextFabricResultItems(result).GroupIncidenceCounts()

	used, present := incidence[contextFabricSubjectBucketKey(quiet)]
	if !present {
		t.Fatal("a declared group nothing named is absent from the incidence map; a caller cannot tell " +
			"'measured none' from 'never measured'")
	}
	if used != 0 {
		t.Errorf("quiet group incidence = %d, want 0", used)
	}
	if incidence[contextFabricSubjectBucketKey(loud)] != 1 {
		t.Errorf("loud group incidence = %d, want 1", incidence[contextFabricSubjectBucketKey(loud)])
	}
}

// TestOnlyAReconciledLedgerInsideItsCeilingIsCertified is the capacity check.
// The four verdicts are four different operator problems and a bool reports
// three of them identically.
func TestOnlyAReconciledLedgerInsideItsCeilingIsCertified(t *testing.T) {
	t.Parallel()
	ledger := ReconcileContextFabricResultItems(ledgerGroupedFixture())
	if !ledger.Reconciled() {
		t.Fatalf("fixture did not reconcile: %q", ledger.Status)
	}
	spent := ledger.Total()

	// GENUINELY unreconciled, not merely labelled so: the certificate
	// re-derives the account from the debits it is handed, so a ledger whose
	// status field was edited while its debits still reconcile is -- correctly
	// -- still certifiable. Dropping a debit is what an unreconciled ledger
	// actually is.
	broken := ledger
	broken.Debits = append([]ContextFabricItemDebit{}, ledger.Debits[:spent-1]...)

	for _, testCase := range []struct {
		name      string
		ledger    ContextFabricItemLedger
		budget    ContextFabricResponseBudget
		want      ContextFabricCapacityVerdict
		certified bool
	}{
		{"inside the ceiling", ledger, ContextFabricResponseBudget{MaxItems: spent}, ContextFabricCapacityCertifiedFit, true},
		{"one over the ceiling", ledger, ContextFabricResponseBudget{MaxItems: spent - 1}, ContextFabricCapacityOverdraw, false},
		{"no ceiling in force", ledger, ContextFabricResponseBudget{}, ContextFabricCapacityUnbounded, false},
		{"accounting did not reconcile", broken, ContextFabricResponseBudget{MaxItems: spent}, ContextFabricCapacityAccountingDefect, false},
		{"unreconciled and unbounded", broken, ContextFabricResponseBudget{}, ContextFabricCapacityAccountingDefect, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			certificate, verdict := CertifyContextFabricCapacity(testCase.ledger, testCase.budget)
			if verdict != testCase.want {
				t.Fatalf("verdict = %q, want %q", verdict, testCase.want)
			}
			if certificate.Certified() != testCase.certified {
				t.Fatalf("Certified() = %v, want %v", certificate.Certified(), testCase.certified)
			}
			if testCase.certified {
				if certificate.Items() != spent || certificate.MaxItems() != testCase.budget.MaxItems {
					t.Errorf("certificate = {items %d, max %d}, want {items %d, max %d}",
						certificate.Items(), certificate.MaxItems(), spent, testCase.budget.MaxItems)
				}
			}
		})
	}

	// The zero value certifies nothing: that is the property the unexported
	// fields buy, and a caller holding a forged certificate is exactly what
	// this type exists to prevent.
	if (ContextFabricCertifiedFit{}).Certified() {
		t.Fatal("the zero ContextFabricCertifiedFit reports itself as certified")
	}
}

// TestTheLedgerVocabulariesAreClosedAndRejectTheEmptyValue keeps a token from
// shipping unnamed or unreachable, the same enumeration discipline every other
// closed vocabulary in this package carries.
func TestTheLedgerVocabulariesAreClosedAndRejectTheEmptyValue(t *testing.T) {
	t.Parallel()
	collections := ContextFabricChargedCollectionVocabulary()
	if len(collections) != ContextFabricChargedCollectionCount {
		t.Fatalf("charged-collection vocabulary has %d members, count says %d", len(collections), ContextFabricChargedCollectionCount)
	}
	seen := map[ContextFabricChargedCollection]struct{}{}
	for _, member := range collections {
		if member == "" {
			t.Error("the charged-collection vocabulary carries an empty member")
		}
		if _, duplicate := seen[member]; duplicate {
			t.Errorf("charged collection %q appears twice", member)
		}
		seen[member] = struct{}{}
		if !ValidContextFabricChargedCollection(member) {
			t.Errorf("published member %q is not valid", member)
		}
	}
	for _, notAMember := range []ContextFabricChargedCollection{"", "paths", "limitations"} {
		if ValidContextFabricChargedCollection(notAMember) {
			t.Errorf("%q is not a charged collection but validated as one", notAMember)
		}
	}

	statuses := ContextFabricLedgerStatusVocabulary()
	if len(statuses) != ContextFabricLedgerStatusCount {
		t.Fatalf("ledger-status vocabulary has %d members, count says %d", len(statuses), ContextFabricLedgerStatusCount)
	}
	for _, member := range statuses {
		if !ValidContextFabricLedgerStatus(member) {
			t.Errorf("published status %q is not valid", member)
		}
	}
	// EVERY NAMED CONSTANT MUST BE PUBLISHED, listed here by hand. The size
	// check above is derived from the array itself, so deleting a member
	// from the array shrinks both sides and passes -- a constant would go on
	// existing, reachable from the reconciler, and absent from the
	// vocabulary a consumer enumerates.
	for _, named := range []ContextFabricLedgerStatus{
		ContextFabricLedgerReconciled,
		ContextFabricLedgerCollectionDisagreement,
		ContextFabricLedgerBucketDisagreement,
		ContextFabricLedgerDuplicateOccurrence,
		ContextFabricLedgerInvalidDebit,
	} {
		if !ValidContextFabricLedgerStatus(named) {
			t.Errorf("status constant %q is named in the package but not published in the vocabulary", named)
		}
	}
	if ValidContextFabricLedgerStatus("") {
		t.Error("the empty ledger status validates, so an unset status could read as reconciled")
	}

	for _, named := range []ContextFabricChargedCollection{
		ContextFabricChargedCandidates,
		ContextFabricChargedCohortMembers,
		ContextFabricChargedDrivers,
		ContextFabricChargedRemainingWork,
		ContextFabricChargedReadinessGaps,
		ContextFabricChargedConflicts,
		ContextFabricChargedClaimedFacts,
	} {
		if !ValidContextFabricChargedCollection(named) {
			t.Errorf("collection constant %q is named but not published", named)
		}
	}

	verdicts := ContextFabricCapacityVerdictVocabulary()
	if len(verdicts) != ContextFabricCapacityVerdictCount {
		t.Fatalf("capacity-verdict vocabulary has %d members, count says %d", len(verdicts), ContextFabricCapacityVerdictCount)
	}
	for _, member := range verdicts {
		if !ValidContextFabricCapacityVerdict(member) {
			t.Errorf("published verdict %q is not valid", member)
		}
	}
	for _, named := range []ContextFabricCapacityVerdict{
		ContextFabricCapacityCertifiedFit,
		ContextFabricCapacityOverdraw,
		ContextFabricCapacityUnbounded,
		ContextFabricCapacityAccountingDefect,
	} {
		if !ValidContextFabricCapacityVerdict(named) {
			t.Errorf("verdict constant %q is named but not published", named)
		}
	}
	if ValidContextFabricCapacityVerdict("") {
		t.Error("the empty capacity verdict validates")
	}
}

// TestTheLedgerAccountsForTheDocumentInHand pins the input contract: the
// reconciler reads the RESULT, so a result that is changed after it was
// measured produces a different ledger. Everything this seam has failed at came
// from checking a document other than the one that gets served.
func TestTheLedgerAccountsForTheDocumentInHand(t *testing.T) {
	t.Parallel()
	result := ledgerGroupedFixture()
	before := ReconcileContextFabricResultItems(result)

	result.Drivers = append(result.Drivers, ContextFabricDriverJudgment{DriverID: "driver_late_composer"})
	after := ReconcileContextFabricResultItems(result)

	if after.Total() != before.Total()+1 {
		t.Fatalf("a late writer added an item and the ledger moved from %d to %d, want %d",
			before.Total(), after.Total(), before.Total()+1)
	}
	if !after.Reconciled() {
		t.Fatalf("the re-measured document did not reconcile: %q", after.Status)
	}
}

// TestAForgedDebitCannotBeCertified is round 2's second finding. The ledger's
// fields are exported, so a hand-built debit naming a collection nothing knows
// about used to contribute to the total, escape the per-collection comparison
// (which walks only KNOWN collections) and collect a capacity certificate.
func TestAForgedDebitCannotBeCertified(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		debit ContextFabricItemDebit
		want  string
	}{
		{
			name:  "a collection outside the vocabulary",
			debit: ContextFabricItemDebit{Collection: ContextFabricChargedCollection("paths"), Bucket: ContextFabricItemBucketGlobal},
			want:  "collection:paths",
		},
		{
			name:  "a bucket outside the vocabulary",
			debit: ContextFabricItemDebit{Collection: ContextFabricChargedCandidates, Bucket: ContextFabricItemBucket("not-a-bucket")},
			want:  "bucket:not-a-bucket",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			forged := ContextFabricItemLedger{
				Debits:      []ContextFabricItemDebit{testCase.debit},
				Attribution: ContextFabricItemAttribution{Global: 1},
			}
			status, disagreement := contextFabricReconcile(forged)
			if status != ContextFabricLedgerInvalidDebit {
				t.Fatalf("status = %q, want %q", status, ContextFabricLedgerInvalidDebit)
			}
			if disagreement != testCase.want {
				t.Errorf("disagreement = %q, want %q", disagreement, testCase.want)
			}
			certificate, verdict := CertifyContextFabricCapacity(forged, ContextFabricResponseBudget{MaxItems: 1})
			if certificate.Certified() || verdict != ContextFabricCapacityAccountingDefect {
				t.Fatalf("a forged debit was certified: verdict %q certified %v", verdict, certificate.Certified())
			}
		})
	}
}

// TestTwoOccurrencesCarryingOneSourceItemAreOBSERVED is round 2's first
// finding, and the shape of the answer is the point.
//
// The occurrence key proves one debit per position; it cannot prove that
// position 1 holds a different source item from position 0. A producer that
// copies one item into two slots keeps the counts, the buckets, the total AND
// the ordinal range intact. Round 1 asked for an id check, round 2 showed what
// the key cannot see, and the fixture in between showed what enforcing it costs
// -- a real answer turned into a 500. So identity is REPORTED and the account
// stays reconciled.
func TestTwoOccurrencesCarryingOneSourceItemAreObserved(t *testing.T) {
	t.Parallel()
	member := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_a"}
	result := ContextFabricInvestigationResult{
		Cohort: &ContextFabricCohort{Members: []ContextFabricCohortMember{{Subject: member}}},
		ClaimedFacts: []ContextFabricClaimedFact{
			{ClaimID: "claim_copied", Subject: member},
			{ClaimID: "claim_copied", Subject: member},
		},
	}
	ledger := ReconcileContextFabricResultItems(result)

	// RECONCILED: this is not an accounting defect and must never become
	// one again.
	if !ledger.Reconciled() {
		t.Fatalf("status = %q disagreement = %q: a repeated id is a producer defect, not an accounting "+
			"one, and refusing it costs a served answer", ledger.Status, ledger.Disagreement)
	}
	// OBSERVED: and the signal is not lost.
	want := string(ContextFabricChargedClaimedFacts) + ":claim_copied"
	if len(ledger.RepeatedDeclaredIDs) != 1 || ledger.RepeatedDeclaredIDs[0] != want {
		t.Fatalf("RepeatedDeclaredIDs = %v, want exactly [%q]", ledger.RepeatedDeclaredIDs, want)
	}
	// The clean control: distinct ids report nothing, so the field is not
	// simply always populated.
	clean := result
	clean.ClaimedFacts = []ContextFabricClaimedFact{
		{ClaimID: "claim_one", Subject: member},
		{ClaimID: "claim_two", Subject: member},
	}
	if repeats := ReconcileContextFabricResultItems(clean).RepeatedDeclaredIDs; len(repeats) != 0 {
		t.Errorf("a result with distinct ids reported repeats %v", repeats)
	}
}

// TestEveryChargedItemCarriesItsDeclaredID pins the diagnosis field on the
// collections whose items declare one. Without it the id could be dropped
// wholesale and only the candidate assertion would notice.
func TestEveryChargedItemCarriesItsDeclaredID(t *testing.T) {
	t.Parallel()
	result := ledgerGroupedFixture()
	ledger := ReconcileContextFabricResultItems(result)

	want := map[ContextFabricChargedCollection][]string{
		ContextFabricChargedDrivers:       {"driver_member", "driver_group_one", "driver_both_groups", "driver_global"},
		ContextFabricChargedRemainingWork: {"finding_group_two", "finding_group_two_more"},
		ContextFabricChargedReadinessGaps: {"finding_global"},
		ContextFabricChargedConflicts:     {"finding_member_b", "finding_member_b_more", "finding_member_b_third"},
		ContextFabricChargedClaimedFacts:  {"claim_member_b"},
	}
	got := map[ContextFabricChargedCollection][]string{}
	for _, debit := range ledger.Debits {
		if _, tracked := want[debit.Collection]; !tracked {
			continue
		}
		got[debit.Collection] = append(got[debit.Collection], debit.DeclaredID)
	}
	for collection, ids := range want {
		if len(got[collection]) != len(ids) {
			t.Fatalf("collection %q: %d debits, fixture declares %d", collection, len(got[collection]), len(ids))
		}
		for index, id := range ids {
			if got[collection][index] != id {
				t.Errorf("collection %q ordinal %d declares %q, want %q", collection, index, got[collection][index], id)
			}
		}
	}
}

// TestTheBucketSplitIsComparedAgainstTheAttributionItCarries mutates the
// attribution ALONE, which is the only way to see that the per-bucket
// comparison is a real comparison rather than a walk agreeing with itself.
func TestTheBucketSplitIsComparedAgainstTheAttributionItCarries(t *testing.T) {
	t.Parallel()
	ledger := ReconcileContextFabricResultItems(ledgerGroupedFixture())
	if !ledger.Reconciled() {
		t.Fatalf("fixture did not reconcile: %q", ledger.Status)
	}
	if ledger.Attribution.Member == 0 || ledger.Attribution.Global == 0 {
		t.Fatal("the fixture must reach both buckets for this redistribution to be visible")
	}

	// A REDISTRIBUTION, not a change in total: this is the mutation a
	// sum-only check cannot see.
	tampered := ledger
	tampered.Attribution.Global += tampered.Attribution.Member
	tampered.Attribution.Member = 0

	status, disagreement := contextFabricReconcile(tampered)
	if status != ContextFabricLedgerBucketDisagreement {
		t.Fatalf("status = %q, want %q", status, ContextFabricLedgerBucketDisagreement)
	}
	if disagreement == "" {
		t.Error("the bucket disagreement named no bucket")
	}
}

// TestAnUnreconciledLedgerCannotBeCertified is round 3's first finding: a ZERO
// ledger is perfectly self-consistent -- no debits, no counts, no attribution,
// everything agreeing at zero -- and is bound to no answer at all. A
// certificate that only re-derived consistency handed it a certified fit for
// any positive ceiling.
func TestAnUnreconciledLedgerCannotBeCertified(t *testing.T) {
	t.Parallel()
	zero := ContextFabricItemLedger{}
	if zero.Reconciled() {
		t.Fatal("the zero ledger reports itself reconciled")
	}
	certificate, verdict := CertifyContextFabricCapacity(zero, ContextFabricResponseBudget{MaxItems: 1})
	if certificate.Certified() || verdict != ContextFabricCapacityAccountingDefect {
		t.Fatalf("a zero ledger was certified: verdict %q certified %v", verdict, certificate.Certified())
	}

	// A hand-built, internally coherent ledger that never saw a result is
	// refused for the same reason: the certificate is a statement about a
	// DOCUMENT, not about a struct.
	handBuilt := ContextFabricItemLedger{
		Debits:      []ContextFabricItemDebit{{Collection: ContextFabricChargedCandidates, Ordinal: 0, Bucket: ContextFabricItemBucketGlobal}},
		Counts:      ContextFabricResultItemCounts{Candidates: 1},
		Attribution: ContextFabricItemAttribution{Global: 1},
	}
	if status, _ := contextFabricReconcile(handBuilt); status != ContextFabricLedgerReconciled {
		t.Fatalf("the hand-built ledger is not self-consistent (%q), so this case tests the wrong thing", status)
	}
	if certificate, verdict := CertifyContextFabricCapacity(handBuilt, ContextFabricResponseBudget{MaxItems: 1}); certificate.Certified() || verdict != ContextFabricCapacityAccountingDefect {
		t.Fatalf("a self-consistent but result-unbound ledger was certified: verdict %q", verdict)
	}

	// The positive control: a ledger the reconciler produced, over the same
	// shape, IS certifiable -- so the guard above is provenance and not a
	// blanket refusal.
	produced := ReconcileContextFabricResultItems(ledgerGroupedFixture())
	if certificate, verdict := CertifyContextFabricCapacity(produced, ContextFabricResponseBudget{MaxItems: produced.Total()}); !certificate.Certified() || verdict != ContextFabricCapacityCertifiedFit {
		t.Fatalf("a genuinely reconciled ledger was refused: verdict %q", verdict)
	}
}

// TestReconciledReportsTheStatusItCarries pins the accessor itself. Nothing
// else calls it on a damaged ledger, so `return true` would survive every other
// test in this file.
func TestReconciledReportsTheStatusItCarries(t *testing.T) {
	t.Parallel()
	for _, status := range ContextFabricLedgerStatusVocabulary() {
		ledger := ContextFabricItemLedger{Status: status}
		want := status == ContextFabricLedgerReconciled
		if ledger.Reconciled() != want {
			t.Errorf("Reconciled() = %v for status %q, want %v", ledger.Reconciled(), status, want)
		}
	}
	if (ContextFabricItemLedger{}).Reconciled() {
		t.Error("the empty status reports reconciled")
	}
}

// TestTheDuplicateDiagnosisIsDeterministic is round 3's third finding: the
// density walk iterated a MAP, so a result with two defective collections named
// whichever one Go reached first -- different telemetry, same input, run to run.
func TestTheDuplicateDiagnosisIsDeterministic(t *testing.T) {
	t.Parallel()
	ledger := ContextFabricItemLedger{
		Debits: []ContextFabricItemDebit{
			{Collection: ContextFabricChargedCandidates, Ordinal: 1, Bucket: ContextFabricItemBucketGlobal},
			{Collection: ContextFabricChargedDrivers, Ordinal: 1, Bucket: ContextFabricItemBucketGlobal},
		},
		Counts:      ContextFabricResultItemCounts{Candidates: 1, Drivers: 1},
		Attribution: ContextFabricItemAttribution{Global: 2},
	}
	// Candidates come first in the published vocabulary, so the diagnosis is
	// candidates#0 on every run.
	want := string(ContextFabricChargedCandidates) + "#0"
	for attempt := 0; attempt < 200; attempt++ {
		status, disagreement := contextFabricReconcile(ledger)
		if status != ContextFabricLedgerDuplicateOccurrence {
			t.Fatalf("attempt %d: status = %q", attempt, status)
		}
		if disagreement != want {
			t.Fatalf("attempt %d: disagreement = %q, want %q -- the diagnosis depends on map order",
				attempt, disagreement, want)
		}
	}
}

// TestRepeatedIDsAreOrderedAndAbsentWhenThereAreNone pins the two properties a
// single-repeat fixture cannot: the ordering, and nil rather than an empty
// slice when nothing repeats.
func TestRepeatedIDsAreOrderedAndAbsentWhenThereAreNone(t *testing.T) {
	t.Parallel()
	member := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_a"}
	result := ContextFabricInvestigationResult{
		ClaimedFacts: []ContextFabricClaimedFact{
			{ClaimID: "claim_zulu", Subject: member},
			{ClaimID: "claim_alpha", Subject: member},
			{ClaimID: "claim_zulu", Subject: member},
			{ClaimID: "claim_alpha", Subject: member},
		},
	}
	repeats := ReconcileContextFabricResultItems(result).RepeatedDeclaredIDs
	want := []string{
		string(ContextFabricChargedClaimedFacts) + ":claim_alpha",
		string(ContextFabricChargedClaimedFacts) + ":claim_zulu",
	}
	if len(repeats) != len(want) {
		t.Fatalf("RepeatedDeclaredIDs = %v, want %v", repeats, want)
	}
	for index, id := range want {
		if repeats[index] != id {
			t.Fatalf("RepeatedDeclaredIDs = %v, want %v (sorted, so the field is stable across runs)", repeats, want)
		}
	}

	// NIL, not an empty slice: an absent observation and an observation that
	// found nothing serialize differently, and the field is omitempty.
	clean := ReconcileContextFabricResultItems(ledgerGroupedFixture())
	if clean.RepeatedDeclaredIDs != nil {
		t.Errorf("RepeatedDeclaredIDs = %#v on a clean result, want nil", clean.RepeatedDeclaredIDs)
	}
}

// TestGroupIncidenceNamesOnlyGroups covers the confirmation pass's first named
// survivor: without the non-group skip, a driver naming a member alongside a
// group would list the member as a participating "group".
func TestGroupIncidenceNamesOnlyGroups(t *testing.T) {
	t.Parallel()
	member := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_a"}
	group := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_one"}
	stranger := ContextFabricSubjectRef{Kind: ContextFabricSubjectRepository, CanonicalID: "repo_x"}
	result := ContextFabricInvestigationResult{
		Cohort: &ContextFabricCohort{
			Members: []ContextFabricCohortMember{{Subject: member}},
			Groups:  []ContextFabricCohortGroup{{Subject: group, MemberCanonicalIDs: []string{"project_a"}}},
		},
		Drivers: []ContextFabricDriverJudgment{{
			DriverID:         "driver_names_all_three",
			AffectedSubjects: []ContextFabricSubjectRef{member, group, stranger},
		}},
	}
	ledger := ReconcileContextFabricResultItems(result)
	if !ledger.Reconciled() {
		t.Fatalf("status = %q", ledger.Status)
	}

	incidence := ledger.GroupIncidenceCounts()
	if len(incidence) != 1 {
		t.Fatalf("incidence names %d subjects %v, want exactly the one declared group", len(incidence), incidence)
	}
	if incidence[contextFabricSubjectBucketKey(group)] != 1 {
		t.Errorf("the declared group has incidence %d, want 1", incidence[contextFabricSubjectBucketKey(group)])
	}
	for _, notAGroup := range []ContextFabricSubjectRef{member, stranger} {
		if _, present := incidence[contextFabricSubjectBucketKey(notAGroup)]; present {
			t.Errorf("%q is not a declared group but appears in the incidence map", notAGroup.CanonicalID)
		}
	}
}

// TestAnItemWithNoDeclaredIDIsNeverReportedAsARepeat covers the second named
// survivor. Candidates carry no receipt before validation, and several of them
// share the empty string; without the skip they would be reported as repeating
// one another.
func TestAnItemWithNoDeclaredIDIsNeverReportedAsARepeat(t *testing.T) {
	t.Parallel()
	unvalidated := ledgerGroupedFixture()
	for index := range unvalidated.SubjectResolution.Candidates {
		unvalidated.SubjectResolution.Candidates[index].ReceiptID = ""
	}
	if len(unvalidated.SubjectResolution.Candidates) < 2 {
		t.Fatal("this case needs at least two id-less candidates to be able to fail")
	}
	ledger := ReconcileContextFabricResultItems(unvalidated)
	if !ledger.Reconciled() {
		t.Fatalf("status = %q", ledger.Status)
	}
	for _, repeat := range ledger.RepeatedDeclaredIDs {
		if repeat == string(ContextFabricChargedCandidates)+":" {
			t.Fatalf("candidates carrying no receipt were reported as repeating one another: %v",
				ledger.RepeatedDeclaredIDs)
		}
	}
}

// TestRepeatsAreOrderedAcrossCollectionsToo covers the third named survivor: a
// map range over the collections would order the report differently run to run
// once repeats exist in more than one of them.
func TestRepeatsAreOrderedAcrossCollectionsToo(t *testing.T) {
	t.Parallel()
	member := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_a"}
	result := ContextFabricInvestigationResult{
		Drivers: []ContextFabricDriverJudgment{
			{DriverID: "driver_repeat"}, {DriverID: "driver_repeat"},
		},
		ClaimedFacts: []ContextFabricClaimedFact{
			{ClaimID: "claim_repeat", Subject: member}, {ClaimID: "claim_repeat", Subject: member},
		},
	}
	// Drivers precede claimed_facts in the published vocabulary, so the
	// report is in that order on every run.
	want := []string{
		string(ContextFabricChargedDrivers) + ":driver_repeat",
		string(ContextFabricChargedClaimedFacts) + ":claim_repeat",
	}
	for attempt := 0; attempt < 200; attempt++ {
		got := ReconcileContextFabricResultItems(result).RepeatedDeclaredIDs
		if len(got) != len(want) {
			t.Fatalf("attempt %d: RepeatedDeclaredIDs = %v, want %v", attempt, got, want)
		}
		for index, id := range want {
			if got[index] != id {
				t.Fatalf("attempt %d: RepeatedDeclaredIDs = %v, want %v -- the report depends on map order",
					attempt, got, want)
			}
		}
	}
}

// TestCohortMemberDebitsDeclareTheirSubject covers the fifth named survivor.
// Member rows carry no id of their own, so the subject key IS their diagnosis,
// and nothing else asserted it.
func TestCohortMemberDebitsDeclareTheirSubject(t *testing.T) {
	t.Parallel()
	result := ledgerGroupedFixture()
	ledger := ReconcileContextFabricResultItems(result)

	seen := 0
	for _, debit := range ledger.Debits {
		if debit.Collection != ContextFabricChargedCohortMembers {
			continue
		}
		want := contextFabricSubjectBucketKey(result.Cohort.Members[debit.Ordinal].Subject)
		if debit.DeclaredID != want {
			t.Errorf("cohort member ordinal %d declares %q, want the subject key %q", debit.Ordinal, debit.DeclaredID, want)
		}
		seen++
	}
	if seen != len(result.Cohort.Members) {
		t.Fatalf("examined %d member debits, the fixture carries %d rows", seen, len(result.Cohort.Members))
	}
}
