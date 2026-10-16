package genkitruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/firebase/genkit/go/core"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/llm"
)

type llmReply struct {
	result llm.Result
	err    error
}

// scriptedLLM is an llm.Provider that records every request and answers the
// i-th with replies[i], repeating the last.
type scriptedLLM struct {
	model   string
	replies []llmReply

	mu       sync.Mutex
	requests []llm.Request
}

func (s *scriptedLLM) Complete(_ context.Context, request llm.Request) (llm.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reply := s.replies[min(len(s.requests), len(s.replies)-1)]
	s.requests = append(s.requests, request)
	return reply.result, reply.err
}
func (s *scriptedLLM) Model() string  { return s.model }
func (s *scriptedLLM) Kind() llm.Kind { return llm.KindLocal }
func (s *scriptedLLM) Close() error   { return nil }

func (s *scriptedLLM) request(i int) llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[i]
}

func jsonOf(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func intp(v int) *int { return &v }

func llmRuntime(t *testing.T, clients LLMClients, phrasingModel string) *Runtime {
	t.Helper()
	runtime, err := NewWithLLM(Config{
		Provider: "openai", Model: "gpt-5.6-luna", PhrasingModel: phrasingModel,
		InterpretationPromptVersion: "interpret-v1", SynthesisPromptVersion: "synthesis-v1",
		SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1", Timeout: time.Second,
		MaxAttempts: 1, MaxInputBytes: 128 << 10,
	}, clients)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func TestLLMInterpretSendsTheSampleSeedAndTheGenkitOutputSchema(t *testing.T) {
	client := &scriptedLLM{model: "gpt-5.6-luna", replies: []llmReply{{result: llm.Result{
		Text: jsonOf(t, validInterpretationOutput()), Model: "gpt-5.6-luna",
		Usage: llm.Usage{InputTokens: intp(40), OutputTokens: intp(18)},
	}}}}
	runtime := llmRuntime(t, LLMClients{"gpt-5.6-luna": client}, "")
	request := validRequest()

	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatal(err)
	}
	sent := client.request(0)
	want := chaos4631InterpretSeedFor(contextfabric.QuestionHash(request.Question), 0)
	if sent.Seed == nil || *sent.Seed != want {
		t.Fatalf("seed = %v, want the sample-0 seed %d", sent.Seed, want)
	}
	if sent.Format == nil || sent.Format.Name != "output" || !reflect.DeepEqual(sent.Format.Schema, core.InferSchemaMap(interpretationOutput{})) {
		t.Fatalf("format = %+v, want name %q and the schema Genkit derived", sent.Format, "output")
	}
	if sent.MaxOutputTokens != DefaultMaxOutputTokens || sent.System == "" || sent.Prompt == "" {
		t.Fatalf("request = max %d, system %d bytes, prompt %d bytes", sent.MaxOutputTokens, len(sent.System), len(sent.Prompt))
	}
	if receipt.Model != "gpt-5.6-luna" || receipt.Provider != "openai" || receipt.Usage != (contextfabric.ModelUsage{InputTokens: 40, OutputTokens: 18, TotalTokens: 58}) {
		t.Fatalf("receipt = %s/%s usage %+v", receipt.Provider, receipt.Model, receipt.Usage)
	}
}

func TestLLMSynthesizeSendsNoSeed(t *testing.T) {
	output := validSynthesisOutput()
	output.ClaimedFacts = []contextfabric.ClaimedFact{}
	client := &scriptedLLM{model: "gpt-5.6-luna", replies: []llmReply{{result: llm.Result{Text: jsonOf(t, output)}}}}
	runtime := llmRuntime(t, LLMClients{"gpt-5.6-luna": client}, "")
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err != nil {
		t.Fatal(err)
	}
	sent := client.request(0)
	if sent.Seed != nil {
		t.Fatalf("synthesis seed = %d, want none", *sent.Seed)
	}
	if sent.Format == nil || !reflect.DeepEqual(sent.Format.Schema, core.InferSchemaMap(synthesisOutput{})) {
		t.Fatal("synthesis format is not the schema Genkit derived")
	}
}

func TestLLMInterpretAcceptsAFencedReplyTheJSONGateRefused(t *testing.T) {
	fenced := "```json\n" + jsonOf(t, validInterpretationOutput()) + "\n```"
	client := &scriptedLLM{model: "gpt-5.6-luna", replies: []llmReply{{result: llm.Result{Unparsed: fenced}}}}
	runtime := llmRuntime(t, LLMClients{"gpt-5.6-luna": client}, "")
	interpreted, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil || interpreted.Shape != contextfabric.ShapeOpen {
		t.Fatalf("interpreted = %+v err = %v, want the fenced answer accepted as Genkit accepted it", interpreted, err)
	}
}

func TestLLMGeneratorRejectsAnInvalidDrawWithItsLenientParse(t *testing.T) {
	gen := llmGenerator{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var err error
	if gen.interpretation, err = newOutputSchema(interpretationOutput{}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		reply      llm.Result
		wantParsed bool
	}{
		{name: "JSON that fails the schema", reply: llm.Result{Text: `{"shape":"open","requested_judgment":"x","time_context":{"axis":"current"},"fact_requirements":[{"kind":"not_a_kind"}],"clarification_needed":false}`}, wantParsed: true},
		{name: "a reply that is not JSON", reply: llm.Result{Unparsed: "I cannot answer that."}, wantParsed: false},
		{name: "an empty reply", reply: llm.Result{}, wantParsed: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gen.clients = LLMClients{"m": &scriptedLLM{model: "m", replies: []llmReply{{result: tc.reply}}}}
			_, _, err := gen.Interpret(context.Background(), generationRequest{Model: "m"})
			var rejection *decodeRejection
			if !errors.As(err, &rejection) || rejection.parsed != tc.wantParsed {
				t.Fatalf("err = %v, want a decodeRejection with parsed=%v", err, tc.wantParsed)
			}
			if !errors.Is(classifyModelError(err), contextfabric.ErrModelOutput) {
				t.Fatalf("classifyModelError(%v) is not ErrModelOutput", err)
			}
		})
	}
}

func TestLLMPhraseUsesThePhrasingModelsClient(t *testing.T) {
	primary := &scriptedLLM{model: "gpt-5.6-luna", replies: []llmReply{{result: llm.Result{Text: `{}`}}}}
	phrasing := &scriptedLLM{model: "gpt-5.6-phrase", replies: []llmReply{{result: llm.Result{Unparsed: "not json"}}}}
	runtime := llmRuntime(t, LLMClients{"gpt-5.6-luna": primary, "gpt-5.6-phrase": phrasing}, "gpt-5.6-phrase")
	_, _, err := runtime.generator.Phrase(context.Background(), generationRequest{Model: runtime.config.PhrasingModelRef})
	if !errors.Is(classifyModelError(err), contextfabric.ErrModelOutput) {
		t.Fatalf("Phrase err = %v, want it classified ErrModelOutput", err)
	}
	if len(phrasing.requests) != 1 || len(primary.requests) != 0 {
		t.Fatalf("phrasing calls %d, primary calls %d, want 1 and 0", len(phrasing.requests), len(primary.requests))
	}
}

func TestNewWithLLMRefusesAClientThatCallsAnotherModel(t *testing.T) {
	if _, err := NewWithLLM(Config{Provider: "openai", Model: "gpt-5.6-luna"}, LLMClients{"gpt-5.6-luna": &scriptedLLM{model: "gpt-5-mini"}}); err == nil {
		t.Fatal("a client keyed gpt-5.6-luna that calls gpt-5-mini was accepted")
	}
	if _, err := NewWithLLM(Config{Provider: "openai", Model: "gpt-5.6-luna", PhrasingModel: "p"}, LLMClients{"gpt-5.6-luna": &scriptedLLM{model: "gpt-5.6-luna"}}); err == nil {
		t.Fatal("a runtime with no client for its phrasing model was accepted")
	}
}

func TestClassifyLLMErrorKeepsTheGenkitTaxonomy(t *testing.T) {
	cases := []struct {
		err  error
		want error
	}{
		{llm.NewError(llm.ClassOutput, llm.KindLocal, "m", "no completion"), contextfabric.ErrModelOutput},
		{llm.NewError(llm.ClassRateLimit, llm.KindLocal, "m", "slow down"), contextfabric.ErrModelRateLimited},
		{llm.NewError(llm.ClassAuth, llm.KindLocal, "m", "bad key"), contextfabric.ErrModelUnavailable},
		{llm.NewError(llm.ClassInvalidRequest, llm.KindLocal, "m", "bad request"), contextfabric.ErrModelUnavailable},
		{llm.NewError(llm.ClassServer, llm.KindLocal, "m", "down"), contextfabric.ErrModelUnavailable},
	}
	for _, tc := range cases {
		got := classifyModelError(tc.err)
		if !errors.Is(got, tc.want) {
			t.Errorf("classifyModelError(%s) = %v, want %v", tc.err, got, tc.want)
		}
		if strings.Contains(got.Error(), "slow down") || strings.Contains(got.Error(), "bad key") {
			t.Errorf("classified error forwards the client's message: %v", got)
		}
	}
}
