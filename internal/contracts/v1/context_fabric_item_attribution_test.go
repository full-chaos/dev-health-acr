package v1

import (
	"reflect"
	"testing"
)

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

// TestEveryChargedItemIsAttributedToExactlyOneBucket is the invariant the
// whole split rests on: it partitions exactly the quantity the item budget
// enforces, no more and no less. If the two can disagree, the four numbers
// describe a different answer from the one that was measured.
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
		t.Fatalf("attribution totals %d but the item budget charges %d: the split describes a "+
			"different answer from the one measured.\nattribution = %+v", attribution.Total(), budgeted, attribution)
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

// TestAMeasurementSplitsExactlyTheQuantityItCharges asserts the invariant
// where it actually matters: on the measurement, which is the value every
// caller reads. The function-level test above can hold while a caller pairs a
// split of one document with a count of another; this one cannot, because
// MeasureContextFabricResponse computes both from a single result.
func TestAMeasurementSplitsExactlyTheQuantityItCharges(t *testing.T) {
	t.Parallel()
	result, _ := attributionFixture()

	measurement, err := MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse() error = %v", err)
	}
	if measurement.Attribution.Total() != measurement.Items.Budgeted() {
		t.Fatalf("measurement.Attribution.Total() = %d but measurement.Items.Budgeted() = %d: "+
			"one measurement reports two different sizes for the same document\nmeasurement = %+v",
			measurement.Attribution.Total(), measurement.Items.Budgeted(), measurement)
	}
	// A measurement whose split is entirely zero would satisfy the equality
	// above only for an empty answer. This fixture is not empty, so a zero
	// split here means the measurement stopped computing one.
	if measurement.Attribution.Total() == 0 {
		t.Fatal("the measurement carries an all-zero split for a non-empty result")
	}
	// PATHS ARE EXCLUDED FROM BOTH, and that is a real exclusion rather
	// than an accident of this fixture: adding a path must move Total() and
	// leave both Budgeted() and the split alone.
	withPath := result
	withPath.Paths = []ContextFabricRelationshipPath{{PathID: "p1"}}
	pathed, err := MeasureContextFabricResponse(withPath)
	if err != nil {
		t.Fatalf("MeasureContextFabricResponse() error = %v", err)
	}
	if pathed.Items.Total() != measurement.Items.Total()+1 {
		t.Fatalf("adding a path did not change the un-budgeted total: %d then %d",
			measurement.Items.Total(), pathed.Items.Total())
	}
	if pathed.Attribution != measurement.Attribution {
		t.Errorf("a path changed the split (%+v -> %+v); paths are excluded from the item budget, "+
			"so they must be excluded from its partition too", measurement.Attribution, pathed.Attribution)
	}
}

// TestAMultiGroupItemChargesOneBucketNotOnePerGroup keeps "what is this item"
// separate from "who would pay for it". A driver naming two groups is one
// item and is charged once. Charging it per group would make the totals stop
// summing to Budgeted(), which is the one property this split must hold.
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
// control on the "distinct groups" rule. Counting mentions rather than
// distinct groups would let a driver naming one group twice read as
// cross-cutting.
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

// TestAGroupAndAMemberSharingAnIDAreNotConfused pins the composite key. An
// id-only key would let a project and a team that happen to carry the same
// canonical id charge each other's bucket, and the two vocabularies are
// independent, so a collision is a naming coincidence rather than an error.
func TestAGroupAndAMemberSharingAnIDAreNotConfused(t *testing.T) {
	t.Parallel()
	shared := "shared_id"
	member := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: shared, Label: "P"}
	group := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: shared, Label: "T"}
	result := ContextFabricInvestigationResult{
		Cohort: &ContextFabricCohort{
			Members: []ContextFabricCohortMember{{Subject: member}},
			Groups:  []ContextFabricCohortGroup{{Subject: group}},
		},
		Drivers: []ContextFabricDriverJudgment{
			{DriverID: "d_member", AffectedSubjects: []ContextFabricSubjectRef{member}},
			{DriverID: "d_group", AffectedSubjects: []ContextFabricSubjectRef{group}},
		},
	}
	attribution := AttributeContextFabricResultItems(result)
	// One member row + the member driver; the group driver alone.
	if attribution.Member != 2 || attribution.Group != 1 {
		t.Fatalf("attribution = %+v, want member=2 group=1: a shared canonical id across two "+
			"subject kinds must not merge the buckets", attribution)
	}
}

