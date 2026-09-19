package graphrank

import (
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func floorRowCandidate(kind contextfabric.SubjectKind, id string, confidence float64, mechanisms ...contextfabric.MatchMechanism) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		Subject:         contextfabric.SubjectRef{Kind: kind, CanonicalID: id},
		Confidence:      confidence,
		MatchMechanisms: mechanisms,
	}
}

// The trace row is the only place a refusal is explained: every field must
// carry its own value, and the mechanisms are joined in canonical order,
// de-duplicated, with "+" between them.
func TestOfferFloorRowCarriesEveryFieldAndJoinsMechanisms(t *testing.T) {
	candidate := floorRowCandidate(contextfabric.SubjectTeam, "team-1", 0.61,
		contextfabric.MatchLexical, contextfabric.MatchExact, contextfabric.MatchLexical)
	row := offerFloorRow(candidate, OfferAdmittedIdentity)
	want := OfferFloorRow{
		Kind: string(contextfabric.SubjectTeam), CanonicalID: "team-1",
		Mechanisms: "exact+lexical", Confidence: 0.61, Admission: OfferAdmittedIdentity,
	}
	if row != want {
		t.Fatalf("row = %+v, want %+v", row, want)
	}
	single := offerFloorRow(floorRowCandidate(contextfabric.SubjectTeam, "team-1", 0.5, contextfabric.MatchLexical), OfferRefusedAtOrBelowFloor)
	if single.Mechanisms != "lexical" || single.Admission != OfferRefusedAtOrBelowFloor {
		t.Fatalf("single-mechanism row = %+v", single)
	}
	three := offerFloorRow(floorRowCandidate(contextfabric.SubjectTeam, "t", 0.5,
		contextfabric.MatchProviderKey, contextfabric.MatchAlias, contextfabric.MatchExact), OfferAdmittedIdentity)
	if three.Mechanisms != "exact+alias+provider_key" {
		t.Fatalf("three-mechanism row = %q", three.Mechanisms)
	}
}

func TestOrderOfferFloorRowsOrderAndKeepsEveryRow(t *testing.T) {
	rows := []OfferFloorRow{
		{Kind: "team", CanonicalID: "b", Confidence: 0.6},
		{Kind: "repository", CanonicalID: "z", Confidence: 0.6},
		{Kind: "team", CanonicalID: "a", Confidence: 0.6},
		{Kind: "team", CanonicalID: "hi", Confidence: 0.9},
		{Kind: "team", CanonicalID: "lo", Confidence: 0.1},
	}
	before := append([]OfferFloorRow(nil), rows...)
	got := orderOfferFloorRows(rows)
	wantIDs := []string{"hi", "z", "a", "b", "lo"}
	if len(got) != len(wantIDs) {
		t.Fatalf("len = %d", len(got))
	}
	for i, id := range wantIDs {
		if got[i].CanonicalID != id {
			t.Fatalf("order[%d] = %s, want %s (%+v)", i, got[i].CanonicalID, id, got)
		}
	}
	for i := range rows {
		if rows[i] != before[i] {
			t.Fatalf("input mutated at %d: %+v", i, rows)
		}
	}

	for _, n := range []int{19, 20, 21, 25, 150} {
		many := make([]OfferFloorRow, 0, n)
		for i := 0; i < n; i++ {
			many = append(many, OfferFloorRow{Kind: "team", CanonicalID: fmt.Sprintf("id%03d", i), Confidence: 0.7})
		}
		ordered := orderOfferFloorRows(many)
		if len(ordered) != n {
			t.Fatalf("n=%d: ordered len = %d, want every row", n, len(ordered))
		}
		if ordered[len(ordered)-1].CanonicalID != fmt.Sprintf("id%03d", n-1) {
			t.Fatalf("n=%d: last = %s", n, ordered[len(ordered)-1].CanonicalID)
		}
	}
}

func TestOfferFloorCandidateLinesFormat(t *testing.T) {
	lines := offerFloorCandidateLines([]OfferFloorRow{
		{Kind: "team", CanonicalID: "low", Mechanisms: "lexical", Confidence: 0.55, Admission: OfferRefusedAtOrBelowFloor},
		{Kind: "repository", CanonicalID: "hi", Mechanisms: "exact+alias", Confidence: 0.8, Admission: OfferAdmittedIdentity},
	})
	want := []string{"repository|hi|exact+alias|0.8000|identity", "team|low|lexical|0.5500|at_or_below_floor"}
	if len(lines) != len(want) || lines[0] != want[0] || lines[1] != want[1] {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
}
