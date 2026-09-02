// Package r9dispatch is the second fixture authored by lane-4782-ssa
// against the SSA model's own blind spots. Not production code; loaded
// standalone by TestFamilyTextGateCatchesHistoricalConstructions.
//
// THE CONSTRUCTION: the family is stored on a struct FIELD, the text is
// produced by a METHOD on that struct, and the method is invoked through
// an INTERFACE, so the call site names neither the family, the concrete
// type, nor the function that does the deriving.
//
// WHY THIS ONE. Interface dispatch is the class the lane handoff called
// out as un-probed rather than closed: four review rounds defeated the
// syntax walker at four different call shapes, and closures, method
// values and interface dispatch were never reached only because no round
// happened to aim there. It is also the one place this analysis depends
// on something outside its own transfer rules -- the CHA call graph --
// and a dependency nothing tests is a dependency that breaks quietly.
//
// The receiver half matters as much as the dispatch half: Phrase() reads
// the family out of a field it never received as an argument, so the
// fixture pins program-wide field taint and dynamic dispatch TOGETHER,
// which is how they occur in real code.
package r9dispatch

import (
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Phraser hides both the concrete type and the derivation behind a
// signature that mentions only string.
type Phraser interface {
	Phrase() string
}

type familyPhraser struct {
	family contractsv1.ContextFabricQuestionFamily
}

// Phrase derives text from a field, not from a parameter.
func (p familyPhraser) Phrase() string {
	return fmt.Sprintf("This is a %s question, so here is some prose.", p.family)
}

// NewPhraser is where the family enters the object.
func NewPhraser(family contractsv1.ContextFabricQuestionFamily) Phraser {
	return familyPhraser{family: family}
}

// Result stands in for a served answer field.
type Result struct {
	DirectJudgment string
}

// Judge builds the phraser and then calls through the INTERFACE. The
// dispatch is the point: the call site names Phraser, not familyPhraser,
// so nothing syntactically present here says which function will run or
// that a family is involved at all.
//
// Judge takes the family rather than a ready-made Phraser deliberately.
// An earlier draft of this fixture took `p Phraser` as its parameter, and
// it passed -- but only on the derivation INSIDE Phrase(), never on the
// dispatch, because a parameter arriving from an unknown caller carries
// no taint. It was green for a reason that had nothing to do with what
// the fixture claims to test. Constructing the phraser here means the
// receiver at the dispatch site is genuinely family-derived, so the
// dispatch itself must be followed for this to be caught.
func Judge(family contractsv1.ContextFabricQuestionFamily) Result {
	var p Phraser = NewPhraser(family)
	return Result{DirectJudgment: p.Phrase()}
}