// TestAnUnrecognisedSubjectChargesTheGlobalPool pins the fail-safe direction:
// an item whose subject matches no member and no group must not silently
// inflate some group's reading.
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

// TestAnItemNamingOneGroupAndAMemberIsAGroupItem pins the precedence step
// that a "member wins" ordering would silently invert: a driver about a team
// that cites one of its projects is an item about the team, and charging it
// to the member would leave the group bucket unable to see the item most
// characteristically its own.
func TestAnItemNamingOneGroupAndAMemberIsAGroupItem(t *testing.T) {
	t.Parallel()
	member := ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "project_a"}
	group := ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team_one"}
	result := ContextFabricInvestigationResult{
		Cohort: &ContextFabricCohort{
			Members: []ContextFabricCohortMember{{Subject: member}},
			Groups:  []ContextFabricCohortGroup{{Subject: group}},
		},
		Drivers: []ContextFabricDriverJudgment{
			{DriverID: "d1", AffectedSubjects: []ContextFabricSubjectRef{member, group}},
		},
	}
	attribution := AttributeContextFabricResultItems(result)
	// The member row is member; the driver is group.
	if attribution.Group != 1 || attribution.Member != 1 {
		t.Fatalf("attribution = %+v, want group=1 (the driver) member=1 (the cohort row)", attribution)
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
}

// chargedCollectionRow says how to add ONE item of one charged collection to a
// result, and which bucket that item must land in.
//
// The map key is a FIELD NAME of ContextFabricResultItemCounts, which is what
// makes this a population rather than a list: the test below reflects over
// that struct and fails if it holds a field this table does not name. A new
// charged collection therefore cannot be added to the counter without either
// a row here or a deliberate exclusion.
type chargedCollectionRow struct {
	add    func(*ContextFabricInvestigationResult, ContextFabricSubjectRef)
	bucket ContextFabricItemBucket
	// budgeted says whether the item budget charges this collection at all.
	// Paths are the one collection it does not (they are evidence, excluded
	// from Budgeted), so they must move Total() and move neither
	// Budgeted() nor the split.
	budgeted bool
}

func chargedCollections() map[string]chargedCollectionRow {
	return map[string]chargedCollectionRow{
		"Candidates": {budgeted: true, bucket: ContextFabricItemBucketGlobal,
			add: func(r *ContextFabricInvestigationResult, s ContextFabricSubjectRef) {
				r.SubjectResolution.Candidates = append(r.SubjectResolution.Candidates, ContextFabricSubjectCandidate{Subject: s})
			}},
		"Drivers": {budgeted: true, bucket: ContextFabricItemBucketGlobal,
			add: func(r *ContextFabricInvestigationResult, s ContextFabricSubjectRef) {
				r.Drivers = append(r.Drivers, ContextFabricDriverJudgment{DriverID: "probe_driver", AffectedSubjects: []ContextFabricSubjectRef{s}})
			}},
		"RemainingWork": {budgeted: true, bucket: ContextFabricItemBucketGlobal,
			add: func(r *ContextFabricInvestigationResult, s ContextFabricSubjectRef) {
				r.RemainingWork = append(r.RemainingWork, ContextFabricFinding{FindingID: "probe_remaining", Subjects: []ContextFabricSubjectRef{s}})
			}},
		"ReadinessGaps": {budgeted: true, bucket: ContextFabricItemBucketGlobal,
			add: func(r *ContextFabricInvestigationResult, s ContextFabricSubjectRef) {
				r.ReadinessGaps = append(r.ReadinessGaps, ContextFabricFinding{FindingID: "probe_readiness", Subjects: []ContextFabricSubjectRef{s}})
			}},
		"Conflicts": {budgeted: true, bucket: ContextFabricItemBucketGlobal,
			add: func(r *ContextFabricInvestigationResult, s ContextFabricSubjectRef) {
				r.Conflicts = append(r.Conflicts, ContextFabricFinding{FindingID: "probe_conflict", Subjects: []ContextFabricSubjectRef{s}})
			}},
		"ClaimedFacts": {budgeted: true, bucket: ContextFabricItemBucketGlobal,
			add: func(r *ContextFabricInvestigationResult, s ContextFabricSubjectRef) {
				r.ClaimedFacts = append(r.ClaimedFacts, ContextFabricClaimedFact{ClaimID: "probe_claim", Subject: s})
			}},
		"CohortMembers": {budgeted: true, bucket: ContextFabricItemBucketMember,
			add: func(r *ContextFabricInvestigationResult, s ContextFabricSubjectRef) {
				r.Cohort.Members = append(r.Cohort.Members, ContextFabricCohortMember{Subject: s})
			}},
		"Paths": {budgeted: false,
			add: func(r *ContextFabricInvestigationResult, _ ContextFabricSubjectRef) {
				r.Paths = append(r.Paths, ContextFabricRelationshipPath{PathID: "probe_path"})
			}},
	}
}

