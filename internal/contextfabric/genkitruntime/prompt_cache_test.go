package genkitruntime

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/interpretprompt"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	"github.com/full-chaos/dev-health-go/llm"
)

func cacheTestGenerator(t *testing.T, promptCache bool, logs *strings.Builder, client *scriptedLLM) llmGenerator {
	t.Helper()
	gen := llmGenerator{promptCache: promptCache, clients: LLMClients{client.model: client}, logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	var err error
	if gen.interpretation, err = newOutputSchema(interpretationOutput{}, true); err != nil {
		t.Fatal(err)
	}
	if gen.synthesis, err = newOutputSchema(synthesisOutput{}, false); err != nil {
		t.Fatal(err)
	}
	if gen.phrasing, err = newOutputSchema(phrasingOutput{}, true); err != nil {
		t.Fatal(err)
	}
	return gen
}

func cacheTestClient(t *testing.T, kind llm.Kind, usage llm.Usage) *scriptedLLM {
	t.Helper()
	return &scriptedLLM{model: "m", kind: kind, replies: []llmReply{{result: llm.Result{
		Text: "{}", Usage: usage, FormatPath: llm.FormatPathSchema,
	}}}}
}

// callAll runs one interpret, one synthesize and one phrase call; the replies
// are not valid output, so every call errors after the model call and the
// test reads the requests the client saw.
func callAll(gen llmGenerator) {
	for _, call := range []func(context.Context, generationRequest) error{
		func(ctx context.Context, r generationRequest) error { _, _, err := gen.Interpret(ctx, r); return err },
		func(ctx context.Context, r generationRequest) error { _, _, err := gen.Synthesize(ctx, r); return err },
		func(ctx context.Context, r generationRequest) error { _, _, err := gen.Phrase(ctx, r); return err },
	} {
		_ = call(context.Background(), generationRequest{Model: "m", System: "system text", Prompt: "p"})
	}
}

func TestPromptCacheMarksInterpretAndSynthesizeOnAnthropicOnly(t *testing.T) {
	for _, c := range []struct {
		name  string
		kind  llm.Kind
		cache bool
		want  [3]llm.CachePolicy
	}{
		{"anthropic platform key", llm.KindAnthropic, true, [3]llm.CachePolicy{llm.CacheSystem, llm.CacheSystem, llm.CacheNone}},
		{"anthropic own key", llm.KindAnthropic, false, [3]llm.CachePolicy{}},
		{"openai-compatible", llm.KindLocal, true, [3]llm.CachePolicy{}},
		{"gemini", llm.KindGemini, true, [3]llm.CachePolicy{}},
		{"qwen", llm.KindQwen, true, [3]llm.CachePolicy{}},
		{"openai", llm.KindOpenAI, true, [3]llm.CachePolicy{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var logs strings.Builder
			client := cacheTestClient(t, c.kind, llm.Usage{})
			callAll(cacheTestGenerator(t, c.cache, &logs, client))
			if len(client.requests) != 3 {
				t.Fatalf("requests = %d, want 3", len(client.requests))
			}
			for i, want := range c.want {
				if got := client.request(i).Cache; got != want {
					t.Errorf("call %d cache = %v, want %v", i, got, want)
				}
			}
		})
	}
}

func TestPromptCacheSendsTheSystemPromptUnchanged(t *testing.T) {
	var logs strings.Builder
	client := cacheTestClient(t, llm.KindAnthropic, llm.Usage{})
	gen := cacheTestGenerator(t, true, &logs, client)
	_, _, _ = gen.Interpret(context.Background(), generationRequest{Model: "m", System: interpretprompt.System(), Prompt: "q1"})
	_, _, _ = gen.Interpret(context.Background(), generationRequest{Model: "m", System: interpretprompt.System(), Prompt: "q2 differs"})
	if client.request(0).System != interpretprompt.System() || client.request(0).System != client.request(1).System {
		t.Error("the Anthropic system text is not the prompt constant, byte for byte, on every call")
	}
}

func TestSystemPromptsAreByteStable(t *testing.T) {
	if interpretprompt.System() == "" || synthesisprompt.System() == "" {
		t.Fatal("empty system prompt")
	}
	for _, c := range []struct{ name, first, second string }{
		{"interpret", interpretprompt.System(), interpretprompt.System()},
		{"synthesize", synthesisprompt.System(), synthesisprompt.System()},
	} {
		if c.first != c.second {
			t.Errorf("%s system prompt changed between calls", c.name)
		}
	}
}

func TestLLMCallLineCarriesCacheCountsOnlyWhenReported(t *testing.T) {
	for _, c := range []struct {
		name   string
		usage  llm.Usage
		want   []string
		absent []string
	}{
		{"write", llm.Usage{InputTokens: intp(120), OutputTokens: intp(7), CacheCreationInputTokens: intp(100), CachedInputTokens: intp(0)},
			[]string{"input_tokens=120", "output_tokens=7", "cache_creation_input_tokens=100", "cached_input_tokens=0", "operation=interpret", "llm_kind=anthropic", "model=m", "format_path=schema"}, nil},
		{"unreported", llm.Usage{InputTokens: intp(120), OutputTokens: intp(7)},
			[]string{"input_tokens=120"}, []string{"cache_creation_input_tokens", "cached_input_tokens"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var logs strings.Builder
			client := cacheTestClient(t, llm.KindAnthropic, c.usage)
			gen := cacheTestGenerator(t, true, &logs, client)
			_, _, _ = gen.Interpret(context.Background(), generationRequest{Model: "m", System: "SECRET_SYSTEM", Prompt: "SECRET_PROMPT"})
			var line string
			for _, l := range strings.Split(logs.String(), "\n") {
				if strings.Contains(l, `msg=llm.call`) {
					line = l
				}
			}
			if line == "" || strings.Count(logs.String(), "msg=llm.call") != 1 {
				t.Fatalf("want one llm.call line in: %s", logs.String())
			}
			for _, w := range c.want {
				if !strings.Contains(line, w) {
					t.Errorf("line lacks %q: %s", w, line)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(line, a) {
					t.Errorf("line carries %q though not reported: %s", a, line)
				}
			}
			if strings.Contains(logs.String(), "SECRET_") {
				t.Errorf("prompt text reached the log: %s", logs.String())
			}
		})
	}
}

func TestLLMCallLineIsWrittenOnTheRefusalPath(t *testing.T) {
	var logs strings.Builder
	client := &scriptedLLM{model: "m", kind: llm.KindAnthropic, replies: []llmReply{{result: llm.Result{
		StopReason: llmRefusalStopReason, Usage: llm.Usage{InputTokens: intp(50), OutputTokens: intp(1), CacheCreationInputTokens: intp(40)},
	}}}}
	gen := cacheTestGenerator(t, true, &logs, client)
	_, _, err := gen.Interpret(context.Background(), generationRequest{Model: "m", System: "s", Prompt: "p"})
	if err == nil {
		t.Fatal("a refusal returned no error")
	}
	if strings.Count(logs.String(), "msg=llm.call") != 1 || !strings.Contains(logs.String(), "cache_creation_input_tokens=40") {
		t.Fatalf("refusal path lost the llm.call line: %s", logs.String())
	}
}
