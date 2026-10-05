package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Each answer tool serves the sentences the server composed from stored facts,
// in the structured answer and in the markdown, and labels them as computed by
// the server. The hosted result is the same for the three tools; what is read
// is what each tool hands the client.
func TestEveryAnswerToolServesTheServerComposedSentences(t *testing.T) {
	result := parityResult()
	result.DeterministicAnswer = "Total of commits count over the period: 147, summed from 29 of 29 days, every day of the period having a stored row."
	const label = "computed by the server from stored facts, not written by a model"

	type served struct {
		structuredText string
		markdown       string
	}
	read := func(t *testing.T, call func(boot *Bootstrap) (*mcpsdk.CallToolResult, error), field func(raw json.RawMessage) string) served {
		t.Helper()
		boot := withInterpretationTool(answerFixtureBootstrap(t, result, nil))
		called, err := call(boot)
		if err != nil || called.IsError {
			t.Fatalf("err %v result %s", err, toolResultText(called))
		}
		return served{structuredText: field(called.StructuredContent.(json.RawMessage)), markdown: toolResultText(called)}
	}
	// Read as the client reads it: the wire JSON, by field name.
	structuredText := func(raw json.RawMessage) string {
		var response struct {
			Structured map[string]any `json:"structured"`
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		text, _ := response.Structured["deterministic_answer"].(string)
		return text
	}
	answerField, resultField := structuredText, structuredText
	questionArgs, err := json.Marshal(contractsv1.MCPInvestigateQuestionRequest{Question: result.Question})
	if err != nil {
		t.Fatal(err)
	}
	resultArgs, err := json.Marshal(contractsv1.MCPInvestigationResultRequest{ResultID: result.ResultID})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]served{
		"investigate_question": read(t, func(boot *Bootstrap) (*mcpsdk.CallToolResult, error) {
			return invokeInvestigateQuestion(context.Background(), boot, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: questionArgs}})
		}, answerField),
		"investigate_with_interpretation": read(t, func(boot *Bootstrap) (*mcpsdk.CallToolResult, error) {
			return invokeInvestigateWithInterpretation(boot, interpretationArgs(t, nil))
		}, answerField),
		"investigation_result": read(t, func(boot *Bootstrap) (*mcpsdk.CallToolResult, error) {
			return invokeInvestigationResult(context.Background(), boot, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: resultArgs}})
		}, resultField),
	}
	for tool, got := range cases {
		if got.structuredText != result.DeterministicAnswer {
			t.Errorf("%s structured deterministic_answer = %q, want %q", tool, got.structuredText, result.DeterministicAnswer)
		}
		if !strings.Contains(got.markdown, result.DeterministicAnswer) || !strings.Contains(got.markdown, label) {
			t.Errorf("%s markdown lacks the sentence or its label:\n%s", tool, got.markdown)
		}
	}
}
