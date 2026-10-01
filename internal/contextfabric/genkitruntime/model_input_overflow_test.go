package genkitruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func requireModelInputOverflow(t *testing.T, err error, wantBytes, wantMaxBytes int) {
	t.Helper()
	var overflow *contextfabric.ModelInputOverflow
	if !errors.As(err, &overflow) || !errors.Is(err, contextfabric.ErrModelInputTooLarge) {
		t.Fatalf("error = %v, want a model input overflow", err)
	}
	if overflow.Bytes != wantBytes || overflow.MaxBytes != wantMaxBytes {
		t.Fatalf("overflow = %+v, want %d bytes against a bound of %d", overflow, wantBytes, wantMaxBytes)
	}
}

// A synthesis input one byte over the bound is refused with both sizes and
// no model call; the same input at the bound is sent.
func TestSynthesizeRefusesAnInputOverTheBoundWithItsSize(t *testing.T) {
	t.Parallel()
	input := validSynthesisInput()
	encoded, err := BuildSynthesisPrompt(input, 1<<30)
	if err != nil {
		t.Fatal(err)
	}

	generator := &sequencedGenerator{synthesis: validSynthesisOutput()}
	runtime := mustRuntime(t, generator, Config{MaxAttempts: 1})
	runtime.config.MaxInputBytes = len(encoded) - 1
	_, _, err = runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	requireModelInputOverflow(t, err, len(encoded), len(encoded)-1)
	if generator.calls != 0 {
		t.Fatalf("generator.calls = %d, want 0", generator.calls)
	}

	runtime.config.MaxInputBytes = len(encoded)
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, input); err != nil {
		t.Fatalf("SynthesizeAnswer() at the bound error = %v, want the call placed", err)
	}
	if generator.calls == 0 {
		t.Fatal("generator.calls = 0, want the model called")
	}
}

func TestInterpretRefusesAnInputOverTheBoundWithItsSize(t *testing.T) {
	t.Parallel()
	request := validRequest()
	encoded, err := BuildInterpretationPrompt(request, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	generator := &sequencedGenerator{interpretation: validInterpretationOutput()}
	runtime := mustRuntime(t, generator, Config{MaxAttempts: 1})
	runtime.config.MaxInputBytes = len(encoded) - 1

	_, _, err = runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, request)

	requireModelInputOverflow(t, err, len(encoded), len(encoded)-1)
	if generator.calls != 0 {
		t.Fatalf("generator.calls = %d, want 0", generator.calls)
	}
}

func TestBuildSynthesisPromptRefusesAnInputOverTheBoundWithItsSize(t *testing.T) {
	t.Parallel()
	input := validSynthesisInput()
	encoded, err := BuildSynthesisPrompt(input, 1<<30)
	if err != nil {
		t.Fatal(err)
	}

	_, err = BuildSynthesisPrompt(input, len(encoded)-1)

	requireModelInputOverflow(t, err, len(encoded), len(encoded)-1)
}
