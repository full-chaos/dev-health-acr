package mcp

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The answer budget is the target the service sheds toward (answer_budget_bytes)
// and the tool projects to it itself. The body bound is the service's advertised
// ceiling, so the canonical result always comes back here and the service never
// answers this tool with a projection it cannot read.
func TestAnAnswerByteBudgetIsTheSheddingTargetAndTheBodyBoundIsTheServiceCeiling(t *testing.T) {
	var seen contractsv1.ContextFabricInvestigationRequest
	result := parityResult()
	boot := answerFixtureBootstrap(t, result, &seen)
	ceiling := boot.Capabilities.Limits.MaxSerializedBytes
	if ceiling <= 0 {
		t.Fatalf("fixture advertises no serving ceiling")
	}

	tight := contractsv1.MCPInvestigationBudget{MaxSerializedBytes: 8192}
	response := callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{
		Question:          result.Question,
		IncludeFullResult: true,
		Budget:            &tight,
	})

	if want := min(ceiling, contractsv1.ContextFabricSerializedBytesMax); seen.Options.MaxSerializedBytes != want {
		t.Errorf("hosted max_serialized_bytes = %d, want the advertised ceiling %d", seen.Options.MaxSerializedBytes, want)
	}
	// The shedding target is the caller's answer budget, exactly the number
	// the service shed toward before the body bound was split from it.
	if seen.Options.AnswerBudgetBytes != tight.MaxSerializedBytes {
		t.Errorf("hosted answer_budget_bytes = %d, want the answer budget %d", seen.Options.AnswerBudgetBytes, tight.MaxSerializedBytes)
	}
	if response.FullResult != nil || !response.Structured.ProjectionBudget.FullResultOmitted {
		t.Errorf("a canonical result over the 8192 byte answer budget was not declared omitted")
	}
	if response.Structured.ResultID != result.ResultID {
		t.Errorf("result_id = %q, want %q so the caller can fetch the canonical result", response.Structured.ResultID, result.ResultID)
	}
}

func TestTheMCPDefaultAnswerBudgetIsForwardedOnlyAsTheServerModeShedTarget(t *testing.T) {
	for _, mode := range []contractsv1.ContextFabricSynthesisMode{"", contractsv1.ContextFabricSynthesisModeClient} {
		var seen contractsv1.ContextFabricInvestigationRequest
		result := parityResult()
		boot := answerFixtureBootstrap(t, result, &seen)
		callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{Question: result.Question, Synthesis: mode})
		want := min(defaultAnswerMaxSerializedBytes, seen.Options.MaxSerializedBytes)
		if mode == contractsv1.ContextFabricSynthesisModeClient {
			want = 0
		}
		if seen.Options.AnswerBudgetBytes != want {
			t.Errorf("mode %q: hosted answer_budget_bytes = %d, want %d", mode, seen.Options.AnswerBudgetBytes, want)
		}
	}
}

func TestACallerBudgetOnAClientSynthesisTurnIsForwardedAsTheAnswerBudget(t *testing.T) {
	var seen contractsv1.ContextFabricInvestigationRequest
	result := parityResult()
	boot := answerFixtureBootstrap(t, result, &seen)
	callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{Question: result.Question, Synthesis: contractsv1.ContextFabricSynthesisModeClient, Budget: &contractsv1.MCPInvestigationBudget{MaxSerializedBytes: 60000}})
	if seen.Options.AnswerBudgetBytes != 60000 {
		t.Errorf("hosted answer_budget_bytes = %d, want 60000", seen.Options.AnswerBudgetBytes)
	}
}
