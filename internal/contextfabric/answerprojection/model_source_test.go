package answerprojection

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The bounded answer names whose model served the turn, exactly as the
// canonical result does, and carries nothing when the result carries nothing.
func TestProjectionCopiesTheModelSource(t *testing.T) {
	for _, source := range append([]contractsv1.ContextFabricModelSource{""}, contractsv1.ContextFabricModelSources()...) {
		result := richResult()
		result.ModelSource = source
		if err := result.Validate(); err != nil {
			t.Fatalf("fixture with model_source %q is not a valid canonical result: %v", source, err)
		}
		projection := Project(result, Budget{})
		if err := projection.Validate(); err != nil {
			t.Fatalf("projection with model_source %q failed contract validation: %v", source, err)
		}
		if projection.ModelSource != source {
			t.Fatalf("projection model_source = %q, want %q", projection.ModelSource, source)
		}
	}
	projection := Project(richResult(), Budget{})
	projection.ModelSource = "tenant"
	if err := projection.Validate(); err == nil {
		t.Fatal("a projection with a model_source outside the vocabulary was accepted")
	}
}
