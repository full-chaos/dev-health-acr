package genkitruntime

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/full-chaos/dev-health-go/llm"
)

// scriptLLM answers call i with script[i] (the last repeats) as raw reply
// text, so the generator's own output check runs on every draw. It records
// the seed each call sent.
type scriptLLM struct {
	model  string
	script []string
	calls  atomic.Int32

	mu    sync.Mutex
	seeds *[]int64
}

func (s *scriptLLM) Complete(_ context.Context, request llm.Request) (llm.Result, error) {
	i := int(s.calls.Add(1)) - 1
	if i >= len(s.script) {
		i = len(s.script) - 1
	}
	if request.Seed != nil {
		s.mu.Lock()
		*s.seeds = append(*s.seeds, *request.Seed)
		s.mu.Unlock()
	}
	in, out := 10, 4
	return llm.Result{Text: s.script[i], Model: s.model, Usage: llm.Usage{InputTokens: &in, OutputTokens: &out}}, nil
}
func (s *scriptLLM) Model() string  { return s.model }
func (s *scriptLLM) Kind() llm.Kind { return llm.KindLocal }
func (s *scriptLLM) Close() error   { return nil }

func scriptedLLMRuntime(t *testing.T, prefix string, script []string, config Config) (*Runtime, *scriptLLM) {
	t.Helper()
	name := prefix + strings.ReplaceAll(t.Name(), "/", "-")
	client := &scriptLLM{model: name, script: script, seeds: &[]int64{}}
	config.Provider, config.Model, config.ModelVersion = "test", name, "test-v1"
	config.Timeout, config.MaxAttempts, config.MaxInputBytes = 1e9, 1, 128<<10
	runtime, err := NewWithLLM(config, LLMClients{name: client})
	if err != nil {
		t.Fatalf("NewWithLLM() error = %v", err)
	}
	return runtime, client
}
