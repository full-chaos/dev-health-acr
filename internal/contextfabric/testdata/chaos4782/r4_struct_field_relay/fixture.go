// Package r4relay is a committed RED fixture for CHAOS-4782's second
// acceptance bullet: a construction the CURRENT (heuristic, syntax-only)
// CHAOS-4735 sweep cannot catch, distinct from R1/R2/R3. The sweep's own
// header comment names this exact gap under "WHAT THIS DOES NOT CATCH":
// "anything reached through a function boundary or a struct field, where
// the family and the text are in different scopes." Not production code;
// loaded standalone by TestChaos4782CatchesHistoricalConstructions.
//
// The construction: one function derives the family's ordinal position (the
// R3 shape) and stores it in a struct FIELD; a SEPARATE function, given only
// that struct, reads the field and indexes a sentence table. No single
// expression in the whole file resembles a family-keyed switch or map --
// the AST-only, single-file, single-expression matching the heuristic does
// has nothing to key on. A gate needs cross-function, field-sensitive data
// flow to see that `lookup.ordinal` is family-derived at all.
package r4relay

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

type narrowingLookup struct {
	ordinal int
}

// resolveOrdinal finds the family's position in the closed vocabulary and
// stores it on a struct field -- the R3 shape, but the result never leaves
// this function as a bare value; it leaves as a struct field.
func resolveOrdinal(family contractsv1.ContextFabricQuestionFamily) narrowingLookup {
	var lookup narrowingLookup
	for position, member := range contractsv1.ContextFabricQuestionFamilyVocabulary() {
		if member == family {
			lookup.ordinal = position
		}
	}
	return lookup
}

var phrases = []string{
	"ask about one subject at a time",
}

// NarrowerPhrase never itself ranges over the vocabulary and never itself
// names a family constant. It only reads a field of a struct built
// elsewhere -- the family/text pairing is invisible in this function's own
// syntax, which is exactly the gap this fixture exercises.
func NarrowerPhrase(family contractsv1.ContextFabricQuestionFamily) string {
	lookup := resolveOrdinal(family)
	return phrases[lookup.ordinal]
}
