package genkitruntime

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestInterpretationPromptScopedMemberRulesStatedOnce(t *testing.T) {
	t.Parallel()
	for _, rule := range []string{
		`Subject expression rules. Where two rules below fit one question, the rule listed first wins:`,
		`A count of the members of a named parent ("how many pull requests did the X team merge") is children_of_scope.`,
		`A trend of a measure over the members of a named parent, where the question names the member noun, is children_of_scope too.`,
		`Incidents, pull requests, deployments and work items of a named team, project or repository are its members when the question lists, counts, trends or filters them.`,
		`A question about the parent itself on that topic ("why does repository X have so many incidents") is named_subject.`,
		`member_kind is the kind that the question lists, counts, trends or ranks under the parent.`,
		`A noun that names only the topic or the condition of those members names fact kinds, not member_kind: in "the X team's repositories with failed deployments" member_kind is repository.`,
		`A review state of a pull request (approved, changes requested, no review yet, stuck in review) is a state condition too, and so is "behind".`,
		`A creation time is not a state.`,
		`member_qualifier is "assignee" when the question selects work items by who they are assigned to ("assigned to the X team", "on the X team's plate"): the named team is the anchor and member_kind is work_item.`,
		`Worked examples for the members of a named parent, as question -> question_frame.subject_expression.`,
	} {
		if got := strings.Count(interpretationSystemPrompt, rule); got != 1 {
			t.Errorf("rule %q appears %d times, want exactly 1", rule, got)
		}
	}
}

// "The rule listed first wins" decides a question only through the order of
// the lines, so the order is pinned: the count rule comes before the share
// rule, and both come after the precedence sentence.
func TestInterpretationPromptListsTheCountRuleBeforeTheShareRule(t *testing.T) {
	t.Parallel()
	precedence := strings.Index(interpretationSystemPrompt, `Where two rules below fit one question, the rule listed first wins:`)
	count := strings.Index(interpretationSystemPrompt, `- A count of the members of a named parent (`)
	share := strings.Index(interpretationSystemPrompt, `- A share or amount of one named subject's members (`)
	if precedence < 0 || count < 0 || share < 0 {
		t.Fatalf("precedence sentence at %d, count rule at %d, share rule at %d: all three must be in the prompt", precedence, count, share)
	}
	if !(precedence < count && count < share) {
		t.Errorf("order is precedence %d, count %d, share %d; want precedence < count < share", precedence, count, share)
	}
	if got := strings.Count(interpretationSystemPrompt, "the rule listed first wins"); got != 2 {
		t.Errorf("%d precedence sentences, want 2 (the time rules and the subject expression rules)", got)
	}
}

func TestInterpretationPromptDropsTheUnorderedCountSentence(t *testing.T) {
	t.Parallel()
	for _, gone := range []string{
		"Subject expression rules:\n",
		"A count of the members of a named parent is children_of_scope too.",
	} {
		if strings.Contains(interpretationSystemPrompt, gone) {
			t.Errorf("prompt still carries %q", gone)
		}
	}
}

func TestInterpretationScopedMemberRulesNameOnlyVocabularyKinds(t *testing.T) {
	t.Parallel()
	for _, named := range []struct{ rule, kind string }{
		{`"ticket" says work_item`, "work_item"},
		{`"repo" says repository`, "repository"},
		{`member_kind is repository`, "repository"},
		{`the named team is the anchor and member_kind is work_item`, "work_item"},
	} {
		if !contractsv1.ValidContextFabricSubjectKind(contractsv1.ContextFabricSubjectKind(named.kind)) {
			t.Errorf("kind %q is not in the closed subject-kind vocabulary", named.kind)
		}
		if got := strings.Count(interpretationSystemPrompt, named.rule); got != 1 {
			t.Errorf("rule %q appears %d times, want 1", named.rule, got)
		}
	}
}
