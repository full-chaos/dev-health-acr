package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// SEPARATE FILE, deliberately: everything here names the allocator API this
// change introduces, so it cannot compile at the fix parent. Keeping it out of
// pr2b_allocator_class_test.go is what lets that file compile at the parent and
// fail there on its own assertion.

func groupedAllocationPlan(maxItems int) AnswerPlan {
	return AnswerPlan{Budget: AnswerPlanBudget{MaxItems: maxItems, SynthesisHeadroom: 20}}
}

// TestTheAllocatorRespectsTheInvariant is the arithmetic the whole quota rests
// on. Everything the answer may spend has to fit inside the ceiling BEFORE
// synthesis is asked for anything, or the quota is advice.
func TestTheAllocatorRespectsTheInvariant(t *testing.T) {
	t.Parallel()
	for _, maxItems := range []int{30, 45} {
		for groups := 0; groups <= 6; groups++ {
			for members := 0; members <= 12; members++ {
				allocation := AllocateItems(groupedAllocationPlan(maxItems), groups, members)
				spent := allocation.Reserved + allocation.Global +
					(allocation.Groups * allocation.ItemsPerGroup) + allocation.Remainder + members
				if spent > maxItems {
					t.Fatalf("maxItems=%d groups=%d members=%d: allocation spends %d "+
						"(reserved %d + global %d + %d groups x %d + remainder %d + members %d), over the ceiling",
						maxItems, groups, members, spent,
						allocation.Reserved, allocation.Global, allocation.Groups,
						allocation.ItemsPerGroup, allocation.Remainder, members)
				}
			}
		}
	}
}

// TestTheAllocatorPublishesTheDeclaredMultiGroupRule pins that the choice is a
// DECLARED decision rather than an implementation detail. The two rules give
// different answers to "does this fit", and the per-bucket counts alone cannot
// say which one ran.
func TestTheAllocatorPublishesTheDeclaredMultiGroupRule(t *testing.T) {
	t.Parallel()
	allocation := AllocateItems(groupedAllocationPlan(30), 4, 8)
	if !contractsv1.ValidContextFabricMultiGroupCharge(allocation.MultiGroupCharge) {
		t.Fatalf("MultiGroupCharge = %q, which is outside the closed vocabulary", allocation.MultiGroupCharge)
	}
	if allocation.MultiGroupCharge != contractsv1.ContextFabricMultiGroupChargeEveryGroup {
		t.Fatalf("MultiGroupCharge = %q, want %q: a shared pool can hide a cross-cutting item from "+
			"every group's quota and let the total overrun while each per-group number still looks compliant",
			allocation.MultiGroupCharge, contractsv1.ContextFabricMultiGroupChargeEveryGroup)
	}
}

// TestRemainderIsPublishedAndDeterministic pins both halves: the split does not
// depend on group ORDER (which this system does not promise to be stable), and
// what integer division could not distribute is REPORTED rather than silently
// dropped or silently handed to whichever group happened to be first.
func TestRemainderIsPublishedAndDeterministic(t *testing.T) {
	t.Parallel()
	plan := groupedAllocationPlan(30)
	first := AllocateItems(plan, 3, 7)
	for i := 0; i < 8; i++ {
		again := AllocateItems(plan, 3, 7)
		if again != first {
			t.Fatalf("AllocateItems is not deterministic: %+v then %+v", first, again)
		}
	}
	pool := first.Global + (first.Groups * first.ItemsPerGroup) + first.Remainder
	if want := first.MaxItems - first.Reserved - 7; pool != want {
		t.Fatalf("global+groups+remainder = %d, want the whole spendable pool %d: an item went missing in the split", pool, want)
	}
	if first.Remainder < 0 || first.Remainder >= first.Groups {
		t.Fatalf("Remainder = %d with %d groups: a remainder at or above the group count means the split under-allocated",
			first.Remainder, first.Groups)
	}
}

