package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestInvestigateQuestionMarkdownShowsDisplayValuesNotRawFieldsOrFloats(t *testing.T) {
	body := envelopeBody(nil)
	risk := 0.993773746751932
	for i := range body.ClaimedFacts {
		if body.ClaimedFacts[i].ClaimID == "claim_status_001" {
			body.ClaimedFacts[i].Field = "compounding_risk"
			body.ClaimedFacts[i].Value = contractsv1.ContextFabricScalarValue{Number: &risk}
		}
	}
	boot := synthesisFixtureBootstrap(t, body, nil, nil)
	result := callInvestigateQuestionRaw(t, boot, map[string]any{"question": "q"})
	if result.IsError {
		t.Fatalf("error result: %+v", result.Content)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var wrapper struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(encoded, &wrapper); err != nil {
		t.Fatal(err)
	}
	var markdown string
	for _, part := range wrapper.Content {
		markdown += part.Text
	}
	if !strings.Contains(markdown, "compounding risk score: 0.99 (Ask Dev, status)") {
		t.Errorf("rendered markdown lacks the display line:\n%s", markdown)
	}
	for _, raw := range []string{"0.993773746751932", "compounding_risk"} {
		if strings.Contains(markdown, raw) {
			t.Errorf("rendered markdown still carries %q:\n%s", raw, markdown)
		}
	}
}
