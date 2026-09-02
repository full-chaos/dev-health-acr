// Package r15ctrlonly PINS THE TIER BOUNDARY between implicit and data
// flow, and it is deliberately identical to R14 in every respect but one.
//
// R14: the served field is assigned text COMPUTED from the family
// (fmt.Sprintf), on a struct this fixture marshals. Data flow. ENFORCED.
//
// R15 (here): the served field is assigned a hand-authored CONSTANT,
// selected by testing the family, on a struct this fixture marshals.
// Implicit flow only -- nothing computes the text FROM the family, the
// branch merely chooses which text is served. REPORTED, never enforced.
//
// The pair is the boundary. Everything about the sink is the same in both
// -- same struct shape, same encoder-reachability, same served field --
// so the ONLY thing separating enforced from reported here is whether the
// derivation is a data-flow fact or a control-flow over-approximation.
//
// Why the boundary sits there: implicit flow says "which value got served
// depended on a family test", which is a genuine signal but not a claim
// that the served value was computed from the family. On real code it
// fires on correct constructions -- chaos4636_plan_carry.go:215 copies a
// closed token from a carried plan under a family test, with no data
// dependence anywhere, and was ENFORCED until this boundary was drawn.
// A gate that fails on correct code gets switched off, and then it
// protects nothing at all.
//
// If a future change makes implicit flow enforceable, THIS FIXTURE FAILS,
// and whoever made that change has to re-read the claim in the gate
// header and the PR body rather than let it drift wider than what is
// proven. That is the point of pinning it.
package r15ctrlonly

import (
	"encoding/json"
	"net/http"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Answer is encoder-reachable, exactly as R14's is.
type Answer struct {
	Note string `json:"note"`
}

// Serve selects hand-authored prose by testing the family. Nothing here
// computes text FROM the family value.
func Serve(w http.ResponseWriter, family contractsv1.ContextFabricQuestionFamily) {
	var answer Answer
	if family == contractsv1.ContextFabricQuestionFamilySubjectInvestigation {
		answer.Note = "ask about one subject at a time"
	}
	encoded, err := json.Marshal(answer)
	if err != nil {
		return
	}
	_, _ = w.Write(encoded)
}
