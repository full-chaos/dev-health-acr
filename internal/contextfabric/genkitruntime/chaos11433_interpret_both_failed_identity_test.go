package genkitruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestInterpretBothLegsFailedReceiptNamesTheReportedLeg(t *testing.T) {
	fallback := validReceipt(contextfabric.ModelOperationInterpret)
	fallback.Provider, fallback.Model, fallback.ModelVersion = "fallback-provider", "fallback-model", "fallback-v9"
	fallback.OutputDigest = ""
	fallback.Outcome = "unavailable"
	fallback.Attempts = 7

	invalid := validInterpretationOutput()
	invalid.Shape = "not_a_shape"

	for _, leg := range []struct {
		name      string
		generator *generatorStub
	}{
		{"primary generation error", &generatorStub{interpretErr: errors.New("primary down")}},
		{"primary invalid output", &generatorStub{interpretation: invalid}},
	} {
		t.Run(leg.name, func(t *testing.T) {
			runtime := mustRuntime(t, leg.generator, Config{
				Fallback: erroringFallbackRuntime{receipt: fallback, err: errors.New("fallback down")},
			})
			_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
			if err == nil {
				t.Fatal("want both legs to have failed")
			}
			if receipt.Outcome != fallback.Outcome {
				t.Errorf("Outcome = %q, want the reported leg's %q", receipt.Outcome, fallback.Outcome)
			}
			if receipt.Provider != fallback.Provider || receipt.Model != fallback.Model || receipt.ModelVersion != fallback.ModelVersion {
				t.Errorf("identity = %q/%q/%q, want the reported leg's %q/%q/%q", receipt.Provider, receipt.Model, receipt.ModelVersion, fallback.Provider, fallback.Model, fallback.ModelVersion)
			}
			if receipt.OutputDigest != "" {
				t.Errorf("OutputDigest = %q, want empty: no draft was judged", receipt.OutputDigest)
			}
			if receipt.FallbackUsed {
				t.Error("FallbackUsed = true, want false")
			}
			if receipt.Attempts == fallback.Attempts {
				t.Errorf("Attempts = %d, want the primary's", receipt.Attempts)
			}
		})
	}
}
