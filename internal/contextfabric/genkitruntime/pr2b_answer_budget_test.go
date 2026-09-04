package genkitruntime

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
	if payload.AnswerBudget.Groups != allocation.Groups {
		t.Errorf("groups = %d, allocator published %d", payload.AnswerBudget.Groups, allocation.Groups)
	}
	// EVERY bucket the model can write into must reach the payload. The
	// member pool is here because review found the allocator had none at
	// all, so member-attributed drivers and claims were unbudgeted.
	if payload.AnswerBudget.Global != allocation.Pool(contractsv1.ContextFabricItemBucketGlobal) {
		t.Errorf("global = %d, allocator published %d",
			payload.AnswerBudget.Global, allocation.Pool(contractsv1.ContextFabricItemBucketGlobal))
	}
	if payload.AnswerBudget.PerMember != allocation.Pool(contractsv1.ContextFabricItemBucketMember) {
		t.Errorf("per_member = %d, allocator published %d",
			payload.AnswerBudget.PerMember, allocation.Pool(contractsv1.ContextFabricItemBucketMember))
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
		"answer_budget.per_member",
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
	// ONE derivation, and that is ALL this test still asserts. Two
	// AllocateItems calls in that function would be two authorities over one
	// number -- the defect the allocator exists to remove -- and "how many
	// times is this derived" has no behavioural expression, so reading the
	// source is the right tool for it.
	//
	// TWO source greps were REMOVED from here in the round-three class sweep,
	// because both asserted things a run can show:
	//
	//   - "the projection reads input.Allocation" is what
	//     TestThePayloadStatesTheAllocatorsOwnNumber above already proves, by
	//     calling synthesisInputFromDomain and comparing the payload against
	//     the allocator's own numbers.
	//   - "the engine sets Allocation:" is proved by
	//     TestTheAllocationTheEngineHandsSynthesisIsThePlansOwn in
	//     internal/contextfabric, which reads the SynthesisInput the ENGINE
	//     built and is mutation-proved by deleting `Plan: plan` from
	//     engine.go.
	//
	// Counted through the AST, not by counting substrings: a substring count
	// cannot tell code from a comment, so commenting a derivation out would
	// leave the count unchanged and this pin green while the call was gone.
	const assembly = "../chaos4636_synthesis_assembly.go"
	file, err := parser.ParseFile(token.NewFileSet(), assembly, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", assembly, err)
	}
	derivations := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "AllocateItems" {
			derivations++
		}
		return true
	})
	if derivations != 1 {
		t.Errorf("synthesis assembly derives the allocation %d times, want exactly 1: a second derivation is a second authority", derivations)
	}
}
