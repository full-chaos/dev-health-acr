package genkitruntime

import (
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The answer_budget payload, asserted at the REAL prompt-serialization
// boundary -- the same BuildSynthesisPrompt path Runtime.SynthesizeAnswer uses.
//
// These exist because of a review finding, and the finding is worth stating
// plainly: `items_per_group` was a plain int with `omitempty`, so a grouped
// answer whose per-group allowance came to ZERO serialized with no
// `items_per_group` at all -- byte-identical, on the wire, to an answer with no
// group axis. Worse, `groups: 1` was still emitted beside it, so the model was
// shown a group axis and no allowance for it, and the prompt paragraph that
// promises "answer_budget.items_per_group is how many may be about each group"
// became FALSE for exactly the answers where the budget bites hardest.
//
// The distinction is the same one the enforcement side already keeps as
// `unavailable` (no group axis) versus `bounded_zero` (a group axis whose
// allowance is zero, so every group item is over budget). It was kept in the
// availability vocabulary, kept again in modelFacingAnswerBudget's nil-versus-
// zeroed-struct decision, and broken in the field tag one level below both.

// budgetInputWith returns a synthesis input carrying the allocation produced by
// the REAL allocator for this ceiling, group count and member-row count.
//
// The allocation is never hand-built: the numbers the prompt shows come from
// allocator METHODS by design, and a fixture that wrote its own would be
// testing a second derivation rather than the one that ships.
func budgetInputWith(maxItems, groups, memberRows int) contextfabric.SynthesisInput {
	input := validSynthesisInput()
	input.Allocation = contextfabric.AllocateItems(
		contextfabric.AnswerPlan{Budget: contextfabric.AnswerPlanBudget{MaxItems: maxItems}},
		groups, memberRows,
	)
	return input
}

// TestAGroupedZeroAllowanceIsSerializedAsAnExplicitZero is the finding, pinned.
func TestAGroupedZeroAllowanceIsSerializedAsAnExplicitZero(t *testing.T) {
	t.Parallel()
	// A ceiling this tight leaves nothing per group, which is a real
	// instruction and not an absence: every group item is over budget.
	input := budgetInputWith(1, 1, 1)

	// PREMISES, asserted rather than assumed. A fixture that quietly stopped
	// producing a grouped zero would make every assertion below pass on the
	// wrong state.
	if !input.Allocation.InForce() {
		t.Fatal("fixture allocation is not in force, so no answer_budget is emitted at all")
	}
	if input.Allocation.Groups <= 0 {
		t.Fatalf("fixture has %d groups, want a real group axis", input.Allocation.Groups)
	}
	if got := input.Allocation.GroupAllowance(); got != 0 {
		t.Fatalf("fixture group allowance = %d, want 0 -- this test is about the ZERO case", got)
	}

	prompt, err := BuildSynthesisPrompt(input, 512<<10)
	if err != nil {
		t.Fatalf("BuildSynthesisPrompt() error = %v", err)
	}

	if !strings.Contains(prompt, `"items_per_group":0`) {
		t.Errorf("the production prompt boundary carries no explicit items_per_group zero for a grouped "+
			"answer whose allowance IS zero -- the model cannot tell it from an answer with no "+
			"groups.\nprompt: %s", prompt)
	}
	// And the field it is paired with is present, which is what made the
	// omission incoherent rather than merely lossy.
	if !strings.Contains(prompt, `"groups":1`) {
		t.Errorf("the prompt carries no groups count beside the allowance.\nprompt: %s", prompt)
	}
}

// TestAnAnswerWithNoGroupAxisOmitsTheGroupFieldsEntirely is the other half, and
// it is what keeps the fix from becoming "always emit zero".
//
// Absence must stay absent. If an ungrouped answer emitted `items_per_group: 0`
// the model would be told every group item is over budget for an answer that
// has no groups at all -- the same conflation in the opposite direction.
func TestAnAnswerWithNoGroupAxisOmitsTheGroupFieldsEntirely(t *testing.T) {
	t.Parallel()
	input := budgetInputWith(60, 0, 2)
	if input.Allocation.Groups != 0 {
		t.Fatalf("fixture has %d groups, want none", input.Allocation.Groups)
	}

	prompt, err := BuildSynthesisPrompt(input, 512<<10)
	if err != nil {
		t.Fatalf("BuildSynthesisPrompt() error = %v", err)
	}
	if strings.Contains(prompt, "items_per_group") {
		t.Errorf("an answer with no group axis still carries items_per_group.\nprompt: %s", prompt)
	}
	if strings.Contains(prompt, `"groups":`) {
		t.Errorf("an answer with no group axis still carries a groups count.\nprompt: %s", prompt)
	}
	// The non-group allowances are still published, so this is not passing
	// by emitting no budget at all.
	if !strings.Contains(prompt, `"per_member":`) || !strings.Contains(prompt, `"global":`) {
		t.Errorf("the ungrouped answer lost its global/per_member allowances too.\nprompt: %s", prompt)
	}
}

// TestAPositiveGroupAllowanceReachesThePromptUnchanged is the ordinary case,
// present so the two edge assertions above cannot both be satisfied by a field
// that is simply always absent or always zero.
func TestAPositiveGroupAllowanceReachesThePromptUnchanged(t *testing.T) {
	t.Parallel()
	input := budgetInputWith(120, 2, 2)
	allowance := input.Allocation.GroupAllowance()
	if allowance <= 0 {
		t.Fatalf("fixture group allowance = %d, want a positive one", allowance)
	}

	prompt, err := BuildSynthesisPrompt(input, 512<<10)
	if err != nil {
		t.Fatalf("BuildSynthesisPrompt() error = %v", err)
	}
	// The number the model is shown must be the allocator's own derivation,
	// not a second one computed here or at the prompt site.
	want := `"items_per_group":` + strconv.Itoa(allowance)
	if !strings.Contains(prompt, want) {
		t.Errorf("the prompt does not carry %s -- the published allowance disagrees with "+
			"GroupAllowance().\nprompt: %s", want, prompt)
	}
}

// TestNoBudgetInForceEmitsNoAnswerBudgetAtAll keeps the outermost distinction:
// an unbounded answer has no quota to state, and a zeroed budget object would
// tell the model to write nothing at all.
func TestNoBudgetInForceEmitsNoAnswerBudgetAtAll(t *testing.T) {
	t.Parallel()
	input := budgetInputWith(0, 2, 2)
	if input.Allocation.InForce() {
		t.Fatal("fixture allocation is in force, want an unbounded one")
	}
	prompt, err := BuildSynthesisPrompt(input, 512<<10)
	if err != nil {
		t.Fatalf("BuildSynthesisPrompt() error = %v", err)
	}
	if strings.Contains(prompt, "answer_budget") {
		t.Errorf("an unbounded answer carries an answer_budget: a zeroed budget tells the model to "+
			"write nothing, which is not what no ceiling means.\nprompt: %s", prompt)
	}
}
