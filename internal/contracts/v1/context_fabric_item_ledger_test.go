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
		RemainingWork: []ContextFabricFinding{{FindingID: "finding_group_two", Subjects: []ContextFabricSubjectRef{groupTwo}}},
		ReadinessGaps: []ContextFabricFinding{{FindingID: "finding_global"}},
		Conflicts:     []ContextFabricFinding{{FindingID: "finding_member_b", Subjects: []ContextFabricSubjectRef{memberB}}},
		ClaimedFacts:  []ContextFabricClaimedFact{{ClaimID: "claim_member_b", Subject: memberB}},
		// Paths are charged by Total() and NOT by Budgeted(), so they must
		// mint no debit. Present in the fixture so that exclusion is
		// exercised rather than assumed.
		Paths: []ContextFabricRelationshipPath{{PathID: "path_one"}, {PathID: "path_two"}},
	}
}

// TestEveryChargedOccurrenceHasExactlyOneDebit is the accounting check itself:
// a bijection between charged occurrences and debits, proved against the two
// INDEPENDENT derivations this package already publishes rather than against
// the ledger's own walk.
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
	if ValidContextFabricLedgerStatus("") {
		t.Error("the empty ledger status validates, so an unset status could read as reconciled")
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
