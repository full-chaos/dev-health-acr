package directread

import (
	"reflect"
	"testing"
)

// CHAOS-12819: the order two refused candidates are compared in, stated once
// on preferredOver: the deeper stage, then a person refusal (personWinsTie,
// T13), then the candidate whose pins the query keeps, then the earlier one.
func TestRefusedCandidatesAreComparedInTheStatedOrder(t *testing.T) {
	refusal := func(code RefusalCode) *Refusal { return &Refusal{Code: code, Reason: string(code)} }
	other, person := refusal(RefusalVariableNotAllowed), refusal(RefusalPersonScopeNotServed)
	for _, c := range []struct {
		label     string
		candidate candidateOutcome
		best      candidateOutcome
		want      bool
	}{
		{"deeper stage wins over a person refusal and kept pins", candidateOutcome{refusal: other, stage: 5}, candidateOutcome{refusal: person, stage: 4, keepsPins: true}, true},
		{"shallower stage loses to anything", candidateOutcome{refusal: person, stage: 3, keepsPins: true}, candidateOutcome{refusal: other, stage: 4}, false},
		{"on a tie a person refusal wins over kept pins", candidateOutcome{refusal: person, stage: 4}, candidateOutcome{refusal: other, stage: 4, keepsPins: true}, true},
		{"on a tie kept pins do not beat a person refusal", candidateOutcome{refusal: other, stage: 4, keepsPins: true}, candidateOutcome{refusal: person, stage: 4}, false},
		{"on a tie kept pins win", candidateOutcome{refusal: other, stage: 4, keepsPins: true}, candidateOutcome{refusal: other, stage: 4}, true},
		{"on a full tie the earlier candidate keeps it", candidateOutcome{refusal: other, stage: 4, keepsPins: true}, candidateOutcome{refusal: other, stage: 4, keepsPins: true}, false},
		{"on a tie with no pins kept the earlier candidate keeps it", candidateOutcome{refusal: other, stage: 4}, candidateOutcome{refusal: other, stage: 4}, false},
		{"two person refusals: the earlier keeps it", candidateOutcome{refusal: person, stage: 4, keepsPins: true}, candidateOutcome{refusal: person, stage: 4}, true},
	} {
		t.Run(c.label, func(t *testing.T) {
			if got := c.candidate.preferredOver(&c.best); got != c.want {
				t.Fatalf("preferredOver = %v, want %v", got, c.want)
			}
		})
	}
	if !(&candidateOutcome{refusal: other, stage: 1}).preferredOver(nil) {
		t.Fatal("the first refused candidate must be taken")
	}
}

// Pins are the enum values that tell a root's candidates apart: on the
// analytics root the scope level of each investment entry. A root with one
// candidate has none, and a one-value enum the siblings share is not a pin.
func TestPinsAreTheVariablesThatTellARootsCandidatesApart(t *testing.T) {
	policy, err := DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	analytics, ok := policy.Root("analytics")
	if !ok {
		t.Fatal("no analytics root")
	}
	want := map[string]map[string]string{
		"investmentBreakdown":           {"batch.filters.scope.level": "ORG"},
		"investmentFull":                {"batch.filters.scope.level": "ORG"},
		"investmentBreakdownTeam":       {"batch.filters.scope.level": "TEAM"},
		"investmentBreakdownRepository": {"batch.filters.scope.level": "REPO"},
	}
	got := map[string]map[string]string{}
	for _, cand := range analytics.candidates {
		got[cand.op.Name] = cand.pins
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("analytics pins %v, want %v", got, want)
	}
	for _, root := range policy.Roots() {
		if len(root.candidates) != 1 {
			continue
		}
		if pins := root.candidates[0].pins; len(pins) != 0 {
			t.Fatalf("root %s has one candidate and pins %v", root.Field, pins)
		}
		if root.candidates[0].keepsPins(map[string]any{}) {
			t.Fatalf("root %s: a candidate with no pins must keep none", root.Field)
		}
	}
	// A query that leaves the scope out keeps the ORG pin through the rule's
	// default, and not the TEAM pin.
	byName := map[string]*rootCandidate{}
	for _, cand := range analytics.candidates {
		byName[cand.op.Name] = cand
	}
	noScope := map[string]any{"batch": map[string]any{"breakdowns": []any{}}}
	team := map[string]any{"batch": map[string]any{"filters": map[string]any{"scope": map[string]any{"level": "TEAM"}}}}
	for _, c := range []struct {
		op   string
		vars map[string]any
		want bool
	}{
		{"investmentBreakdown", noScope, true},
		{"investmentBreakdownTeam", noScope, false},
		{"investmentBreakdown", team, false},
		{"investmentBreakdownTeam", team, true},
	} {
		if got := byName[c.op].keepsPins(c.vars); got != c.want {
			t.Fatalf("%s keepsPins(%v) = %v, want %v", c.op, c.vars, got, c.want)
		}
	}
}

// A one-value enum is a pin only where siblings pin the same path to another
// value: two candidates that both admit mode X alone are not told apart by it,
// and a not-allowed or non-enum rule is never a pin.
func TestOnlyADifferingOneValueEnumIsAPin(t *testing.T) {
	op := func(name string, rules ...VariableRule) *rootCandidate {
		return &rootCandidate{op: &OperationPolicy{Name: name, Variables: rules}}
	}
	enum := func(path string, values ...string) VariableRule {
		return VariableRule{Path: path, Kind: VariableKindEnum, Allowed: true, AllowedValues: values}
	}
	a := op("a", enum("v.level", "ORG"), enum("v.mode", "X"), VariableRule{Path: "v.off", Kind: VariableKindEnum, AllowedValues: []string{"P"}}, VariableRule{Path: "v.id", Kind: "scalar", Allowed: true, AllowedValues: []string{"1"}})
	b := op("b", enum("v.level", "TEAM"), enum("v.mode", "X"), VariableRule{Path: "v.off", Kind: VariableKindEnum, AllowedValues: []string{"Q"}}, VariableRule{Path: "v.id", Kind: "scalar", Allowed: true, AllowedValues: []string{"2"}})
	c := op("c", enum("v.level", "REPO", "TEAM"), enum("v.mode", "X"))
	assignPins(&GraphQLRootPolicy{Field: "r", candidates: []*rootCandidate{a, b, c}})
	for _, w := range []struct {
		cand *rootCandidate
		want map[string]string
	}{
		{a, map[string]string{"v.level": "ORG"}},
		{b, map[string]string{"v.level": "TEAM"}},
		{c, nil},
	} {
		if !reflect.DeepEqual(w.cand.pins, w.want) {
			t.Fatalf("%s pins %v, want %v", w.cand.op.Name, w.cand.pins, w.want)
		}
	}
}
