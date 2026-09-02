// Package r8maprelay is a fixture authored by lane-4782-ssa against the
// SSA model's OWN weakest joint, not against a construction some review
// round found. Not production code; loaded standalone by
// TestFamilyTextGateCatchesHistoricalConstructions.
//
// THE CONSTRUCTION: family-derived text is written into a PACKAGE-LEVEL
// MAP in one function and read back out of it in a DIFFERENT function,
// with no value, parameter, field or return ever carrying it between the
// two. The only thing connecting Record to Serve is the global.
//
// WHY THIS ONE. The analysis models memory flow-insensitively, per
// origin: a store through an address taints the object that address
// resolves to, and a load reads it back, with no regard for whether the
// store actually happens before the load, or in the same function, or at
// all. That is the deliberately coarse half of the design -- it
// over-approximates rather than under-approximates -- and coarse
// approximations are exactly where an analysis is most likely to be
// accidentally right for the wrong reason. If this fixture ever passes
// because of statement ORDER rather than because the global carries the
// taint, the model has a hole a reviewer would find before this test
// would, so the fixture pins the property directly.
//
// It also covers the case the syntax walker's own doc named as uncaught
// and could never have reached: "the family and the text are in different
// scopes", here with no shared scope at all.
package r8maprelay

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// narrowingCache is the only thing joining the two functions below.
var narrowingCache = map[string]string{}

// Result stands in for a served answer field. The real one is
// InvestigationResult.DirectJudgment; see the lane handoff for the chain
// from there to the wire.
type Result struct {
	DirectJudgment string
}

// Record puts family-derived text into the global. Nothing is returned
// and nothing is stored on a receiver.
func Record(family contractsv1.ContextFabricQuestionFamily) {
	narrowingCache["narrower"] = "the caller asked a " + string(family) + " question"
}

// Serve reads it back out, in a different function, and serves it. It
// never sees a family value.
func Serve() Result {
	return Result{DirectJudgment: narrowingCache["narrower"]}
}
