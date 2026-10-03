package genkitruntime

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretprompt"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestInterpretSendsTheInterpretpromptSystemMessage(t *testing.T) {
	stub := &generatorStub{interpretation: validInterpretationOutput()}
	runtime := mustRuntime(t, stub, Config{})
	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
		t.Fatal(err)
	}
	if len(stub.requests) == 0 || stub.requests[0].System != interpretprompt.System() {
		t.Fatal("the system message sent to the model differs from interpretprompt.System()")
	}
	if InterpretationSystemPrompt() != interpretprompt.System() {
		t.Fatal("InterpretationSystemPrompt differs from interpretprompt.System()")
	}
}
