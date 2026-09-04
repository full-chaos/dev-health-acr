//go:build never

package contextfabric

import "testing"

// TestABuildConstrainedProbeMustNotCount is a PERMANENT NEGATIVE CONTROL for
// the reach-probe rule, and it is deliberately excluded from every build.
//
// It looks like a valid probe in every way a source-reading check can see: it
// is a Test function, it lives in a _test.go file in this package, and it calls
// assertArmNeverExecuted. The only thing wrong with it is that `go test` never
// compiles it, so it can never fail and therefore can never prove an arm
// unreachable.
//
// A review defeated the previous version of the rule with exactly this shape,
// using an in-memory file. Keeping a real one on disk means the control cannot
// rot: if packageTestFunctions ever goes back to enumerating the filesystem
// instead of asking the compiler, TestEveryUncoveredArmClaimCarriesAReachProbe
// fails immediately, because this name would reappear in the probe set.
//
// The tag is `never`, which nothing sets. If you are reading this because you
// added `-tags never`, do not: this file exists to be excluded.
func TestABuildConstrainedProbeMustNotCount(t *testing.T) {
	assertArmNeverExecuted(t, "this probe is never compiled", 0)
}
