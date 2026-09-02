// Package r3ordinal is a committed RED fixture for CHAOS-4782: the R3
// construction that defeated the third version of the CHAOS-4735 heuristic
// sweep (codex round 3, EXECUTED) and is the reason CHAOS-4782 exists at
// all -- the sweep's own header comment says so: "R3 IS NOT [closed], and
// saying so is the point." Not production code; loaded standalone by
// TestFamilyTextGateCatchesHistoricalConstructions.
//
// The construction: no switch, no map, no comparison to a string literal.
// The family's ordinal POSITION in the closed vocabulary is found by
// ranging over ContextFabricQuestionFamilyVocabulary(), and that position
// indexes an unrelated []string sentence table. Nothing here has type
// QuestionFamily except the range's element variable and the parameter;
// the value that actually reaches the served field is a plain string
// selected by a plain int. Syntax matching cannot see this: it needs DATA
// FLOW from a family-typed value to a text-yielding index.
package r3ordinal

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

var phrases = []string{
	"ask about one subject at a time",
}

// NarrowerPhrase reproduces codex R3: family -> ordinal via
// ContextFabricQuestionFamilyVocabulary() -> index into a hand-authored
// sentence table, landing in a returned (served) string.
func NarrowerPhrase(family contractsv1.ContextFabricQuestionFamily) string {
	idx := -1
	for position, member := range contractsv1.ContextFabricQuestionFamilyVocabulary() {
		if member == family {
			idx = position
		}
	}
	if idx < 0 || idx >= len(phrases) {
		return ""
	}
	return phrases[idx]
}
