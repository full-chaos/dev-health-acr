package contextfabric

import (
	"encoding/json"
	"testing"
)

func orderTestFact(scope, day string, backlog int64) CanonicalFact {
	return CanonicalFact{
		Kind: FactReadiness, Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "team:T1"},
		Fields: map[string]FactValue{
			"work_scope_id": StringFactValue(scope), "day": StringFactValue(day), "backlog_size": IntegerFactValue(backlog),
		},
		EvidenceRefIDs: []string{"acr:v1:team:T1"}, SourceState: SourceAvailable, Source: "devhealthfacts.readiness", SourceVersion: "v1",
	}
}

func permutations(n int) [][]int {
	if n == 0 {
		return [][]int{{}}
	}
	var out [][]int
	for _, tail := range permutations(n - 1) {
		for position := 0; position <= len(tail); position++ {
			next := append(append(append([]int{}, tail[:position]...), n-1), tail[position:]...)
			out = append(out, next)
		}
	}
	return out
}

// TestSortCanonicalFactsIsATotalOrderOverFactsThatShareKindSubjectAndSource
// serves four facts that share kind, subject and source in every possible
// order and requires one resulting sequence.
func TestSortCanonicalFactsIsATotalOrderOverFactsThatShareKindSubjectAndSource(t *testing.T) {
	base := []CanonicalFact{
		orderTestFact("scope-a", "2026-10-03", 0), orderTestFact("scope-b", "2026-10-03", 1),
		orderTestFact("scope-b", "2026-08-07", 1), orderTestFact("scope-c", "2026-10-04", 5),
	}
	var want string
	for _, order := range permutations(len(base)) {
		facts := make([]CanonicalFact, 0, len(base))
		for _, index := range order {
			facts = append(facts, base[index])
		}
		sortCanonicalFacts(facts)
		encoded, err := json.Marshal(facts)
		if err != nil {
			t.Fatal(err)
		}
		if want == "" {
			want = string(encoded)
			continue
		}
		if string(encoded) != want {
			t.Fatalf("order %v gives a different sequence:\n%s\nwant\n%s", order, encoded, want)
		}
	}
}

// TestSortCanonicalFactsKeepsTheKindSubjectSourceOrderBeforeTheTieBreak pins
// the keys the tie-break must not override.
func TestSortCanonicalFactsKeepsTheKindSubjectSourceOrderBeforeTheTieBreak(t *testing.T) {
	late := orderTestFact("scope-a", "2026-10-03", 0)
	late.Source = "z-source"
	early := orderTestFact("scope-z", "2026-10-04", 9)
	early.Source = "a-source"
	other := orderTestFact("scope-a", "2026-10-03", 0)
	other.Subject.CanonicalID = "team:T0"
	status := orderTestFact("scope-a", "2026-10-03", 0)
	status.Kind = FactStatus
	facts := []CanonicalFact{late, early, other, status}
	sortCanonicalFacts(facts)
	got := []string{string(facts[0].Kind) + "/" + facts[0].Subject.CanonicalID + "/" + facts[0].Source, string(facts[1].Kind) + "/" + facts[1].Subject.CanonicalID + "/" + facts[1].Source,
		string(facts[2].Kind) + "/" + facts[2].Subject.CanonicalID + "/" + facts[2].Source, string(facts[3].Kind) + "/" + facts[3].Subject.CanonicalID + "/" + facts[3].Source}
	want := []string{"status/team:T1/devhealthfacts.readiness", "readiness/team:T0/devhealthfacts.readiness", "readiness/team:T1/a-source", "readiness/team:T1/z-source"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d = %s, want %s (all: %v)", i, got[i], want[i], got)
		}
	}
}
