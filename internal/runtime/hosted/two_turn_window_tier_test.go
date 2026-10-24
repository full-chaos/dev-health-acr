package hosted_test

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestTwoTurnWindowTierRoutedExpectsQuestionStated(t *testing.T) {
	cases := []struct {
		name string
		w    *contractsv1.ContextFabricEffectiveEvidenceWindow
		want bool
	}{
		{"nil window", nil, false},
		{"question_stated", &contractsv1.ContextFabricEffectiveEvidenceWindow{Provenance: contractsv1.ContextFabricWindowQuestionStated}, true},
		{"inferred_default", &contractsv1.ContextFabricEffectiveEvidenceWindow{Provenance: contractsv1.ContextFabricWindowInferredDefault}, false},
		{"clarification_confirmed", &contractsv1.ContextFabricEffectiveEvidenceWindow{Provenance: contractsv1.ContextFabricWindowClarificationConfirmed}, false},
	}
	for _, c := range cases {
		if got := twoTurnWindowTierRouted(c.w); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
