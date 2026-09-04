package contextfabric

import (
	"os"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Round-1 findings, all four confirmed by re-derivation before any fix.
//
// The round could not EXECUTE (its sandbox denied Go's work dir, 0 go
// test/run/build blocks in 14 exec blocks), so every finding arrived ARGUED.
// These are the executed repros the round could not produce.

// TestTheAllocatorDoesNotDoubleReserveNarration is P1a.
//
// The allocator assigned the ENTIRE spendable pool to Global + per-group +
// remainder, and then handed narration half of the per-group pool on top. Its
// own published invariant omitted narration, so the overlap was invisible to
// the very check meant to catch it.
//
// RED before the fix: reserved 2 + global 9 + groups 18 + members 1 = 30 at a
// ceiling of 30, and narration may then spend 9 more -- 39 against 30.
//
// This is CHAOS-5008's own shape reintroduced by the repair for it: a spender
// that cannot see the others.
func TestTheAllocatorDoesNotDoubleReserveNarration(t *testing.T) {
	t.Parallel()
	for _, maxItems := range []int{30, 45, 300} {
		for groups := 0; groups <= 4; groups++ {
			for members := 0; members <= 10; members++ {
				a := AllocateItems(groupedAllocationPlan(maxItems), groups, members)
				total := a.Reserved + a.Global + (a.Groups * a.ItemsPerGroup) + a.Remainder + a.NarrationBudget + members
				if total > maxItems {
					t.Fatalf("maxItems=%d groups=%d members=%d: every spender together claims %d "+
						"(reserved %d + global %d + %dx%d groups + remainder %d + narration %d + members %d). "+
						"Narration is allocated ON TOP of an already fully-allocated pool, so the quota permits an overrun.",
						maxItems, groups, members, total,
						a.Reserved, a.Global, a.Groups, a.ItemsPerGroup, a.Remainder, a.NarrationBudget, members)
				}
			}
		}
	}
}

// TestNarrationStillGetsAShareOfTheBudget is the positive control for the fix
// above: setting NarrationBudget to zero would satisfy it and delete the
// feature.
func TestNarrationStillGetsAShareOfTheBudget(t *testing.T) {
	t.Parallel()
	a := AllocateItems(groupedAllocationPlan(45), 2, 4)
	if a.NarrationBudget <= 0 {
		t.Fatalf("NarrationBudget = %d on a budget with room: the fix deleted narration rather than bounding it", a.NarrationBudget)
	}
}

// TestExposureImplementsTheDeclaredMultiGroupRule is P1b.
//
// The allocator DECLARES `every_group` -- an item naming several groups is
// charged to each of them -- but the exposure compared `Group + MultiGroup`
// against AGGREGATE capacity, which is the `shared_pool` arithmetic. The two
// disagree exactly where it matters.
//
// RED before the fix: two groups at a quota of 9, eighteen drivers each naming
// BOTH groups. Measured 18 <= 9*2, so OverQuota = 0 -- while under the declared
// rule each group carries all 18 against its 9.
func TestExposureImplementsTheDeclaredMultiGroupRule(t *testing.T) {
	t.Parallel()
	groupOne := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_one", Label: "One"}
	groupTwo := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_two", Label: "Two"}
	member := SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "A"}

	result := InvestigationResult{Cohort: &Cohort{
		Members: []CohortMember{{Subject: member}},
		Groups: []contractsv1.ContextFabricCohortGroup{
			{Subject: groupOne, MemberCanonicalIDs: []string{"project_a"}},
			{Subject: groupTwo, MemberCanonicalIDs: []string{"project_a"}},
		},
	}}
	for i := 0; i < 18; i++ {
		result.Drivers = append(result.Drivers, DriverJudgment{
			DriverID: "d", AffectedSubjects: []SubjectRef{groupOne, groupTwo},
		})
	}

	allocation := AllocateItems(groupedAllocationPlan(30), 2, 1)
	exposure := exposeQuota(allocation, result)

	if allocation.MultiGroupCharge != contractsv1.ContextFabricMultiGroupChargeEveryGroup {
		t.Fatalf("fixture drift: charge rule is %q, this test is about every_group", allocation.MultiGroupCharge)
	}
	if exposure.OverQuota != 2 {
		t.Fatalf("OverQuota = %d, want 2: eighteen drivers each name BOTH groups, so under the DECLARED "+
			"every_group rule each group carries 18 against a quota of %d -- both are over. "+
			"Comparing Group+MultiGroup against aggregate capacity is shared_pool arithmetic, not every_group.",
			exposure.OverQuota, allocation.ItemsPerGroup)
	}
}

