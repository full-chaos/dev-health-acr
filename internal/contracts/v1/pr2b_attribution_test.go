package v1

import "testing"

// attributionFixture builds a result in which EVERY bucket is non-empty and
// every bucket's item is DISTINCT, so no bucket can be satisfied by another
// bucket's item and a fixture that quietly stopped covering one is visible.
func attributionFixture() (ContextFabricInvestigationResult, map[ContextFabricItemBucket]int) {
	memberA := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_a", Label: "A"}
	memberB := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_b", Label: "B"}
	groupOne := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_one", Label: "One"}
	groupTwo := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_two", Label: "Two"}
	stranger := ContextFabricSubjectRef{Kind: ContextFabricSubjectRepository, CanonicalID: "repo_x", Label: "X"}

	result := ContextFabricInvestigationResult{
		SubjectResolution: ContextFabricSubjectResolution{
			// global: two candidates
			Candidates: []ContextFabricSubjectCandidate{{Subject: memberA}, {Subject: memberB}},
		},
		Cohort: &ContextFabricCohort{
			Members: []ContextFabricCohortMember{{Subject: memberA}, {Subject: memberB}},
			Groups: []ContextFabricCohortGroup{
				{Subject: groupOne, MemberCanonicalIDs: []string{"project_a"}},
				{Subject: groupTwo, MemberCanonicalIDs: []string{"project_b"}},
			},
		},
		Drivers: []ContextFabricDriverJudgment{
			{DriverID: "d1", AffectedSubjects: []ContextFabricSubjectRef{memberA}},            // member
			{DriverID: "d2", AffectedSubjects: []ContextFabricSubjectRef{groupOne}},           // group
			{DriverID: "d3", AffectedSubjects: []ContextFabricSubjectRef{groupOne, groupTwo}}, // multi_group
			{DriverID: "d4", AffectedSubjects: []ContextFabricSubjectRef{stranger}},           // global
		},
		RemainingWork: []ContextFabricFinding{
			{FindingID: "f1", Subjects: []ContextFabricSubjectRef{groupTwo}}, // group
			{FindingID: "f2"}, // global, names nothing
		},
		ClaimedFacts: []ContextFabricClaimedFact{
			{ClaimID: "c1", Subject: memberB}, // member
		},
	}

	want := map[ContextFabricItemBucket]int{
		// 2 candidates + driver d4 + finding f2
		ContextFabricItemBucketGlobal: 4,
		// 2 cohort member rows + driver d1 + claim c1
		ContextFabricItemBucketMember: 4,
		// driver d2 + finding f1
		ContextFabricItemBucketGroup: 2,
		// driver d3
		ContextFabricItemBucketMultiGroup: 1,
	}
	return result, want
}

// TestEveryChargedItemIsAttributedToExactlyOneBucket is the invariant the whole
// quota rests on: the attribution splits exactly the quantity the item budget
// enforces, no more and no less. If these can disagree, every per-group figure
// is apportioning a number nothing checks.
func TestEveryChargedItemIsAttributedToExactlyOneBucket(t *testing.T) {
	t.Parallel()
	result, want := attributionFixture()

	// The fixture's own claim FIRST: a fixture that silently stopped
	// covering a bucket would otherwise make this test pass by testing less.
	for bucket, count := range want {
		if count == 0 {
			t.Fatalf("fixture claims to cover %q with zero items -- the fixture changed, not the invariant", bucket)
		}
	}

	attribution := AttributeContextFabricResultItems(result)
	budgeted := CountContextFabricResultItems(result).Budgeted()

	if attribution.Total() != budgeted {
		t.Fatalf("attribution totals %d but the item budget charges %d: the quota would apportion a "+
			"number the budget does not enforce.\nattribution = %+v", attribution.Total(), budgeted, attribution)
	}

	got := map[ContextFabricItemBucket]int{
		ContextFabricItemBucketGlobal:     attribution.Global,
		ContextFabricItemBucketMember:     attribution.Member,
		ContextFabricItemBucketGroup:      attribution.Group,
		ContextFabricItemBucketMultiGroup: attribution.MultiGroup,
	}
	for bucket, wantCount := range want {
		if got[bucket] != wantCount {
			t.Errorf("bucket %q = %d, want %d (full attribution %+v)", bucket, got[bucket], wantCount, attribution)
		}
	}
}

