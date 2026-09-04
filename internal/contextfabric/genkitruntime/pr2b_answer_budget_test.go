package genkitruntime

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// T6, the prompt half of the quota (S5, quota side).
//
// A number in a prompt is not an enforcement mechanism -- S7c owns
// enforcement -- but it is the only way prediction and outcome can converge
// rather than being reconciled after the fact. The model decides how many
// drivers, findings and claims to write, so a per-group quota it never sees
// can only be discovered afterwards, as an overrun.

// TestThePayloadStatesTheAllocatorsOwnNumber pins that the number the model is
// shown IS the allocator's, not a second derivation of it. Two derivations of
// one budget is how this area came to have two spenders in the first place.
func TestThePayloadStatesTheAllocatorsOwnNumber(t *testing.T) {
	t.Parallel()
	allocation := contextfabric.AllocateItems(
		contextfabric.AnswerPlan{Budget: contextfabric.AnswerPlanBudget{MaxItems: 30, SynthesisHeadroom: 20}}, 3, 4)

	payload := synthesisInputFromDomain("org_1", contextfabric.SynthesisInput{Allocation: allocation})
	if payload.AnswerBudget == nil {
		t.Fatal("answer_budget absent from the payload: the model is never told the quota it was planned against")
	}
	if payload.AnswerBudget.ItemsPerGroup != allocation.ItemsPerGroup {
		t.Errorf("items_per_group = %d, allocator published %d: the prompt states a number the allocator did not",
			payload.AnswerBudget.ItemsPerGroup, allocation.ItemsPerGroup)
	}
	if payload.AnswerBudget.Groups != allocation.Groups || payload.AnswerBudget.Global != allocation.Global {
		t.Errorf("payload budget %+v does not match the allocation %+v", payload.AnswerBudget, allocation)
	}
}

// TestAnUnbudgetedRequestShowsTheModelNoQuotaAtAll is the fail-safe direction,
// and it is the same distinction QuotaExposure keeps on the enforcement side:
// an omitted field says "no quota"; a present field full of zeros says "a
// quota of zero", and a model shown the latter has been told to write nothing.
func TestAnUnbudgetedRequestShowsTheModelNoQuotaAtAll(t *testing.T) {
	t.Parallel()
	payload := synthesisInputFromDomain("org_1", contextfabric.SynthesisInput{})
	if payload.AnswerBudget != nil {
		t.Fatalf("answer_budget = %+v on a request with no item budget, want absent", payload.AnswerBudget)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "answer_budget") {
		t.Error("the serialized payload still carries an answer_budget key with no budget in force")
	}
}

// TestTheSystemPromptExplainsTheBudgetItIsGiven pins that the number is
// accompanied by what it MEANS. A payload field with no prompt paragraph is a
// number the model has no instruction about, which is indistinguishable from
// not sending it.
func TestTheSystemPromptExplainsTheBudgetItIsGiven(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"answer_budget",
		"answer_budget.items_per_group",
		"answer_budget.global",
		// The charge rule the allocator declares must be the one the model
		// is told, or the model optimises against different arithmetic.
		"counts against each group it names",
	} {
		if !strings.Contains(synthesisSystemPrompt, want) {
			t.Errorf("the synthesis system prompt does not mention %q: the model is handed a number with no instruction about it", want)
		}
	}
	// And it must NOT be presented as a hard limit -- S7c owns enforcement,
	// and a model told this is a rejection threshold will truncate its own
	// answer rather than write a shorter well-grounded one.
	if !strings.Contains(synthesisSystemPrompt, "exceeding them does not invalidate an answer") {
		t.Error("the prompt does not say the budget is not a rejection threshold; a model told otherwise will self-truncate")
	}
}

// TestTheAllocationReachesThePayloadFromTheEngine is the CALL-SITE PIN, and
// it is deliberately written at the READ end.
//
// The round-1 lesson: the earlier pin asserted the allocation was passed IN to
// narrowing and never that the exposure was read OUT, so a value written at
// three sites and read at none passed every test. Here the risk is the mirror
// image -- synthesisInputFromDomain could stop reading input.Allocation, or
// the engine could stop setting it, and every test above would still pass
// because they construct the SynthesisInput themselves.
func TestTheAllocationReachesThePayloadFromTheEngine(t *testing.T) {
	// The projection must READ the field.
	runtime, err := os.ReadFile("runtime.go")
	if err != nil {
		t.Fatalf("read runtime source: %v", err)
	}
	if !strings.Contains(string(runtime), "modelFacingAnswerBudget(input.Allocation)") {
		t.Error("synthesisInputFromDomain does not read input.Allocation: the payload's budget would be empty on every " +
			"real request while every unit test that builds its own SynthesisInput stays green")
	}

	// And the ENGINE must set it, or the field the projection reads is
	// always zero.
	assembly, err := os.ReadFile("../chaos4636_synthesis_assembly.go")
	if err != nil {
		t.Fatalf("read assembly source: %v", err)
	}
	body := string(assembly)
	if !strings.Contains(body, "Allocation: synthesisAllocation") {
		t.Error("the engine's Synthesize call does not set Allocation: the model is never told its budget in production")
	}
	// ONE derivation. Two AllocateItems calls in this function would be two
	// authorities over one number -- the defect the allocator exists to remove.
	if got := strings.Count(body, "AllocateItems("); got != 1 {
		t.Errorf("synthesis assembly derives the allocation %d times, want exactly 1: a second derivation is a second authority", got)
	}
}
