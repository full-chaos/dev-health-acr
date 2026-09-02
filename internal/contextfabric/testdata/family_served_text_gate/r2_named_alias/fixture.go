// Package r2alias is a committed RED fixture for CHAOS-4782: the R2
// construction that defeated the second version of the CHAOS-4735 heuristic
// sweep (codex round 2, ARGUED then reproduced by the lane). Not production
// code; loaded standalone by TestFamilyTextGateCatchesHistoricalConstructions.
//
// The construction: a named type `phrase` whose underlying type is
// `string` stands in for the map's value type. Matching textual types by
// NAME ("string", or an identifier ending String/Text/Message) cannot
// resolve `phrase` to its underlying representation; a gate that asks
// go/types whether the value type CAN hold text (anything except a closed
// set of non-textual builtins) closes this by construction.
package r2alias

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

type phrase string

var budgetRefusalMessage = map[contractsv1.ContextFabricQuestionFamily]phrase{
	contractsv1.ContextFabricQuestionFamilySubjectInvestigation: "Ask about one subject.",
}

// Message reproduces codex R2: the family keys a map from the wire family
// type to a named string-underlying type, feeding a served message.
func Message(family contractsv1.ContextFabricQuestionFamily) string {
	return string(budgetRefusalMessage[family])
}
