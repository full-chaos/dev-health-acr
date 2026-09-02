package mcp

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The MCP default budget must clear the contract's minimum answer size, or
// every caller who omits a budget is forwarded a value the hosted validator
// rejects.
//
// WHY THIS GUARD LIVES HERE and not beside the constant it protects: the
// minimum is declared in internal/contracts/v1, which cannot import
// internal/mcp -- the dependency runs the other way, and inverting it to reach
// a default would be worse than the drift it guards. So the assertion sits in
// the package that owns the default, reads BOTH values as constants, and
// compares them. A relation between two constants is checkable; a relation
// between a constant and a literal copied into another package is not.
//
// THE MARGIN IS THIN AND THAT IS THE POINT. At the time of writing the default
// is 65,536 and the minimum is 64,166 -- 1,370 bytes apart. The minimum is
// dominated by the worst-case echoed question (8000 runes at six serialized
// bytes each when the encoder escapes them), so any growth in the bounded
// envelope moves it toward this default. This fails at the moment it crosses,
// naming both numbers, rather than surfacing as MCP callers being refused for a
// budget they never chose.
func TestMCPDefaultBudgetClearsTheMinimumAnswerSize(t *testing.T) {
	t.Parallel()

	if defaultAnswerMaxSerializedBytes < contractsv1.ContextFabricMinimumAnswerBytes {
		t.Fatalf("the MCP default %d is now BELOW the minimum answer size %d.\n"+
			"Every MCP caller that omits a budget is now forwarded a value the hosted validator rejects, "+
			"for a budget they never chose. Raise defaultAnswerMaxSerializedBytes above the minimum, or "+
			"reconsider whether the minimum can keep growing.",
			defaultAnswerMaxSerializedBytes, contractsv1.ContextFabricMinimumAnswerBytes)
	}

	margin := defaultAnswerMaxSerializedBytes - contractsv1.ContextFabricMinimumAnswerBytes
	t.Logf("MCP default %d clears the minimum %d by %d bytes",
		defaultAnswerMaxSerializedBytes, contractsv1.ContextFabricMinimumAnswerBytes, margin)
}