// TestAMultiGroupItemChargesOneBucketNotOnePerGroup separates ATTRIBUTION from
// PRICING. A driver naming two groups is one item and is charged once; how the
// allocator then prices it across per-group quotas is a separate declared
// decision. Conflating the two breaks the totals-sum invariant the moment the
// pricing rule changes.
func TestAMultiGroupItemChargesOneBucketNotOnePerGroup(t *testing.T) {
	t.Parallel()
	result, _ := attributionFixture()
	attribution := AttributeContextFabricResultItems(result)
	if attribution.MultiGroup != 1 {
		t.Fatalf("MultiGroup = %d, want 1: a two-group driver is ONE item, charged once", attribution.MultiGroup)
	}
	if attribution.Total() != CountContextFabricResultItems(result).Budgeted() {
		t.Fatal("charging a multi-group item per group broke the totals-sum invariant")
	}
}

// TestADuplicateGroupMentionDoesNotPromoteAnItemToMultiGroup is the negative
// control on the "distinct groups" rule. Counting mentions rather than distinct
// groups would let a driver naming one group twice be priced as cross-cutting.
func TestADuplicateGroupMentionDoesNotPromoteAnItemToMultiGroup(t *testing.T) {
	t.Parallel()
	group := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_one", Label: "One"}
	result := ContextFabricInvestigationResult{
		Cohort: &ContextFabricCohort{Groups: []ContextFabricCohortGroup{{Subject: group}}},
		Drivers: []ContextFabricDriverJudgment{
			{DriverID: "d1", AffectedSubjects: []ContextFabricSubjectRef{group, group}},
		},
	}
	attribution := AttributeContextFabricResultItems(result)
	if attribution.Group != 1 || attribution.MultiGroup != 0 {
		t.Fatalf("a driver naming ONE group twice was attributed %+v, want group=1 multi_group=0", attribution)
	}
}

// TestAnUnrecognisedSubjectChargesTheGlobalPool pins the fail-safe direction:
// an item whose subject matches no member and no group must not silently
// inflate some group's usage.
func TestAnUnrecognisedSubjectChargesTheGlobalPool(t *testing.T) {
	t.Parallel()
	result := ContextFabricInvestigationResult{
		Cohort: &ContextFabricCohort{},
		Drivers: []ContextFabricDriverJudgment{{DriverID: "d1", AffectedSubjects: []ContextFabricSubjectRef{
			{Kind: ContextFabricSubjectRepository, CanonicalID: "repo_unknown"},
		}}},
	}
	if attribution := AttributeContextFabricResultItems(result); attribution.Global != 1 {
		t.Fatalf("attribution = %+v, want the unrecognised subject charged to global", attribution)
	}
}

// TestItemBucketVocabularyIsClosed keeps the vocabulary honest, in the manner
// every other closed vocabulary in this package is pinned.
func TestItemBucketVocabularyIsClosed(t *testing.T) {
	t.Parallel()
	if got := len(ContextFabricItemBucketVocabulary()); got != ContextFabricItemBucketCount {
		t.Fatalf("vocabulary size %d != published count %d", got, ContextFabricItemBucketCount)
	}
	for _, bucket := range ContextFabricItemBucketVocabulary() {
		if !ValidContextFabricItemBucket(bucket) {
			t.Errorf("published member %q is not valid", bucket)
		}
	}
	if ValidContextFabricItemBucket("") || ValidContextFabricItemBucket("invented") {
		t.Error("a non-member was accepted into the closed vocabulary")
	}
	for _, charge := range ContextFabricMultiGroupChargeVocabulary() {
		if !ValidContextFabricMultiGroupCharge(charge) {
			t.Errorf("published charge rule %q is not valid", charge)
		}
	}
	if ValidContextFabricMultiGroupCharge("") || ValidContextFabricMultiGroupCharge("invented") {
		t.Error("a non-member was accepted into the multi-group charge vocabulary")
	}
}
