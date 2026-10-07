package mcp

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A budget field sent beside the question instead of inside budget is refused
// by name; it is not dropped so the default budget is served.
func TestInvestigateQuestionRefusesAMisplacedBudgetField(t *testing.T) {
	var seen contractsv1.ContextFabricInvestigationRequest
	boot := answerFixtureBootstrap(t, parityResult(), &seen)
	result := callInvestigateQuestionRaw(t, boot, map[string]any{"question": "Which teams need attention?", "max_cohort_members": 5})
	if !result.IsError {
		t.Fatal("IsError = false, want a validation refusal for the misplaced field")
	}
	text := toolResultText(result)
	if !strings.HasPrefix(text, "validation:") || !strings.Contains(text, `"max_cohort_members"`) {
		t.Errorf("tool text %q does not name the misplaced field", text)
	}
	if seen.Question != "" {
		t.Error("the hosted API was called for a request the tool must refuse")
	}
}

func TestInvestigateQuestionRefusesAnUnknownNestedField(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"budget": {"question": "q", "budget": map[string]any{"max_members": 5}},
		"scope":  {"question": "q", "scope": map[string]any{"repos": []string{"a/b"}}},
	} {
		t.Run(name, func(t *testing.T) {
			var seen contractsv1.ContextFabricInvestigationRequest
			boot := answerFixtureBootstrap(t, parityResult(), &seen)
			result := callInvestigateQuestionRaw(t, boot, args)
			if !result.IsError || !strings.HasPrefix(toolResultText(result), "validation:") {
				t.Fatalf("result = %q (IsError %v), want a validation refusal", toolResultText(result), result.IsError)
			}
			if seen.Question != "" {
				t.Error("the hosted API was called for a request the tool must refuse")
			}
		})
	}
}

// The budget inside budget still narrows the list.
func TestInvestigateQuestionHonoursABudgetInsideBudget(t *testing.T) {
	var seen contractsv1.ContextFabricInvestigationRequest
	boot := answerFixtureBootstrap(t, parityResult(), &seen)
	result := callInvestigateQuestionRaw(t, boot, map[string]any{"question": "Which teams need attention?", "budget": map[string]any{"max_cohort_members": 5}})
	if result.IsError {
		t.Fatalf("refused: %s", toolResultText(result))
	}
	if seen.Options.MaxCohortMembers != 5 {
		t.Errorf("max_cohort_members = %d, want 5", seen.Options.MaxCohortMembers)
	}
}

func TestInvestigateWithInterpretationNamesAMisplacedBudgetField(t *testing.T) {
	var seen contractsv1.ContextFabricInvestigationRequest
	boot := withInterpretationTool(answerFixtureBootstrap(t, parityResult(), &seen))
	result, err := invokeInvestigateWithInterpretation(boot, interpretationArgs(t, func(m map[string]any) { m["max_cohort_members"] = 5 }))
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	text := toolResultText(result)
	if !result.IsError || !strings.Contains(text, `"max_cohort_members"`) {
		t.Errorf("tool text %q does not name the misplaced field", text)
	}
	if seen.Question != "" {
		t.Error("the hosted API was called for a request the tool must refuse")
	}
}

func TestDecodeInvestigationArgumentsRefusesTrailingDataAndBoundsTheNamedKey(t *testing.T) {
	var input contractsv1.MCPInvestigateQuestionRequest
	if err := decodeInvestigationArguments([]byte(`{"question":"q"} {"question":"r"}`), &input); err == nil {
		t.Fatal("trailing JSON value accepted, want a refusal")
	}
	long := strings.Repeat("k", 200)
	err := decodeInvestigationArguments([]byte(`{"question":"q","`+long+`":1}`), &input)
	if err == nil {
		t.Fatal("unknown key accepted")
	}
	message := investigationArgumentsMessage(toolInvestigateQuestion, err)
	if strings.Contains(message, strings.Repeat("k", 65)) || !strings.Contains(message, strings.Repeat("k", 64)) {
		t.Errorf("message %q does not bound the named key to 64 characters", message)
	}
}
