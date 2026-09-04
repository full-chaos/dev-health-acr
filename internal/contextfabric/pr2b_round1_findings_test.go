package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
	// The sweep now includes the DEGENERATE regime (1, 2, 5) where the
	// reserved allowance alone meets or exceeds the budget. Round 2 found a
	// bounded quota of zero being mishandled there, and the old sweep of
	// {30, 45, 300} could not reach it.
	for _, maxItems := range []int{1, 2, 5, 30, 45, 300} {
		for groups := 0; groups <= 4; groups++ {
			for members := 0; members <= 10; members++ {
				a := AllocateItems(groupedAllocationPlan(maxItems), groups, members)
				// EXACT partition, not an upper bound. "<=" would pass on an
				// allocation that quietly strands budget, and every pool is
				// derived from the bucket vocabulary now, so there is no
				// claimant left outside this sum to excuse a gap.
				total := a.Reserved + a.TotalPooled() + a.Remainder + a.NarrationBudget
				if total != maxItems {
					t.Fatalf("maxItems=%d groups=%d members=%d: every spender together accounts for %d "+
						"(reserved %d + pools %v + remainder %d + narration %d). The pools must partition the "+
						"ceiling EXACTLY -- a claimant outside them is a spender the others cannot see.",
						maxItems, groups, members, total,
						a.Reserved, a.Pools, a.Remainder, a.NarrationBudget)
				}
				// EVERY bucket that can receive items must HAVE a pool when
				// there is anything to give. This is the class guard: the
				// first two versions of this allocator each forgot a
				// different claimant.
				for _, bucket := range activeBuckets(groups) {
					if a.TotalPooled() > 0 && a.Pool(bucket) == 0 && maxItems > 8 {
						t.Fatalf("maxItems=%d groups=%d: bucket %q has NO pool while others do (%v) -- "+
							"a bucket the answer can write into with no allowance is exactly the class "+
							"this allocator was redesigned to make unexpressible",
							maxItems, groups, bucket, a.Pools)
					}
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
	t.Parallel()
	// REPLACES A LEXICAL PIN, and the replacement is the finding.
	//
	// The first version of this test read the package source and asserted
	// that the string "attempt.Quota" appeared somewhere. It passed while
	// the quota reached the SERVED narrowing emitter and NEITHER refusal
	// arm -- so on a refusal, the case where a per-group breach matters
	// most, the emitted line carried zeros. A pin that proves a string
	// exists proves nothing about a consumer.
	//
	// So this asserts the EMITTED LINE, for every emitter that can carry it.
	for _, emitter := range []struct {
		name  string
		event PlanNarrowingEvent
	}{
		{"served narrowing", PlanNarrowingEvent{QuotaItemsPerGroup: 9, QuotaGroups: 2, QuotaOverQuota: 1}},
		{"refusal", PlanNarrowingEvent{RefusalPlanned: true, QuotaItemsPerGroup: 9, QuotaGroups: 2, QuotaOverQuota: 1}},
	} {
		var buf bytes.Buffer
		telemetry := NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buf, nil)))
		telemetry.RecordPlanNarrowing(context.Background(), storage.Principal{OrgID: "org_1"}, emitter.event)

		line := buf.String()
		for _, want := range []string{
			"quota_items_per_group=9",
			"quota_groups=2",
			"quota_over_quota=1",
		} {
			if !strings.Contains(line, want) {
				t.Errorf("%s line does not carry %q -- enforcement is handed no per-group numbers on this path.\nline: %s",
					emitter.name, want, line)
			}
		}
	}
}

