package genkitruntime

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/llm"
)

// A reply refused by the output-schema check before any validator rule ran
// used to reach the receipt as "unclassified".
func TestSchemaRefusedInterpretDrawNamesTheBrokenStage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		want  contractsv1.ContextFabricInterpretationRejectionReason
	}{
		{"prose", "I could not interpret this question.", contractsv1.ContextFabricInterpretationRejectionOutputNotJSON},
		{"empty object text", " ", contractsv1.ContextFabricInterpretationRejectionOutputNotJSON},
		{"wrong type", `{"shape":5,"requested_judgment":"x","fact_requirements":[],"clarification_needed":false,"time_context":{"axis":"current"}}`, contractsv1.ContextFabricInterpretationRejectionOutputSchemaViolation},
		{"null optional", `{"shape":"open","requested_judgment":"x","fact_requirements":[],"clarification_needed":false,"time_context":{"axis":"current","as_of":null}}`, contractsv1.ContextFabricInterpretationRejectionOutputSchemaViolation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &scriptedLLM{model: "claude-haiku-5-5", kind: llm.KindAnthropic, replies: []llmReply{{result: llm.Result{Text: tc.reply}}}}
			runtime, err := NewWithLLM(Config{
				Provider: "anthropic", Model: "claude-haiku-5-5", InterpretationPromptVersion: "interpret-v1", SynthesisPromptVersion: "synthesis-v1",
				SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1", Timeout: time.Second, MaxAttempts: 1, MaxInputBytes: 128 << 10,
			}, LLMClients{"claude-haiku-5-5": client})
			if err != nil {
				t.Fatal(err)
			}
			_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
			if err == nil {
				t.Fatal("a refused reply was accepted")
			}
			if got := contextfabric.InterpretationRejectionReasonOf(err); got != tc.want {
				t.Errorf("error reason = %q, want %q", got, tc.want)
			}
			if receipt.InterpretationRejectionReason != tc.want {
				t.Errorf("receipt reason = %q, want %q", receipt.InterpretationRejectionReason, tc.want)
			}
		})
	}
}
