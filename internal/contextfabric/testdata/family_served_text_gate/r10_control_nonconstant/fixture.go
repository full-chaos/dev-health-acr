// Package r10control is codex round 1 P1, re-executed by the lane.
package r10control

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type Details struct {
	NarrowerHint string
}

// hint returns prose. It takes no arguments, so its result carries no
// data dependence on the family at all.
func hint() string {
	return "ask about one subject at a time"
}

func BuildDetails(family contractsv1.ContextFabricQuestionFamily) Details {
	var d Details
	if family == contractsv1.ContextFabricQuestionFamilySubjectInvestigation {
		d.NarrowerHint = hint()
	}
	return d
}