// TestAnUnboundedBudgetIsNoQuotaRatherThanAQuotaOfZero is the fail-safe
// direction. MaxItems <= 0 means unbounded; reading that as "every group gets
// zero items" would silence a whole answer on the configuration that asked for
// no limit at all.
func TestAnUnboundedBudgetIsNoQuotaRatherThanAQuotaOfZero(t *testing.T) {
	t.Parallel()
	allocation := AllocateItems(AnswerPlan{Budget: AnswerPlanBudget{MaxItems: 0}}, 4, 8)
	if allocation.MaxItems != 0 || allocation.ItemsPerGroup != 0 || allocation.NarrationBudget != 0 {
		t.Fatalf("unbounded budget produced a quota: %+v", allocation)
	}
	// And the consumer must read it as "no quota": narration is unbounded.
	if got := allocation.NarrationDriverAllowance(3); got != 0 {
		t.Fatalf("NarrationDriverAllowance = %d on an unbounded allocation, want 0 (the caller applies its own cap)", got)
	}
}

// TestTheAllocatorIsIdempotent is ruling 3's guard. planCandidateNarrowing
// RE-ENTERS finalizeResult, so anything derived from the plan is computed twice
// on any narrowed answer. A non-idempotent allocator would double-count
// invisibly -- no compiler and no reviewer catches that.
func TestTheAllocatorIsIdempotent(t *testing.T) {
	t.Parallel()
	plan := groupedAllocationPlan(45)
	first := AllocateItems(plan, 4, 10)
	second := AllocateItems(plan, 4, 10)
	if first != second {
		t.Fatalf("allocator is not idempotent across re-derivation: %+v then %+v", first, second)
	}
	// The plan must not be mutated by allocating against it.
	if plan.Budget.MaxItems != 45 || plan.Budget.SynthesisHeadroom != 20 {
		t.Fatalf("AllocateItems mutated the plan it was given: %+v", plan.Budget)
	}
}

// TestNarrationAllowanceChargesBothDriversAndTheirClaims pins the doubling the
// static-cap version never saw: each narrated driver mints a claim, so a
// narrated member charges TWICE its drivers against the item budget.
func TestNarrationAllowanceChargesBothDriversAndTheirClaims(t *testing.T) {
	t.Parallel()
	allocation := AllocateItems(groupedAllocationPlan(45), 0, 10)
	const driversPerMember = 3

	members := allocation.NarrationDriverAllowance(driversPerMember)
	charged := members * driversPerMember * 2 // drivers + one minted claim each
	if charged > allocation.NarrationBudget {
		t.Fatalf("narration allowance of %d members x %d drivers charges %d items against a narration budget of %d: "+
			"the minted claims are not being counted", members, driversPerMember, charged, allocation.NarrationBudget)
	}
}

// TestTheAssemblyPathActuallyAllocates pins the CALL SITE, not the allocator.
//
// Every other test here drives AllocateItems directly, so deleting `Plan: plan`
// from the assembly params -- which silently makes every allocation unbounded
// and restores the CHAOS-5008 behaviour in production while leaving the unit
// tests green -- would survive all of them. That is the same mutation shape
// this lane already had survive once, on a disclosure composer nobody called.
func TestTheAssemblyPathActuallyAllocates(t *testing.T) {
	// Constructing a params value here and asserting the field exists would
	// prove only that the STRUCT has a Plan -- it would stay green with the
	// production call site never setting it. So this reads the source and
	// asserts the literal POPULATES it.
	source, err := os.ReadFile("engine.go")
	if err != nil {
		t.Fatalf("read engine source: %v", err)
	}
	body := string(source)
	index := strings.Index(body, "assemblyParams := synthesisAssemblyParams{")
	if index < 0 {
		t.Fatal("the assembly params literal has moved; this call-site pin is watching nothing")
	}
	end := strings.Index(body[index:], "\n\t}")
	if end < 0 {
		t.Fatal("could not delimit the assembly params literal")
	}
	literal := body[index : index+end]
	if !strings.Contains(literal, "Plan:") {
		t.Error("engine.go's synthesisAssemblyParams literal does not set Plan: every allocation is then " +
			"unbounded and narration silently returns to charging the static contract caps (CHAOS-5008), " +
			"with every unit test still green because they drive AllocateItems directly")
	}

	// And the allocator must be reached from the assembly path, not merely
	// exist: a composer nobody calls allocates nothing.
	assembly, err := os.ReadFile("chaos4636_synthesis_assembly.go")
	if err != nil {
		t.Fatalf("read assembly source: %v", err)
	}
	if !strings.Contains(string(assembly), "AllocateItems(params.Plan") {
		t.Error("the assembly path does not call AllocateItems(params.Plan, ...): the quota is computed nowhere")
	}
}