// TestEveryChargedCollectionMovesTheSplit is the sweep that closes class A.
//
// WHY IT EXISTS. The invariant test above compares two totals on ONE fixture,
// so it is blind to any collection that fixture happens not to populate:
// deleting `result.Conflicts` from the attribution walk left it green, and
// that is precisely the defect shape this seam has produced over and over --
// a quantity the budget charges that the split does not see. A total-vs-total
// comparison cannot find it; only quantifying over the collections can.
//
// The population is the FIELD SET of ContextFabricResultItemCounts, read by
// reflection from the production type, so a collection added to the counter
// without a row here fails this test rather than silently widening the gap.
func TestEveryChargedCollectionMovesTheSplit(t *testing.T) {
	t.Parallel()
	rows := chargedCollections()

	// The population, derived from the production type rather than from this
	// table -- otherwise the table would be agreeing with itself.
	countsType := reflect.TypeOf(ContextFabricResultItemCounts{})
	if countsType.NumField() < 8 {
		t.Fatalf("ContextFabricResultItemCounts has %d fields; the reflection is broken and an "+
			"empty population reports a clean sweep for a check that never ran", countsType.NumField())
	}
	for index := 0; index < countsType.NumField(); index++ {
		name := countsType.Field(index).Name
		if _, covered := rows[name]; !covered {
			t.Errorf("ContextFabricResultItemCounts.%s is a charged collection with no row in this "+
				"sweep: nothing proves the attribution walk sees it", name)
		}
	}
	if len(rows) != countsType.NumField() {
		t.Errorf("the sweep has %d rows for %d counter fields: a row names a collection the counter "+
			"does not have, so it is testing something that is not charged", len(rows), countsType.NumField())
	}

	stranger := ContextFabricSubjectRef{Kind: ContextFabricSubjectRepository, CanonicalID: "repo_probe", Label: "probe"}

	for name, row := range rows {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			base, _ := attributionFixture()
			beforeCounts := CountContextFabricResultItems(base)
			beforeSplit := AttributeContextFabricResultItems(base)

			probed, _ := attributionFixture()
			row.add(&probed, stranger)
			afterCounts := CountContextFabricResultItems(probed)
			afterSplit := AttributeContextFabricResultItems(probed)

			// The probe must actually have added something, or every
			// assertion below is satisfied by an unchanged result.
			if afterCounts.Total() != beforeCounts.Total()+1 {
				t.Fatalf("adding one %s item moved the un-budgeted total by %d, want 1: the probe did not "+
					"add what it claims to", name, afterCounts.Total()-beforeCounts.Total())
			}

			wantBudgetedDelta := 0
			if row.budgeted {
				wantBudgetedDelta = 1
			}
			if got := afterCounts.Budgeted() - beforeCounts.Budgeted(); got != wantBudgetedDelta {
				t.Fatalf("adding one %s item moved Budgeted() by %d, want %d", name, got, wantBudgetedDelta)
			}
			if got := afterSplit.Total() - beforeSplit.Total(); got != wantBudgetedDelta {
				t.Fatalf("adding one %s item moved Budgeted() by %d but the split by %d: this collection "+
					"is charged and the attribution walk does not see it -- the split under-reports the "+
					"answer by one item per %s", name, wantBudgetedDelta, got, name)
			}
			if !row.budgeted {
				return
			}
			bucketDelta := map[ContextFabricItemBucket]int{
				ContextFabricItemBucketGlobal:     afterSplit.Global - beforeSplit.Global,
				ContextFabricItemBucketMember:     afterSplit.Member - beforeSplit.Member,
				ContextFabricItemBucketGroup:      afterSplit.Group - beforeSplit.Group,
				ContextFabricItemBucketMultiGroup: afterSplit.MultiGroup - beforeSplit.MultiGroup,
			}
			for bucket, delta := range bucketDelta {
				want := 0
				if bucket == row.bucket {
					want = 1
				}
				if delta != want {
					t.Errorf("adding one %s item moved bucket %q by %d, want %d (the item names a subject "+
						"the cohort does not contain, so it belongs in %q)", name, bucket, delta, want, row.bucket)
				}
			}
		})
	}
}
