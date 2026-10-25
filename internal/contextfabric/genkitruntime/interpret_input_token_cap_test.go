package genkitruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretprompt"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/llm"
)

const interpretCapModel = "interpret-cap-model"

func interpretCapRuntime(t *testing.T, tokenCap int, fallback contextfabric.ModelRuntime) (*Runtime, *scriptedLLM) {
	t.Helper()
	client := &scriptedLLM{model: interpretCapModel, replies: []llmReply{{result: llm.Result{
		Text: jsonOf(t, validInterpretationOutput()), Model: interpretCapModel,
		Usage: llm.Usage{InputTokens: intp(10), OutputTokens: intp(4)},
	}}}}
	runtime, err := NewWithLLM(Config{
		Provider: "test", Model: interpretCapModel, ModelVersion: "test-v1",
		InterpretationPromptVersion: "interpret-v1", SynthesisPromptVersion: "synthesis-v1",
		SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1", Timeout: time.Minute,
		MaxAttempts: 1, MaxInputBytes: 512 << 10, MaxInterpretInputTokens: tokenCap, Fallback: fallback,
	}, LLMClients{interpretCapModel: client})
	if err != nil {
		t.Fatalf("NewWithLLM() error = %v", err)
	}
	return runtime, client
}

// paddedInterpretRequest carries three full conversation turns and a fourth of
// the given length, so the payload grows one byte per padding byte within the
// request's own v1 bounds.
func paddedInterpretRequest(padding int) contextfabric.InvestigationRequest {
	request := validRequest()
	created := time.Date(2026, 8, 11, 19, 0, 0, 0, time.UTC)
	for index, length := range []int{12000, 12000, 12000, padding} {
		request.Conversation = append(request.Conversation, contextfabric.ConversationTurn{
			TurnID: fmt.Sprintf("turn_%d", index), Role: contextfabric.ConversationUser,
			Content: strings.Repeat("x", length), CreatedAt: created,
		})
	}
	return request
}

func interpretPayloadBytes(t *testing.T, request contextfabric.InvestigationRequest) int {
	t.Helper()
	encoded, err := interpretprompt.UserPayload(request, math.MaxInt32)
	if err != nil {
		t.Fatal(err)
	}
	return len(encoded)
}

// interpretCapAt returns a cap and two requests: one whose payload is exactly
// the byte bound the cap leaves, and one a single byte over it.
func interpretCapAt(t *testing.T) (tokenCap int, atCap, overCap contextfabric.InvestigationRequest) {
	t.Helper()
	const basePadding = 11000
	base := interpretPayloadBytes(t, paddedInterpretRequest(basePadding))
	overhead := len(interpretprompt.System())
	schema := interpretPromptOverheadBytes() - overhead
	tokenCap = (overhead + schema + base + 2) / 3
	bound := 3*tokenCap - overhead - schema
	atCap = paddedInterpretRequest(basePadding + bound - base)
	overCap = paddedInterpretRequest(basePadding + bound - base + 1)
	if got := interpretPayloadBytes(t, atCap); got != bound {
		t.Fatalf("at-cap payload = %d bytes, want exactly %d", got, bound)
	}
	return tokenCap, atCap, overCap
}

func TestAnInterpretInputOverTheTokenCapIsNotSent(t *testing.T) {
	tokenCap, atCap, overCap := interpretCapAt(t)

	runtime, client := interpretCapRuntime(t, tokenCap, nil)
	_, _, err := runtime.InterpretQuestion(context.Background(), tokenCapPrincipal, overCap)
	var overflow *contextfabric.ModelInputOverflow
	if !errors.As(err, &overflow) || !errors.Is(err, contextfabric.ErrModelInputTooLarge) {
		t.Fatalf("err = %v, want the input overflow", err)
	}
	if got := client.calls(); got != 0 {
		t.Fatalf("model calls over the cap = %d, want 0", got)
	}

	runtime, client = interpretCapRuntime(t, tokenCap, nil)
	if _, _, err := runtime.InterpretQuestion(context.Background(), tokenCapPrincipal, atCap); err != nil {
		t.Fatalf("at the cap: err = %v", err)
	}
	if got := client.calls(); got != 1 {
		t.Fatalf("model calls at the cap = %d, want exactly 1", got)
	}
}

func TestAnInterpretInputWithTheTokenCapOffIsSentWhateverItsEstimate(t *testing.T) {
	_, _, overCap := interpretCapAt(t)
	runtime, client := interpretCapRuntime(t, 0, nil)
	if _, _, err := runtime.InterpretQuestion(context.Background(), tokenCapPrincipal, overCap); err != nil {
		t.Fatal(err)
	}
	if got := client.calls(); got != 1 {
		t.Fatalf("model calls with the cap off = %d, want 1", got)
	}
}

