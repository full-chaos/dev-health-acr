// Package p2repro is a committed RED fixture found by codex round 2, P1
// (EXECUTED) against this gate itself, not against the original CHAOS-4735
// sweep -- a NEW class, not a re-find. Not production code; loaded
// standalone by TestFamilyTextGateCatchesHistoricalConstructions.
//
// The construction combines two evasions:
//  1. A TAGLESS switch (`switch { case cond: ... }`) has no tag
//     expression at all, so a rule keyed on "is the switch TAG tainted"
//     never fires -- it must look at the CASE CONDITIONS instead.
//  2. The comparison against the family value is hidden inside an
//     ORDINARY function call (strings.EqualFold), not a direct `==`.
//     Recognizing strings.EqualFold BY NAME would be exactly the
//     allowlist-of-shapes anti-pattern this ticket exists to retire --
//     the next construction would use strings.Contains, or a regexp, or
//     a hand-written equals method, and the list would grow forever. The
//     gate instead asks a structural question with no name-list at all:
//     does ANY identifier anywhere in the case condition's syntax tree
//     resolve to a tainted value, regardless of which function wraps it.
//
// The DEFAULT arm is also served hand-authored prose here, and is flagged
// too: once one arm's selection is shown to be family-derived, the whole
// switch is a family dispatch, default included -- which side of a binary
// partition you land on is still decided by the family value.
package p2repro

import (
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Result stands in for a served answer field.
type Result struct {
	DirectJudgment string
}

// Judge reproduces the tagless-switch-plus-EqualFold construction.
func Judge(family contractsv1.ContextFabricQuestionFamily) Result {
	var result Result
	switch {
	case strings.EqualFold(string(family), "subject_investigation"):
		result.DirectJudgment = "This family receives hand-authored prose."
	default:
		result.DirectJudgment = "A different hand-authored answer."
	}
	return result
}