// TestBothRefusalArmsCarryTheQuotaToTheLine pins the CALL SITES of the two
// refusal paths, which is where the quota was actually being dropped.
//
// The served emitter copied attempt.Quota; planRefusal built its event without
// it, and the retry-refusal arm built its own event without it too. Two
// consumers, both silent, while a source-grep pin stayed green.
func TestBothRefusalArmsCarryTheQuotaToTheLine(t *testing.T) {
	source, err := os.ReadFile("chaos4636_budget_stage3.go")
	if err != nil {
		t.Fatalf("read stage-3 source: %v", err)
	}
	body := string(source)
	// The declined arm passes the exposure INTO planRefusal...
	if !strings.Contains(body, "attempt.Declined, attempt.Quota)") {
		t.Error("the declined refusal arm does not pass the quota into planRefusal: a refusal from that arm " +
			"emits zeros for the per-group numbers while the attempt holds the real ones")
	}
	// ...planRefusal stamps them...
	if !strings.Contains(body, "event.QuotaItemsPerGroup = quota.ItemsPerGroup") {
		t.Error("planRefusal does not stamp the quota onto its event")
	}
	// ...and the retry-refusal arm stamps its own.
	if !strings.Contains(body, "event.QuotaItemsPerGroup = outcomeAttempt.Quota.ItemsPerGroup") {
		t.Error("the retry-refusal arm does not stamp the quota onto its event: the same omission, one branch over")
	}
	// The served emitter's own read, so all three consumers are covered.
	outcomes, err := os.ReadFile("requirement_outcomes.go")
	if err != nil {
		t.Fatalf("read outcomes source: %v", err)
	}
	if !strings.Contains(string(outcomes), "attempt.Quota.ItemsPerGroup") {
		t.Error("the served narrowing emitter does not read attempt.Quota")
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

// TestABoundedZeroQuotaIsMeasuredNotSkipped is round 2's second P1.
//
// `exposeQuota` returned early whenever ItemsPerGroup <= 0, which silently
// covered the case where the quota is genuinely ZERO -- a budget too small to
// give a group anything. There, EVERY charged item is over quota, and
// reporting zero groups over was the one answer that could not be right.
//
// It is my own absent-vs-zero distinction violated in the other direction: an
// absent quota (no group axis) means nothing to measure; a quota OF zero means
// everything is over.
func TestABoundedZeroQuotaIsMeasuredNotSkipped(t *testing.T) {
	t.Parallel()
	group := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_one", Label: "One"}
	result := InvestigationResult{
		Cohort:  &Cohort{Groups: []contractsv1.ContextFabricCohortGroup{{Subject: group}}},
		Drivers: []DriverJudgment{{DriverID: "d1", AffectedSubjects: []SubjectRef{group}}},
	}
	// A ceiling smaller than the deterministic reserve: the group's quota is
	// a real zero.
	allocation := AllocateItems(AnswerPlan{Budget: AnswerPlanBudget{MaxItems: 1}}, 1, 1)
	if allocation.ItemsPerGroup != 0 {
		t.Fatalf("fixture drift: ItemsPerGroup = %d, this test is about a quota of zero", allocation.ItemsPerGroup)
	}
	exposure := exposeQuota(allocation, result)
	if exposure.OverQuota != 1 {
		t.Fatalf("OverQuota = %d, want 1: one driver names the group and its quota is ZERO, so it is over. "+
			"Skipping the measurement because the quota is zero reports the one answer that cannot be true.",
			exposure.OverQuota)
	}
}

// TestNoDriversIsANamedOutcomeNotUnclassified is round 2's P2.
//
// The ordinary no-driver path left the allocator empty, and the emitter
// converts any non-vocabulary value to `unclassified` -- the word reserved for
// corrupt or future enum input. Conflating a documented normal outcome with
// corruption makes the fail-closed signal useless: an operator filtering for
// real corruption would drown in ordinary no-driver runs.
func TestNoDriversIsANamedOutcomeNotUnclassified(t *testing.T) {
	t.Parallel()
	_, _, event := narrateCohortDriverJudgments(nil, nil, 0, nil, ItemAllocation{})
	if event.Outcome != CohortDriverNarrationNoDrivers {
		t.Fatalf("fixture drift: outcome = %q", event.Outcome)
	}
	if event.Allocator != CohortDriverNarrationAllocatorNotApplicable {
		t.Fatalf("Allocator = %q on the no-drivers path, want %q", event.Allocator, CohortDriverNarrationAllocatorNotApplicable)
	}

	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buf, nil)))
	telemetry.RecordCohortDriverNarration(context.Background(), storage.Principal{OrgID: "org_1"}, event)
	line := buf.String()
	if strings.Contains(line, "narration_allocator=unclassified") {
		t.Errorf("a normal no-drivers run is logged as `unclassified`, which is the word for corrupt input.\nline: %s", line)
	}
	if !strings.Contains(line, "narration_allocator=not_applicable") {
		t.Errorf("line does not name the no-drivers state.\nline: %s", line)
	}
}
