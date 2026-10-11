package contextpacket

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func itemsWithRules(ids ...string) []contractsv1.ContextPacketItem {
	items := make([]contractsv1.ContextPacketItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, contractsv1.ContextPacketItem{RuleID: id})
	}
	return items
}

func assertNoRepeatedActionText(t *testing.T, checks []contractsv1.RequiredCheck, steps []contractsv1.RecommendedStep) {
	t.Helper()
	labels, reasons := map[string]bool{}, map[string]bool{}
	for _, c := range checks {
		if labels["c"+c.Label] || reasons["c"+c.Reason] {
			t.Fatalf("repeated check text: %+v", checks)
		}
		labels["c"+c.Label], reasons["c"+c.Reason] = true, true
	}
	for _, s := range steps {
		if labels["s"+s.Label] || reasons["s"+s.Reason] {
			t.Fatalf("repeated step text: %+v", steps)
		}
		labels["s"+s.Label], reasons["s"+s.Reason] = true, true
	}
}

func TestActionsForGivesEachRuleItsOwnLabels(t *testing.T) {
	checks, steps := actionsFor(itemsWithRules("evidence.observed.reference.v1", "evidence.observed.cause.v1"))
	if len(checks) != 2 || len(steps) != 2 {
		t.Fatalf("want one check and step per rule: %+v %+v", checks, steps)
	}
	assertNoRepeatedActionText(t, checks, steps)
	if checks[0].CheckID != "check.evidence.observed.cause.v1" || checks[0].Label != "Validate retrieved evidence for the cause rule" {
		t.Fatalf("cause check = %+v", checks[0])
	}
}

func TestEveryBuiltInRuleHasASubject(t *testing.T) {
	for _, entity := range []string{"check", "commit", "recommendation", "state", "other"} {
		ref := contractsv1.EvidenceRef{}
		ref.Source.EntityType = entity
		id := evidenceCategoryRule(ref).id
		if _, ok := ruleSubjects[id]; !ok {
			t.Fatalf("rule %q has no entry in ruleSubjects", id)
		}
	}
	if len(ruleSubjects) != 5 {
		t.Fatalf("ruleSubjects has %d entries, want the 5 built-in rules", len(ruleSubjects))
	}
}

func TestActionsForExplicitRuleIDsStayDistinct(t *testing.T) {
	checks, steps := actionsFor(itemsWithRules("x.one.v1", "x.two.v1", "evidence.observed.cause.v1"))
	if len(checks) != 3 || len(steps) != 3 {
		t.Fatalf("want 3 each: %+v %+v", checks, steps)
	}
	assertNoRepeatedActionText(t, checks, steps)
}

func TestActionsForEmptyItemsEmitsNothing(t *testing.T) {
	checks, steps := actionsFor(nil)
	if len(checks) != 0 || len(steps) != 0 {
		t.Fatalf("want none: %+v %+v", checks, steps)
	}
}
