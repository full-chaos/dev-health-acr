package v1_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func mcpSynthesisInput() *contractsv1.ContextFabricSynthesisInput {
	input := json.RawMessage(`{"facts":[]}`)
	sum := sha256.Sum256(input)
	return &contractsv1.ContextFabricSynthesisInput{
		Contract: contractsv1.ContextFabricSynthesisContract{
			ModelOutputVersion: "context-fabric-model-output.v8",
			PromptVersion:      "context-fabric-synthesis.v3",
			SystemSHA256:       strings.Repeat("b", 64),
		},
		Input:       input,
		InputSHA256: hex.EncodeToString(sum[:]),
		Rules:       []string{"Use only the facts in this input."},
	}
}

func TestMCPInvestigateQuestionRequestSynthesisAcceptsOnlyEmptyOrClient(t *testing.T) {
	t.Parallel()
	for value, wantOK := range map[contractsv1.ContextFabricSynthesisMode]bool{
		"": true, contractsv1.ContextFabricSynthesisModeClient: true,
		contractsv1.ContextFabricSynthesisModeServer: false, "x": false, "Client": false,
	} {
		err := contractsv1.MCPInvestigateQuestionRequest{Question: "Which teams need attention?", Synthesis: value}.Validate()
		if (err == nil) != wantOK {
			t.Errorf("synthesis %q: Validate error = %v, want ok = %v", value, err, wantOK)
		}
	}
}

func TestMCPInvestigateQuestionResponseValidatesItsSynthesisInput(t *testing.T) {
	t.Parallel()
	result := validMCPTestResult(contractsv1.ContextFabricInvestigationResultSchema)
	build := func(bundle *contractsv1.ContextFabricSynthesisInput) contractsv1.MCPInvestigateQuestionResponse {
		return contractsv1.MCPInvestigateQuestionResponse{
			SchemaVersion:    contractsv1.MCPInvestigateQuestionResponseSchema,
			Structured:       answerprojection.Project(result, answerprojection.DefaultBudget),
			SynthesisInput:   bundle,
			RenderedMarkdown: validRenderedMarkdown(),
			UntrustedContent: contractsv1.MCPUntrustedContent{Untrusted: true, Notice: contractsv1.MCPUntrustedContentNotice, Fields: contractsv1.MCPInvestigateQuestionUntrustedFields},
		}
	}
	if err := build(mcpSynthesisInput()).Validate(); err != nil {
		t.Fatalf("a response with a valid synthesis input failed: %v", err)
	}
	bad := mcpSynthesisInput()
	bad.InputSHA256 = strings.Repeat("0", 64)
	if err := build(bad).Validate(); err == nil {
		t.Fatal("a response with a wrong input_sha256 validated")
	}
}
