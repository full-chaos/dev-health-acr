// Package r7sprintf is a committed RED fixture found by codex round 3, P1
// (EXECUTED against this gate, re-executed independently by the lane) --
// a NEW class, not a re-find, and the finding that ended the syntax-walker
// approach: chris ruled 2026-09-02 to re-shape this gate onto value-flow
// analysis (golang.org/x/tools/go/ssa) rather than patch the walker a
// fourth time. See `.remember/lanes/lane-4782/handoff-2026-09-02.md` for
// the full history and the SSA design note. Not production code; loaded
// standalone by TestFamilyTextGateCatchesHistoricalConstructions.
//
// The construction: an ORDINARY function call (fmt.Sprintf) whose
// arguments include a family-typed value, its RESULT assigned directly to
// a served field. The current gate's eval() propagates taint through
// conversions and evaluates a call's arguments (so a NESTED violation
// inside an argument is still caught), but a plain call's own RESULT is
// deliberately treated as untainted -- the original design choice that
// kept `log.Debug("family", family)` from being flagged. fmt.Sprintf is
// the single most natural way to leak a family value into text, and nine
// syntax-level fixes across three rounds (R1-P1 composite-literal keys,
// R1-P2 sanctioning granularity, R2-P1 tagless-switch conditions) never
// touched this: every fix closed one MORE syntactic site, and the walker
// is provably never closed under an arbitrary call boundary. That is the
// re-shape's whole argument: SSA sees a function's RETURN VALUE as a value
// derived from its arguments, uniformly, with no per-call-shape
// enumeration required.
package r7sprintf

import (
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Result stands in for a served answer field (DirectJudgment on the real
// InvestigationResult; see the handoff for the exact served-field chain).
type Result struct {
	DirectJudgment string
}

// Judge reproduces the fmt.Sprintf construction verbatim.
func Judge(family contractsv1.ContextFabricQuestionFamily) Result {
	return Result{
		DirectJudgment: fmt.Sprintf("The selected family is %s.", family),
	}
}
