package genkitruntime

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestSynthesizeSendsTheSynthesispromptSystemMessageAndPayload(t *testing.T) {
	stub := &generatorStub{synthesis: validSynthesisOutput()}
	runtime := mustRuntime(t, stub, Config{})
	input := validSynthesisInput()
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, input); err != nil {
		t.Fatal(err)
	}
	if len(stub.requests) == 0 {
		t.Fatal("no model request was placed")
	}
	sent := stub.requests[len(stub.requests)-1]
	if sent.System != synthesisprompt.System() {
		t.Fatal("the system message sent to the model differs from synthesisprompt.System()")
	}
	want, err := synthesisprompt.UserPayload("org_1", input, DefaultExchangeMaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Prompt != string(want) {
		t.Fatal("the user message sent to the model differs from synthesisprompt.UserPayload")
	}
	if SynthesisSystemPrompt() != synthesisprompt.System() {
		t.Fatal("SynthesisSystemPrompt differs from synthesisprompt.System()")
	}
	if DefaultSynthesisPromptVersion != synthesisprompt.PromptVersion {
		t.Fatal("DefaultSynthesisPromptVersion differs from synthesisprompt.PromptVersion")
	}
}