// TestNarrationTelemetryNamesWhichBudgetBoundedIt is the CONSUMER test for
// CHAOS-5008, and the reason the counts are not sufficient on their own.
//
// A narration bounded by the item budget and one bounded by the static contract
// caps can emit IDENTICAL counts on a small cohort. Without a field naming
// which applied, a regression to the caps would be invisible in the run's own
// artifacts -- which is the diagnosability bar, not a nice-to-have.
func TestNarrationTelemetryNamesWhichBudgetBoundedIt(t *testing.T) {
	t.Parallel()
	ranked, citations := manyRankedMembersCohort(t, 6)
	plan := groupedAllocationPlan(30)

	_, _, bounded := narrateCohortDriverJudgments(ranked, nil, 0, citations, AllocateItems(plan, 0, len(ranked.Members)))
	if bounded.Allocator != CohortDriverNarrationAllocatorPlanBudget {
		t.Errorf("Allocator = %q, want %q: a narration bounded by the plan's item budget must say so",
			bounded.Allocator, CohortDriverNarrationAllocatorPlanBudget)
	}
	if bounded.AllocatedItems <= 0 {
		t.Errorf("AllocatedItems = %d, want the published narration budget", bounded.AllocatedItems)
	}

	// The other arm, which is legitimate rather than a defect: an unbounded
	// plan leaves the static caps as the only bound, and that must be
	// DISTINGUISHABLE rather than reported as if the budget had applied.
	_, _, unbounded := narrateCohortDriverJudgments(ranked, nil, 0, citations, ItemAllocation{})
	if unbounded.Allocator != CohortDriverNarrationAllocatorStaticCaps {
		t.Errorf("Allocator = %q on an unbounded plan, want %q", unbounded.Allocator, CohortDriverNarrationAllocatorStaticCaps)
	}
}

// TestNarrationAllocatorReachesTheEmittedLine asserts the LOG LINE, not the
// struct field. The key is the interface: an operator filter, a dashboard and
// an alert all name it in text, so a rename breaks every one of them silently
// and nothing reading the struct can see it.
func TestNarrationAllocatorReachesTheEmittedLine(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buf, nil)))

	telemetry.RecordCohortDriverNarration(context.Background(), storage.Principal{OrgID: "org_1"},
		CohortDriverNarrationEvent{
			Outcome:        CohortDriverNarrationEmitted,
			Allocator:      CohortDriverNarrationAllocatorPlanBudget,
			AllocatedItems: 9,
		})

	line := buf.String()
	for _, want := range []string{"narration_allocator=plan_budget", "narration_allocated_items=9"} {
		if !strings.Contains(line, want) {
			t.Errorf("emitted line does not carry %q -- a rename breaks every operator filter silently.\nline: %s", want, line)
		}
	}
}

// TestNarrationAllocatorFailsClosedOnAnUnknownValue pins the same fail-closed
// posture every other closed enum in the telemetry file takes: a value outside
// the vocabulary is reported as unclassified, never emitted verbatim.
func TestNarrationAllocatorFailsClosedOnAnUnknownValue(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buf, nil)))

	telemetry.RecordCohortDriverNarration(context.Background(), storage.Principal{OrgID: "org_1"},
		CohortDriverNarrationEvent{Allocator: CohortDriverNarrationAllocator("something_invented")})

	line := buf.String()
	if strings.Contains(line, "something_invented") {
		t.Errorf("an out-of-vocabulary allocator reached the log verbatim.\nline: %s", line)
	}
	if !strings.Contains(line, "narration_allocator=unclassified") {
		t.Errorf("line does not report the unknown allocator as unclassified.\nline: %s", line)
	}
}

