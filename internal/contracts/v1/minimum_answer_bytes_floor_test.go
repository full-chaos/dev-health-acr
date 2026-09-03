package v1

import (
	"encoding/json"
	"testing"
)

// TestMinimumAnswerBytesMatchesTheConstructedFixture is the rot guard. It ties
// the published constant to the fixture it is derived from, so the two cannot
// drift apart -- the failure mode that produced four wrong values before the
// fixture was built by construction.
func TestMinimumAnswerBytesMatchesTheConstructedFixture(t *testing.T) {
	r := buildFromTable(t, func(b answerBound) func(*ContextFabricInvestigationResult) { return b.Min })
	if err := r.Validate(); err != nil {
		t.Fatalf("the irreducible fixture must be valid before it can define a floor: %v", err)
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(encoded) != ContextFabricMinimumAnswerBytes {
		t.Fatalf("ContextFabricMinimumAnswerBytes is %d, but the irreducible fixture measures %d bytes.\n"+
			"The constant is DERIVED from the fixture: if a Min in answerBoundTable() changed on purpose, "+
			"update the constant in the same commit and say why. If not, a bound just drifted.",
			ContextFabricMinimumAnswerBytes, len(encoded))
	}
	t.Logf("floor: %d bytes, derived from the constructed irreducible fixture", len(encoded))
}
