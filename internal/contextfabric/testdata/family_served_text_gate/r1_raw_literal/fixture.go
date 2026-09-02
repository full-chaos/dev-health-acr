// Package r1raw is a committed RED fixture for CHAOS-4782: the R1
// construction that defeated the first version of the CHAOS-4735 heuristic
// sweep (codex round 1, EXECUTED). It is never imported by production code
// and is not itself production code -- TestFamilyTextGateCatchesHistoricalConstructions
// loads it standalone and asserts the type-aware gate flags it.
//
// The construction: the family value is converted with `string(...)` before
// comparison, so no QuestionFamily-typed expression is directly compared to
// the literal at the syntax level, and no family CONSTANT is named. A gate
// keyed on constant names or on the literal type of the comparison operands
// cannot see this; a gate that follows the VALUE through the conversion can.
package r1raw

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// Details stands in for a served field (an HTTP error body, an MCP
// projection field) -- the fixture does not need to be wired into the real
// wire types to prove the shape is caught.
type Details struct {
	NarrowerHint string
}

// BuildDetails reproduces codex R1 exactly: `string(plan.Family) ==
// "subject_investigation"` gating a served, hand-authored phrase.
func BuildDetails(family contractsv1.ContextFabricQuestionFamily) Details {
	var d Details
	if string(family) == "subject_investigation" {
		d.NarrowerHint = "ask_about_one_subject"
	}
	return d
}