func TestAnInterpretOverTheTokenCapReachesNeitherFallbackNorSecondDraw(t *testing.T) {
	tokenCap, _, overCap := interpretCapAt(t)
	fallback := &interpretCountingFallback{}
	runtime, client := interpretCapRuntime(t, tokenCap, fallback)
	if _, _, err := runtime.InterpretQuestion(context.Background(), tokenCapPrincipal, overCap); !errors.Is(err, contextfabric.ErrModelInputTooLarge) {
		t.Fatalf("err = %v, want the input overflow", err)
	}
	if client.calls() != 0 || fallback.calls.Load() != 0 {
		t.Fatalf("primary calls = %d, fallback calls = %d, want 0 and 0", client.calls(), fallback.calls.Load())
	}
}

func TestAnEnsembleSampleOverTheInterpretTokenCapIsNotSent(t *testing.T) {
	tokenCap, _, overCap := interpretCapAt(t)
	runtime, client := interpretCapRuntime(t, tokenCap, nil)
	for sample := 0; sample < 3; sample++ {
		if _, _, err := runtime.InterpretQuestionForSample(context.Background(), tokenCapPrincipal, overCap, sample); !errors.Is(err, contextfabric.ErrModelInputTooLarge) {
			t.Fatalf("sample %d: err = %v, want the input overflow", sample, err)
		}
	}
	if got := client.calls(); got != 0 {
		t.Fatalf("model calls = %d, want 0", got)
	}
}

// A cap above what the byte bound admits leaves the byte bound alone, and no
// cap value wraps the product.
func TestAnInterpretCapAboveTheByteBoundLeavesTheByteBound(t *testing.T) {
	for _, tokenCap := range []int{estimateInterpretInputTokens(512 << 10), 1 << 40, math.MaxInt / 3, math.MaxInt / 2, math.MaxInt} {
		runtime, _ := interpretCapRuntime(t, tokenCap, nil)
		if got := runtime.interpretInputByteBound(); got != 512<<10 {
			t.Fatalf("cap %d: byte bound = %d, want %d", tokenCap, got, 512<<10)
		}
	}
	runtime, _ := interpretCapRuntime(t, estimateInterpretInputTokens(512<<10)-1, nil)
	if got := runtime.interpretInputByteBound(); got >= 512<<10 || got < 8<<10 {
		t.Fatalf("byte bound just under the byte bound's own cap = %d, want it lowered and at least 8 KiB", got)
	}
}

func TestTheInterpretTokenCapRange(t *testing.T) {
	minimum := MinInterpretInputTokens()
	for _, tokenCap := range []int{-1, 1, minimum - 1} {
		_, err := NewWithLLM(Config{
			Provider: "test", Model: interpretCapModel, MaxInterpretInputTokens: tokenCap,
		}, LLMClients{interpretCapModel: &scriptedLLM{model: interpretCapModel}})
		if err == nil {
			t.Fatalf("cap %d: want a refusal", tokenCap)
		}
	}
	runtime, _ := interpretCapRuntime(t, minimum, nil)
	if got := runtime.interpretInputByteBound(); got < 8<<10 {
		t.Fatalf("byte bound at the minimum cap = %d, want at least 8 KiB", got)
	}
}

type interpretCountingFallback struct{ calls atomic.Int32 }

func (f *interpretCountingFallback) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	f.calls.Add(1)
	return validInterpretedQuestion(), validReceipt(contextfabric.ModelOperationInterpret), nil
}

func (f *interpretCountingFallback) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, errors.New("not used")
}

// The estimate is the whole prompt over three, rounded up, stated here from
// the prompt package and the output schema rather than from the runtime.
func TestTheInterpretEstimateIsTheWholePromptOverThree(t *testing.T) {
	schema, err := json.Marshal(inferSchemaMap(interpretationOutput{}))
	if err != nil {
		t.Fatal(err)
	}
	overhead := len(interpretprompt.System()) + len(schema)
	for _, inputBytes := range []int{0, 1, 2, 3, 8 << 10, 100000, 100001, 100002} {
		if got, want := estimateInterpretInputTokens(inputBytes), (overhead+inputBytes+2)/3; got != want {
			t.Fatalf("estimate(%d) = %d, want %d", inputBytes, got, want)
		}
	}
	if got, want := MinInterpretInputTokens(), (overhead+8<<10+2)/3; got != want {
		t.Fatalf("MinInterpretInputTokens() = %d, want %d", got, want)
	}
}