// TestQuotaExposureIsActuallyRead is P1c, and the most serious of the four.
//
// QuotaExposure was written onto outcomeNarrowingAttempt at three sites and
// read by NOTHING: recordCandidateNarrowing never copied it, no telemetry field
// carried it. The seam this whole change exists to add was inert -- S7c could
// not act on a breach and no emitted line could diagnose one.
//
// The earlier call-site pin missed this because it tested that the allocation
// is passed IN, never that the exposure is read OUT. Writing a pin at the wrong
// end of a data path is the lesson; this one pins the READ.
func TestQuotaExposureIsActuallyRead(t *testing.T) {
	// Read from SOURCE rather than asserting a struct field exists. A test
	// naming a field the fix adds would fail at the parent as a BUILD ERROR,
	// and a red-first that fails only because it does not compile proves
	// nothing.
	sources := map[string]string{}
	for _, name := range []string{"requirement_outcomes.go", "chaos4636_plan_telemetry.go", "telemetry.go"} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		sources[name] = string(body)
	}

	// WRITTEN: the three sites that populate it.
	writes := strings.Count(sources["requirement_outcomes.go"], "Quota:     exposeQuota") +
		strings.Count(sources["requirement_outcomes.go"], "Quota: exposeQuota")
	if writes == 0 {
		t.Fatal("nothing writes QuotaExposure any more; this guard is watching nothing")
	}

	// READ: something must consume `attempt.Quota` and carry it onward.
	if !strings.Contains(sources["requirement_outcomes.go"], "attempt.Quota") {
		t.Error("QuotaExposure is written but NEVER READ: recordCandidateNarrowing does not consume " +
			"attempt.Quota, so S7c cannot act on a per-group breach and the whole seam is inert. " +
			"A value written at three sites and read at none is dead code wearing a boundary's name.")
	}
	// EMITTED: and it must reach the wire, or an operator cannot diagnose one.
	if !strings.Contains(sources["chaos4636_plan_telemetry.go"], "Quota") {
		t.Error("PlanNarrowingEvent carries no quota field: a per-group breach cannot be diagnosed from the run's own artifacts")
	}
	if !strings.Contains(sources["telemetry.go"], "quota_items_per_group") {
		t.Error("the emitted narrowing line carries no quota dimensions")
	}
}

// TestNarrationAllocatorNamesTheBOUNDThatActuallyBound is P2.
//
// The label was set to plan_budget whenever MaxItems > 0, BEFORE checking
// whether the allocator was actually tighter than the static contract caps.
//
// RED before the fix: at a budget large enough that the allocator is NOT the
// binding constraint, the line still said plan_budget. That defeats the
// consumer-side proof the field exists to be: a reader cannot tell which bound
// applied, which is the one thing it is for.
//
// THE THRESHOLD MOVED UNDER THE P1a FIX, and that is worth recording rather
// than quietly re-tuning: this test first used MaxItems=300, where the old
// half-the-pool narration share allowed 23 narrated members against the caps'
// 16. Carving narration out of the pool at a third dropped that to 15, so 300
// is now a budget where plan_budget genuinely DOES bind -- the label was right
// there for a new reason. The number below is recomputed for the fixed
// allocator, not adjusted until the test passed.
func TestNarrationAllocatorNamesTheBOUNDThatActuallyBound(t *testing.T) {
	t.Parallel()
	ranked, citations := manyRankedMembersCohort(t, 16)
	// A budget so large the allocator is NOT the binding constraint: at 400
	// the allocator permits 21 narrated members and the static contract caps
	// permit 16, so the CAPS are what bound this narration.
	plan := AnswerPlan{Budget: AnswerPlanBudget{MaxItems: 400, SynthesisHeadroom: 20}}
	allocation := AllocateItems(plan, 0, len(ranked.Members))

	_, _, event := narrateCohortDriverJudgments(ranked, nil, 0, citations, allocation)

	if event.Allocator != CohortDriverNarrationAllocatorStaticCaps {
		t.Fatalf("Allocator = %q, want %q: at MaxItems=400 the allocator permits more narrated members "+
			"than the static contract caps do, so the CAPS are what bound this narration. "+
			"Reporting plan_budget here makes the field unable to answer the only question it exists for.",
			event.Allocator, CohortDriverNarrationAllocatorStaticCaps)
	}
}

// TestNarrationAllocatorStillNamesThePlanWhenThePlanBinds is the attribution
// control for P2: always reporting static_caps would satisfy the test above
// and be equally useless.
func TestNarrationAllocatorStillNamesThePlanWhenThePlanBinds(t *testing.T) {
	t.Parallel()
	ranked, citations := manyRankedMembersCohort(t, 16)
	plan := AnswerPlan{Budget: AnswerPlanBudget{MaxItems: 30, SynthesisHeadroom: 20}}

	_, _, event := narrateCohortDriverJudgments(ranked, nil, 0, citations, AllocateItems(plan, 0, len(ranked.Members)))

	if event.Allocator != CohortDriverNarrationAllocatorPlanBudget {
		t.Fatalf("Allocator = %q at MaxItems=30, want %q: here the item budget is far tighter than the caps",
			event.Allocator, CohortDriverNarrationAllocatorPlanBudget)
	}
}
