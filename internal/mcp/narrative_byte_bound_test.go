package mcp

import (
	"strconv"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The caller's own byte budget reaches the projection: limitations and
// warnings yield to it, whatever the hosted service sent.
func TestInvestigateQuestionNarrativesYieldToTheCallersByteBudget(t *testing.T) {
	result := parityResult()
	result.Limitations, result.Warnings = nil, nil
	for i := 0; i < contractsv1.ContextFabricProjectedNarrativeMaxCount; i++ {
		result.Limitations = append(result.Limitations, "limitation "+strconv.Itoa(i)+" "+strings.Repeat("x", 1900))
		result.Warnings = append(result.Warnings, "warning "+strconv.Itoa(i)+" "+strings.Repeat("y", 1900))
	}
	boot := answerFixtureBootstrap(t, result, nil)
	tight := contractsv1.MCPInvestigationBudget{MaxSerializedBytes: 8192}
	response := callInvestigateQuestion(t, boot, contractsv1.MCPInvestigateQuestionRequest{Question: result.Question, Budget: &tight})

	allowance := 8192 / 4
	for name, values := range map[string][]string{"limitations": response.Structured.Limitations, "warnings": response.Structured.Warnings} {
		total := 0
		for _, value := range values {
			total += len(value)
		}
		// One entry is kept even when it alone exceeds the allowance.
		if len(values) == 0 || (len(values) > 1 && total > allowance) {
			t.Errorf("%s: %d entries, %d bytes, want 1 entry or <= %d bytes", name, len(values), total, allowance)
		}
	}
	if response.Structured.ProjectionBudget.LimitationsOmitted == 0 || response.Structured.ProjectionBudget.WarningsOmitted == 0 {
		t.Errorf("cut lists reported omitted %d/%d", response.Structured.ProjectionBudget.LimitationsOmitted, response.Structured.ProjectionBudget.WarningsOmitted)
	}
}
