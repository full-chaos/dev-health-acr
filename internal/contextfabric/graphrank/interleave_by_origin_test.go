package graphrank

import "testing"

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

func TestRankEdgesByOriginKeepsConfidenceBeforeTurns(t *testing.T) {
	high, low := 0.9, 0.1
	edges := []CandidateEdge{
		{UUID: "a1", Score: &low}, {UUID: "a2", Score: &low}, {UUID: "b1", Score: &low},
		{UUID: "b2", Score: &high}, {UUID: "a3", Score: &high},
	}
	originOf := map[string]string{"a1": "A", "a2": "A", "a3": "A", "b1": "B", "b2": "B"}
	got := RankEdgesByOrigin(edges, originOf, []string{"A", "B"})
	want := []string{"a3", "b2", "a1", "b1", "a2"}
	for i := range want {
		if got[i].UUID != want[i] {
			t.Fatalf("position %d: %s, want %s (order %v)", i, got[i].UUID, want[i], got)
		}
	}
}