// TestQuotaExposureReportsWithoutEnforcing is the boundary ruling made
// observable: the seam MEASURES per-group usage and reports it, and does
// nothing else. S7c decides what happens when a quota is blown, for every
// shape; PR2B exists so that decision has numbers to make.
func TestQuotaExposureReportsWithoutEnforcing(t *testing.T) {
	t.Parallel()
	member := SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "A"}
	groupOne := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_one", Label: "One"}
	groupTwo := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_two", Label: "Two"}

	result := InvestigationResult{
		Cohort: &Cohort{
			Members: []CohortMember{{Subject: member}},
			Groups: []contractsv1.ContextFabricCohortGroup{
				{Subject: groupOne, MemberCanonicalIDs: []string{"project_a"}},
				{Subject: groupTwo, MemberCanonicalIDs: []string{"project_a"}},
			},
		},
	}
	// Far more group-attributed drivers than any small quota can hold.
	for i := 0; i < 24; i++ {
		result.Drivers = append(result.Drivers, DriverJudgment{
			DriverID: "d", AffectedSubjects: []SubjectRef{groupOne},
		})
	}

	before := len(result.Drivers)
	allocation := AllocateItems(groupedAllocationPlan(30), 2, 1)
	exposure := exposeQuota(allocation, result)

	if exposure.Groups != 2 || exposure.ItemsPerGroup != allocation.ItemsPerGroup {
		t.Fatalf("exposure = %+v, want it to carry the allocator's own published quota", exposure)
	}
	if exposure.OverQuota <= 0 {
		t.Fatalf("OverQuota = %d with %d group-attributed drivers against a quota of %d: "+
			"the exposure is not measuring", exposure.OverQuota, before, allocation.ItemsPerGroup)
	}
	if exposure.OverQuota > exposure.Groups {
		t.Fatalf("OverQuota = %d exceeds the group count %d", exposure.OverQuota, exposure.Groups)
	}
	// THE BOUNDARY: nothing was removed, nothing was disclosed.
	if len(result.Drivers) != before {
		t.Errorf("exposeQuota mutated the result (%d drivers, was %d): enforcement belongs to S7c", len(result.Drivers), before)
	}
	if len(result.Limitations) != 0 || result.Coverage.Partial {
		t.Error("exposeQuota wrote a disclosure: the outcome set is the single disclosure authority, and it is S7c's")
	}
}

// TestZeroOverQuotaIsAnAnswerNotAnAbsentQuota keeps the two readings distinct.
// "Every group fitted" and "there was no quota" are different facts, and an
// enforcement layer that confused them would act on the wrong one.
func TestZeroOverQuotaIsAnAnswerNotAnAbsentQuota(t *testing.T) {
	t.Parallel()
	group := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_one"}
	fits := InvestigationResult{Cohort: &Cohort{Groups: []contractsv1.ContextFabricCohortGroup{{Subject: group}}}}

	withQuota := exposeQuota(AllocateItems(groupedAllocationPlan(30), 1, 1), fits)
	if withQuota.Groups != 1 || withQuota.OverQuota != 0 {
		t.Fatalf("a fitting grouped answer = %+v, want Groups=1 OverQuota=0", withQuota)
	}
	noQuota := exposeQuota(ItemAllocation{}, fits)
	if noQuota.Groups != 0 || noQuota.ItemsPerGroup != 0 {
		t.Fatalf("an unbounded plan = %+v, want an absent quota (Groups=0), not a quota of zero", noQuota)
	}
}

// TestBothStageThreeArmsPassTheQuotaIn pins the CALL SITES. Every test above
// drives exposeQuota directly, so dropping the allocation argument at either
// arm -- which silently makes every exposure empty and leaves S7c with nothing
// to act on -- would survive all of them.
func TestBothStageThreeArmsPassTheQuotaIn(t *testing.T) {
	source, err := os.ReadFile("chaos4636_budget_stage3.go")
	if err != nil {
		t.Fatalf("read stage-3 source: %v", err)
	}
	body := string(source)
	if got := strings.Count(body, "planCandidateNarrowing("); got != 2 {
		t.Fatalf("found %d planCandidateNarrowing call sites, want the two documented stage-3 arms", got)
	}
	if got := strings.Count(body, "AllocateItems(*plan,"); got != 2 {
		t.Errorf("only %d of the 2 stage-3 arms pass an allocation into planCandidateNarrowing: "+
			"the quota reaches enforcement at one arm and not the other, so a refusal on the silent arm "+
			"carries no per-group numbers at all", got)
	}
}
