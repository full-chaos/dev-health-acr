package falkorgraph

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// TestCHAOS3838_ExpansionOnlyLexicalHitCorroboratesAVectorHitIntoTheCommitGate
// pins the EXACT arithmetic property CHAOS-3838's whole lift theory depends
// on (team-lead ratification rider 3, 2026-08-15): a lexicon-expansion-only
// lexical find -- a candidate whose search_text contains only the SYNONYM
// text, so fulltextSearchNodes' matched-term coverage against the caller's
// ORIGINAL term is zero, landing it at the bare fulltextRelevanceFloor
// (0.50, see queries.go's fulltextSearchNodes doc comment) -- becomes a
// genuine commit-eligible signal ONLY once a second, distinct mechanism
// (a vector hit) corroborates it, via graphrank's PUBLIC API exactly as
// production calls it (CorroboratedConfidence), never a re-derivation of
// graphrank's own band math here.
//
// This test lives in falkorgraph (a CONSUMER of graphrank), deliberately
// NOT inside graphrank itself: it exists to trip LOUDLY if graphrank's own
// corroboration band ever moves in a way that silently kills the lift this
// ticket's design depends on -- CorroboratedFloor rising, or the
// mechanism-weight formula changing shape -- without this package's own
// tests ever re-deriving or duplicating that math.
func TestCHAOS3838_ExpansionOnlyLexicalHitCorroboratesAVectorHitIntoTheCommitGate(t *testing.T) {
	// The floor a lexicon-expansion-only lexical find lands at -- see
	// TestFulltextSearchNodes_LexiconWidensTheQueryWithoutChangingConfidence,
	// which proves production actually produces exactly this value for
	// exactly this scenario.
	const expansionOnlyLexicalConfidence = fulltextRelevanceFloor

	t.Run("alone, it never reaches the lone-candidate commit gate", func(t *testing.T) {
		mechanisms := graphrank.MergeMechanisms([]contextfabric.MatchMechanism{contextfabric.MatchLexical})
		if got := graphrank.DistinctMechanismCount(mechanisms); got != 1 {
			t.Fatalf("DistinctMechanismCount(%v) = %d, want 1 (single-mechanism fixture)", mechanisms, got)
		}
		got := graphrank.CorroboratedConfidence(mechanisms, expansionOnlyLexicalConfidence)
		if got != expansionOnlyLexicalConfidence {
			t.Fatalf("CorroboratedConfidence(single mechanism, %v) = %v, want the base UNCHANGED (distinct<2 must be a no-op)", expansionOnlyLexicalConfidence, got)
		}
		if got >= graphrank.CorroboratedFloor {
			t.Fatalf("an expansion-only lexical hit ALONE scored %v, want strictly below the lone-candidate commit gate %v -- if this ever passes, AC-3778-3-adjacent safety (a single weak proposal auto-committing) has silently broken", got, graphrank.CorroboratedFloor)
		}
	})

	t.Run("corroborated by a vector hit, it clears the lone-candidate commit gate", func(t *testing.T) {
		// MergeMechanisms is the SAME function MergeCandidates (resolve.go)
		// calls to union two findings of one subject -- exercised directly
		// here rather than re-implemented, so this fixture cannot silently
		// diverge from what a real merge produces.
		mechanisms := graphrank.MergeMechanisms(
			[]contextfabric.MatchMechanism{contextfabric.MatchLexical},
			[]contextfabric.MatchMechanism{contextfabric.MatchVector},
		)
		if got := graphrank.DistinctMechanismCount(mechanisms); got != 2 {
			t.Fatalf("DistinctMechanismCount(%v) = %d, want 2 (lexical+vector fixture)", mechanisms, got)
		}
		got := graphrank.CorroboratedConfidence(mechanisms, expansionOnlyLexicalConfidence)
		if got < graphrank.CorroboratedFloor {
			t.Fatalf("CorroboratedConfidence(lexical+vector, %v) = %v, want >= the lone-candidate commit gate %v -- this is the EXACT property CHAOS-3838's lift theory depends on: an expansion-only lexical floor hit, once corroborated by a vector hit, must clear this gate", expansionOnlyLexicalConfidence, got, graphrank.CorroboratedFloor)
		}
		if got > graphrank.CorroboratedCeiling {
			t.Fatalf("CorroboratedConfidence(lexical+vector, %v) = %v, want <= the corroborated ceiling %v (AC-3778-3's two-mechanism cap)", expansionOnlyLexicalConfidence, got, graphrank.CorroboratedCeiling)
		}
	})

	t.Run("vector alone still cannot reach the commit gate (AC-3778-3, unaffected by this ticket)", func(t *testing.T) {
		// Sanity anchor: L11's question-union widens WHICH subjects a
		// vector hit can propose, but never how confident a vector-only
		// hit reads -- vectorRelevanceCeiling (0.70) staying strictly below
		// CorroboratedFloor (0.72) is a falkorgraph-owned invariant this
		// ticket does not touch (see TestVectorBandCeilingStaysBelowTheLoneCandidateGate).
		mechanisms := graphrank.MergeMechanisms([]contextfabric.MatchMechanism{contextfabric.MatchVector})
		got := graphrank.CorroboratedConfidence(mechanisms, vectorRelevanceCeiling)
		if got >= graphrank.CorroboratedFloor {
			t.Fatalf("CorroboratedConfidence(vector alone, %v) = %v, want strictly below %v -- AC-3778-3 violated", vectorRelevanceCeiling, got, graphrank.CorroboratedFloor)
		}
	})
}
