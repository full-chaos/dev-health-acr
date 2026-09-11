package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// An UNSUPPORTED result -- nothing read, no citable evidence -- may carry an
// empty answer sentence. This drives the PUBLISHED example of that form through
// both real tool handlers, over the real sidecar client and its transport-side
// revalidation, and validates the EMITTED JSON against each tool's published
// schema: what an MCP client actually receives, not the value in memory.
//
// The control makes the same document SUPPORTED (one claimed fact, one
// evidence ref) while keeping the empty answer sentence: the sidecar client
// must refuse it, or the acceptance above would hold for a transport that
// accepts anything.
func TestAnUnsupportedResultReachesAnMCPClientWithAnEmptyAnswerSentence(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "examples", "v1", "context_fabric_investigation_result_unsupported.v1.json"))
	if err != nil {
		t.Fatalf("read the published example: %v", err)
	}
	var result contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode the published example: %v", err)
	}
	if result.DeterministicAnswer != "" || contractsv1.ContextFabricResultSupported(result) {
		t.Fatalf("premise: the example must be unsupported with an empty answer sentence (answer=%q)", result.DeterministicAnswer)
	}

	boot := answerFixtureBootstrap(t, result, nil)
	t.Run("investigate_question", func(t *testing.T) {
		response := callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{Question: result.Question})
		if err := response.Validate(); err != nil {
			t.Fatalf("the answer wrapper rejected an unsupported result: %v", err)
		}
		assertMatchesToolSchema(t, response, investigateQuestionResponseSchemaFile)
	})
	t.Run("investigate_question with the full result attached", func(t *testing.T) {
		// include_full_result is the ONLY path that carries the canonical
		// result through the question response's own embedded result schema.
		// Without it the answer projection is validated and that schema's
		// copy of the conditional is never exercised.
		response := callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{Question: result.Question, IncludeFullResult: true})
		if response.FullResult == nil {
			t.Fatal("include_full_result returned no canonical result; the embedded result schema would not be exercised")
		}
		if response.FullResult.DeterministicAnswer != "" {
			t.Fatalf("full_result.deterministic_answer = %q, want the empty form carried whole", response.FullResult.DeterministicAnswer)
		}
		if err := response.Validate(); err != nil {
			t.Fatalf("the answer wrapper rejected an unsupported full result: %v", err)
		}
		assertMatchesToolSchema(t, response, investigateQuestionResponseSchemaFile)
	})
	t.Run("investigation_result", func(t *testing.T) {
		response := callInvestigationResult(t, boot, result.ResultID)
		if response.Structured.DeterministicAnswer != "" {
			t.Fatalf("structured.deterministic_answer = %q, want the empty form returned whole", response.Structured.DeterministicAnswer)
		}
		if len(response.Structured.Limitations) == 0 {
			t.Fatal("the disclosure did not survive the round trip")
		}
		if err := response.Validate(); err != nil {
			t.Fatalf("the result wrapper rejected an unsupported result: %v", err)
		}
		assertMatchesToolSchema(t, response, investigationResultResponseSchemaFile)
	})

	t.Run("control: the same document made supported is refused", func(t *testing.T) {
		supported := result
		text := "open"
		supported.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{{ClaimID: "claim_status_0000", Kind: contractsv1.ContextFabricFactStatus,
			Subject: result.SubjectResolution.Committed[0], Field: "state", Value: contractsv1.ContextFabricScalarValue{String: &text}}}
		supported.Completeness.ClaimedFactsCount = 1
		supported.EvidenceRefIDs = []string{"evidence_00000000"}
		controlBoot := answerFixtureBootstrap(t, supported, nil)
		args, err := json.Marshal(contractsv1.MCPInvestigationResultRequest{ResultID: supported.ResultID})
		if err != nil {
			t.Fatal(err)
		}
		toolResult, err := handleInvestigationResult(context.Background(), controlBoot, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: args}})
		if err != nil {
			t.Fatalf("protocol error: %v", err)
		}
		if !toolResult.IsError {
			t.Fatal("a SUPPORTED result with an empty answer sentence reached the MCP client")
		}
	})
}

func callInvestigationResult(t *testing.T, boot *Bootstrap, resultID string) contractsv1.MCPInvestigationResultResponse {
	t.Helper()
	args, err := json.Marshal(contractsv1.MCPInvestigationResultRequest{ResultID: resultID})
	if err != nil {
		t.Fatal(err)
	}
	toolResult, err := handleInvestigationResult(context.Background(), boot, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: args}})
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	if toolResult.IsError {
		t.Fatalf("investigation_result reported an error: %s", toolResultText(toolResult))
	}
	var response contractsv1.MCPInvestigationResultResponse
	if err := json.Unmarshal(toolResult.StructuredContent.(json.RawMessage), &response); err != nil {
		t.Fatal(err)
	}
	return response
}
