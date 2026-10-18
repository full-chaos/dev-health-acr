package genkitruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/llm"
)

const refusalModel = "gpt-5.6-luna"

func refusalReply() llmReply {
	return llmReply{result: llm.Result{
		Text: "", StopReason: "refusal", Model: refusalModel,
		Usage: llm.Usage{InputTokens: intp(900), OutputTokens: intp(0)},
	}}
}

func refusalRuntime(t *testing.T, primary *scriptedLLM, fallback contextfabric.ModelRuntime) *Runtime {
	t.Helper()
	runtime, err := NewWithLLM(Config{
		Provider: "openai", Model: refusalModel,
		InterpretationPromptVersion: "interpret-v1", SynthesisPromptVersion: "synthesis-v1",
		SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1", Timeout: time.Second,
		MaxAttempts: 3, MaxSynthesisResynthesisAttempts: 3, MaxInputBytes: 128 << 10,
		Fallback: fallback,
	}, LLMClients{refusalModel: primary})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

// A refusal is a terminal outcome of its draw: one provider call per stage,
// the input tokens of that one call on the receipt, no redraw, no retry.
func TestRefusalIsOneProviderCallPerStage(t *testing.T) {
	principal := storage.Principal{OrgID: "org_1"}
	t.Run("interpret", func(t *testing.T) {
		client := &scriptedLLM{model: refusalModel, replies: []llmReply{refusalReply()}}
		_, receipt, err := refusalRuntime(t, client, nil).InterpretQuestion(context.Background(), principal, validRequest())
		if !errors.Is(err, contextfabric.ErrModelOutput) {
			t.Fatalf("err = %v, want ErrModelOutput", err)
		}
		if len(client.requests) != 1 || receipt.Attempts != 1 || receipt.Usage.InputTokens != 900 {
			t.Fatalf("provider calls = %d attempts = %d input tokens = %d, want 1, 1, 900", len(client.requests), receipt.Attempts, receipt.Usage.InputTokens)
		}
	})
	t.Run("synthesize", func(t *testing.T) {
		client := &scriptedLLM{model: refusalModel, replies: []llmReply{refusalReply()}}
		_, receipt, err := refusalRuntime(t, client, nil).SynthesizeAnswer(context.Background(), principal, validSynthesisInput())
		if !errors.Is(err, contextfabric.ErrModelOutput) {
			t.Fatalf("err = %v, want ErrModelOutput", err)
		}
		if len(client.requests) != 1 || receipt.Attempts != 1 || receipt.Usage.InputTokens != 900 {
			t.Fatalf("provider calls = %d attempts = %d input tokens = %d, want 1, 1, 900", len(client.requests), receipt.Attempts, receipt.Usage.InputTokens)
		}
	})
}

// A refusal is not retried on the fallback leg either: the fallback runtime
// sees no call.
func TestRefusalSkipsTheFallbackLeg(t *testing.T) {
	principal := storage.Principal{OrgID: "org_1"}
	fallbackClient := &scriptedLLM{model: refusalModel, replies: []llmReply{{result: llm.Result{Text: jsonOf(t, validInterpretationOutput())}}}}
	fallback := refusalRuntime(t, fallbackClient, nil)
	primary := &scriptedLLM{model: refusalModel, replies: []llmReply{refusalReply()}}
	runtime := refusalRuntime(t, primary, fallback)
	if _, _, err := runtime.InterpretQuestion(context.Background(), principal, validRequest()); !errors.Is(err, contextfabric.ErrModelOutput) {
		t.Fatalf("err = %v, want ErrModelOutput", err)
	}
	if len(primary.requests) != 1 || len(fallbackClient.requests) != 0 {
		t.Fatalf("primary calls = %d fallback calls = %d, want 1 and 0", len(primary.requests), len(fallbackClient.requests))
	}
}

// Control: a reply that fails the output schema is still redrawn within the
// budget.
func TestInvalidSchemaReplyIsStillRedrawn(t *testing.T) {
	client := &scriptedLLM{model: refusalModel, replies: []llmReply{{result: llm.Result{Text: `{"shape":1}`, StopReason: "stop"}}}}
	_, _, err := refusalRuntime(t, client, nil).InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err == nil || len(client.requests) != 3 {
		t.Fatalf("err = %v provider calls = %d, want an error after 3 draws", err, len(client.requests))
	}
}
