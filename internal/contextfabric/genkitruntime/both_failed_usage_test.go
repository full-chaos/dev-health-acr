package genkitruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestBothLegsFailedInterpretReceiptSumsBothLegsUsage(t *testing.T) {
	t.Parallel()
	invalid := validInterpretationOutput()
	invalid.Shape = "not_a_shape"
	for _, arm := range []struct {
		name    string
		primary *generatorStub
	}{
		{"primary generation error", &generatorStub{interpretErr: errors.New("primary down")}},
		{"primary invalid output", &generatorStub{interpretation: invalid}},
	} {
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()
			fallback := mustRuntime(t, &generatorStub{interpretErr: errors.New("fallback down")}, Config{})
			primary := mustRuntime(t, arm.primary, Config{Fallback: fallback})
			_, receipt, err := primary.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
			if err == nil {
				t.Fatal("want both legs to have failed")
			}
			if receipt.FallbackUsed {
				t.Fatal("FallbackUsed = true on a both-failed call")
			}
			u := receipt.Usage
			if u.InputTokens != 20 || u.OutputTokens != 8 || u.TotalTokens != 28 {
				t.Fatalf("receipt usage = %+v, want both legs summed (20/8/28)", u)
			}
		})
	}
}

func TestBothLegsFailedSynthesizeReceiptSumsBothLegsUsage(t *testing.T) {
	t.Parallel()
	rejected := validSynthesisOutput()
	rejected.Drivers[0].Category = "not_a_category"
	for _, arm := range []struct {
		name    string
		primary *generatorStub
	}{
		{"primary transport failure", &generatorStub{synthesisErr: errors.New("primary down")}},
		{"primary rejected a draft", &generatorStub{synthesis: rejected}},
	} {
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()
			fallback := mustRuntime(t, &generatorStub{synthesisErr: errors.New("fallback down")}, Config{})
			primary := mustRuntime(t, arm.primary, Config{Fallback: fallback})
			_, receipt, err := primary.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
			if err == nil {
				t.Fatal("want both legs to have failed")
			}
			u := receipt.Usage
			if u.InputTokens != 40 || u.OutputTokens != 16 || u.TotalTokens != 56 {
				t.Fatalf("receipt usage = %+v, want both legs summed (40/16/56)", u)
			}
		})
	}
}
