package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestInterleaveByOriginTakesTurnsAndKeepsTheBudget(t *testing.T) {
	ranked := []CandidateEdge{{UUID: "a1"}, {UUID: "a2"}, {UUID: "a3"}, {UUID: "b1"}, {UUID: "c1"}, {UUID: "x1"}}
	originOf := map[string]string{"a1": "A", "a2": "A", "a3": "A", "b1": "B", "c1": "C", "x1": "X"}
	got := InterleaveByOrigin(ranked, originOf, []string{"B", "A", "C", "A"})
	want := []string{"b1", "a1", "c1", "a2", "a3", "x1"}
	if len(got) != len(want) {
		t.Fatalf("got %d edges, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].UUID != want[i] {
			t.Fatalf("position %d: %s, want %s (order %v)", i, got[i].UUID, want[i], got)
		}
	}
	if out := InterleaveByOrigin(nil, originOf, []string{"A"}); len(out) != 0 {
		t.Fatalf("no edges: got %v", out)
	}
}

func TestRankEdgesByOriginOrdersEachOriginByRelevanceThenTakesTurns(t *testing.T) {
	high, low := 0.9, 0.1
	edges := []CandidateEdge{
		{UUID: "a1", Score: &low}, {UUID: "a2", Score: &low}, {UUID: "b1", Score: &low},
		{UUID: "b2", Score: &high}, {UUID: "a3", Score: &high},
	}
	originOf := map[string]string{"a1": "A", "a2": "A", "a3": "A", "b1": "B", "b2": "B"}
	got := RankEdgesByOrigin(edges, originOf, nil, []string{"A", "B"})
	want := []string{"a3", "b2", "a1", "b1", "a2"}
	for i := range want {
		if got[i].UUID != want[i] {
			t.Fatalf("position %d: %s, want %s (order %v)", i, got[i].UUID, want[i], got)
		}
	}
}

func TestRankEdgesByOriginPutsTiersAndClassesFirst(t *testing.T) {
	high := 0.9
	edges := []CandidateEdge{
		{UUID: "t1", Score: &high}, {UUID: "c2"}, {UUID: "c1"}, {UUID: "c0"}, {UUID: "d1"},
	}
	originOf := map[string]string{"t1": "T", "c0": "C", "c1": "C", "c2": "C", "d1": "D"}
	class := map[string]int{"c0": 2, "c1": 1, "c2": 0}
	got := RankEdgesByOrigin(edges, originOf, class, []string{"C", "D"}, []string{"T"})
	want := []string{"c2", "d1", "c1", "c0", "t1"}
	for i := range want {
		if got[i].UUID != want[i] {
			t.Fatalf("position %d: %s, want %s (order %v)", i, got[i].UUID, want[i], got)
		}
	}
}

// A turn never puts one origin's lower-confidence edge ahead of another
// origin's higher one: with a budget of two, A's 0.9 and 0.8 edges are admitted
// before B's 0.1 edge.
func TestRankEdgesByOriginTakesTurnsWithinEachConfidenceRun(t *testing.T) {
	a9, a8, b1 := 0.9, 0.8, 0.1
	edges := []CandidateEdge{{UUID: "b1", Score: &b1}, {UUID: "a8", Score: &a8}, {UUID: "a9", Score: &a9}}
	originOf := map[string]string{"a9": "A", "a8": "A", "b1": "B"}
	got := RankEdgesByOrigin(edges, originOf, nil, []string{"A", "B"})
	want := []string{"a9", "a8", "b1"}
	for i := range want {
		if got[i].UUID != want[i] {
			t.Fatalf("position %d: %s, want %s (order %v)", i, got[i].UUID, want[i], got)
		}
	}
}

func TestOwnedByTeamIsNamedByAnIdentityQuestionAndMakesNoDriver(t *testing.T) {
	identity := []contextfabric.FactRequirement{{Kind: contextfabric.FactIdentity}}
	blockers := []contextfabric.FactRequirement{{Kind: contextfabric.FactBlockers}}
	if !NamedByQuestion("OWNED_BY_TEAM", identity) || NamedByQuestion("OWNED_BY_TEAM", blockers) || NamedByQuestion("OWNED_BY_TEAM", nil) {
		t.Fatal("OWNED_BY_TEAM must be named by an identity question only")
	}
	if !NamedByQuestion("BLOCKS", blockers) || NamedByQuestion("BLOCKS", identity) || NamedByQuestion("BELONGS_TO_PROJECT", identity) {
		t.Fatal("BLOCKS is named by a blockers question; an unlisted type by none")
	}
	if MakesDriver("OWNED_BY_TEAM") || !MakesDriver("BLOCKS") {
		t.Fatal("only BLOCKS makes a driver")
	}
}

// The driving relationship types are a census: an entry added to
// relationMeaningTable without drives makes no driver (the zero value), and a
// new driving type must be added here on purpose.
func TestOnlyTheListedRelationshipTypesMakeDrivers(t *testing.T) {
	driving := map[string]bool{"BLOCKS": true}
	for name, entry := range relationMeaningTable {
		if entry.drives != driving[name] {
			t.Fatalf("%s: drives=%v, census says %v", name, entry.drives, driving[name])
		}
		if MakesDriver(name) != driving[name] {
			t.Fatalf("%s: MakesDriver=%v, census says %v", name, MakesDriver(name), driving[name])
		}
	}
	for name := range driving {
		if _, ok := relationMeaningTable[name]; !ok {
			t.Fatalf("census lists %s, which the table does not have", name)
		}
	}
}
