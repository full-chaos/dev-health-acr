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
// THE MARGIN IS NOW WIDE, AND THAT IS A DELIBERATE CHANGE OF MEANING. An
// earlier revision pinned the minimum to the MAXIMAL valid answer (64,166) and
// this guard warned that only 1,370 bytes separated it from the default. That
// pin was wrong: the maximal answer is dominated by request-shaped fields --
// the echoed question and the interpretation's terms -- which no narrowing path
// reduces, so no static constant can be a floor for every request. The static
// constant is now the request-INDEPENDENT floor (2,160), and the
// request-dependent part is a runtime check.
//
// So this guard is no longer about a thin margin. It is the standing assertion
// that the sidecar's default clears the static floor at all -- cheap, and it
// still fails loudly if either number moves past the other.
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
