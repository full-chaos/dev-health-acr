package genkitruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
		{"null optional", `{"shape":"open","requested_judgment":"x","fact_requirements":[],"clarification_needed":false,"time_context":{"axis":"current"},"subject_terms":null}`, contractsv1.ContextFabricInterpretationRejectionOutputSchemaViolation},
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

func TestSchemaRejectionReasonNamesEveryStage(t *testing.T) {
	for stage, want := range map[string]contractsv1.ContextFabricInterpretationRejectionReason{
		"empty":    contractsv1.ContextFabricInterpretationRejectionOutputNotJSON,
		"not_json": contractsv1.ContextFabricInterpretationRejectionOutputNotJSON,
		"schema":   contractsv1.ContextFabricInterpretationRejectionOutputSchemaViolation,
		"decode":   contractsv1.ContextFabricInterpretationRejectionOutputDecodeFailed,
	} {
		cause := fmt.Errorf("draw: %w", &outputMismatch{stage: stage})
		if !errors.Is(cause, errOutputSchemaMismatch) {
			t.Errorf("%s: a stage error must still match errOutputSchemaMismatch", stage)
		}
		rejection := contextfabric.NewInterpretationRejection(schemaRejectionReason(cause), cause)
		if got := contextfabric.InterpretationRejectionReasonOf(rejection); got != want {
			t.Errorf("%s: reason = %q, want %q", stage, got, want)
		}
	}
	if got := schemaRejectionReason(errors.New("not a mismatch")); got != contractsv1.ContextFabricInterpretationRejectionOutputSchemaViolation {
		t.Errorf("a cause with no stage = %q, want the schema violation reason", got)
	}
}

type levelRecorder struct {
	slog.Handler
	levels map[string]slog.Level
}

func (r *levelRecorder) Handle(ctx context.Context, record slog.Record) error {
	r.levels[record.Message] = record.Level
	return nil
}

func TestSchemaRefusalLineIsWrittenAtInfo(t *testing.T) {
	recorder := &levelRecorder{Handler: slog.NewTextHandler(io.Discard, nil), levels: map[string]slog.Level{}}
	gen := llmGenerator{logger: slog.New(recorder)}
	var err error
	if gen.interpretation, err = newOutputSchema(interpretationOutput{}); err != nil {
		t.Fatal(err)
	}
	gen.clients = LLMClients{"m": &scriptedLLM{model: "m", replies: []llmReply{{result: llm.Result{Text: `{"shape":5}`}}}}}
	if _, _, err := gen.Interpret(context.Background(), generationRequest{Model: "m"}); err == nil {
		t.Fatal("an invalid reply was accepted")
	}
	if level, ok := recorder.levels[llmDrawRejectedReason]; !ok || level != slog.LevelInfo {
		t.Fatalf("schema refusal line level = %v (logged %v), want Info", level, ok)
	}
}
