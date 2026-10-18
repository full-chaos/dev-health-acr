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

func clientError(class llm.ErrorClass) *llm.Error {
	return llm.NewError(class, llm.KindLocal, "m", "provider failure")
}

func rateLimitWithRetryAfter(wait time.Duration) *llm.Error {
	err := clientError(llm.ClassRateLimit)
	err.RetryAfter = wait
	return err
}

func TestRetryableByTheSharedClientErrorClass(t *testing.T) {
	t.Parallel()
	cases := []struct {
		class llm.ErrorClass
		want  bool
	}{
		{llm.ClassRateLimit, true},
		{llm.ClassServer, true},
		{llm.ClassTimeout, true},
		{llm.ClassTransport, true},
		{llm.ClassAuth, false},
		{llm.ClassModelNotFound, false},
		{llm.ClassInvalidRequest, false},
		{llm.ClassContextLength, false},
		{llm.ClassOutput, false},
		{llm.ClassGeneric, false},
		{llm.ClassBatchTimeout, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.class), func(t *testing.T) {
			t.Parallel()
			if got := retryable(clientError(tc.class)); got != tc.want {
				t.Fatalf("retryable(class %s) = %v, want %v", tc.class, got, tc.want)
			}
			wrapped := errors.Join(errors.New("outer"), clientError(tc.class))
			if got := retryable(wrapped); got != tc.want {
				t.Fatalf("retryable(wrapped class %s) = %v, want %v", tc.class, got, tc.want)
			}
		})
	}
}

type sleepLog struct{ waits []time.Duration }

func (s *sleepLog) sleep(_ context.Context, d time.Duration) error {
	s.waits = append(s.waits, d)
	return nil
}

func TestWithRetryWaitsBetweenSharedClientRetries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		maxAttempts  int
		failures     []error
		wantAttempts int
		wantWaits    []time.Duration
		wantErr      bool
	}{
		{"rate limit waits its Retry-After then serves", 2, []error{rateLimitWithRetryAfter(2 * time.Second), nil}, 2, []time.Duration{2 * time.Second}, false},
		{"rate limit without Retry-After backs off", 2, []error{clientError(llm.ClassRateLimit), nil}, 2, []time.Duration{500 * time.Millisecond}, false},
		{"a shared-client failure is retried once even at MaxAttempts 3", 3, []error{clientError(llm.ClassServer), clientError(llm.ClassTransport), nil}, 2, []time.Duration{500 * time.Millisecond}, true},
		{"a rate limit is retried once even at MaxAttempts 3", 3, []error{clientError(llm.ClassRateLimit), clientError(llm.ClassRateLimit), nil}, 2, []time.Duration{500 * time.Millisecond}, true},
		{"Retry-After beyond the bound is not waited for", 3, []error{rateLimitWithRetryAfter(30 * time.Second)}, 1, nil, true},
		{"a timeout is retried once whatever the attempt bound", 3, []error{clientError(llm.ClassTimeout), clientError(llm.ClassTimeout), clientError(llm.ClassTimeout)}, 2, []time.Duration{500 * time.Millisecond}, true},
		{"a server error is retried within the bound only", 2, []error{clientError(llm.ClassServer), clientError(llm.ClassServer)}, 2, []time.Duration{500 * time.Millisecond}, true},
		{"refusals and auth are never retried", 3, []error{clientError(llm.ClassOutput)}, 1, nil, true},
		{"insufficient quota (auth class) is never retried", 3, []error{clientError(llm.ClassAuth)}, 1, nil, true},
		{"an invalid request is never retried", 3, []error{clientError(llm.ClassInvalidRequest)}, 1, nil, true},
		{"a context-length refusal is never retried", 3, []error{clientError(llm.ClassContextLength)}, 1, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runtime := mustRuntime(t, &generatorStub{}, Config{MaxAttempts: tc.maxAttempts})
			log := &sleepLog{}
			runtime.sleep = log.sleep
			call := 0
			outcomes, err := runtime.withRetry(context.Background(), func(context.Context) error {
				failure := tc.failures[call]
				call++
				return failure
			})
			if len(outcomes) != tc.wantAttempts {
				t.Fatalf("attempts = %d, want %d", len(outcomes), tc.wantAttempts)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if len(log.waits) != len(tc.wantWaits) {
				t.Fatalf("waits = %v, want %v", log.waits, tc.wantWaits)
			}
			for i, want := range tc.wantWaits {
				if log.waits[i] != want {
					t.Fatalf("waits = %v, want %v", log.waits, tc.wantWaits)
				}
			}
		})
	}
}

func TestWithRetryStopsWhenTheWaitIsInterrupted(t *testing.T) {
	t.Parallel()
	runtime := mustRuntime(t, &generatorStub{}, Config{MaxAttempts: 3})
	runtime.sleep = func(context.Context, time.Duration) error { return context.Canceled }
	calls := 0
	outcomes, err := runtime.withRetry(context.Background(), func(context.Context) error {
		calls++
		return clientError(llm.ClassServer)
	})
	if calls != 1 || len(outcomes) != 1 || err == nil {
		t.Fatalf("calls = %d outcomes = %d err = %v, want one call and the failure returned", calls, len(outcomes), err)
	}
}

func retryingLLMRuntime(t *testing.T, client *scriptedLLM, maxAttempts int) (*Runtime, *sleepLog) {
	t.Helper()
	runtime, err := NewWithLLM(Config{
		Provider: "openai", Model: "gpt-5.6-luna",
		InterpretationPromptVersion: "interpret-v1", SynthesisPromptVersion: "synthesis-v1",
		SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1", Timeout: time.Second,
		MaxAttempts: maxAttempts, MaxInputBytes: 128 << 10,
	}, LLMClients{"gpt-5.6-luna": client})
	if err != nil {
		t.Fatal(err)
	}
	log := &sleepLog{}
	runtime.sleep = log.sleep
	return runtime, log
}

func TestInterpretServesTheAnswerAfterARateLimitedFirstCall(t *testing.T) {
	t.Parallel()
	client := &scriptedLLM{model: "gpt-5.6-luna", replies: []llmReply{
		{err: rateLimitWithRetryAfter(time.Second)},
		{result: llm.Result{Text: jsonOf(t, validInterpretationOutput()), Model: "gpt-5.6-luna", Usage: llm.Usage{InputTokens: intp(40), OutputTokens: intp(18)}}},
	}}
	runtime, log := retryingLLMRuntime(t, client, 2)
	interpreted, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil || interpreted.Shape != contextfabric.ShapeOpen {
		t.Fatalf("interpreted = %+v err = %v, want the answer served after one retried 429", interpreted, err)
	}
	if len(client.requests) != 2 || receipt.Attempts != 2 || len(log.waits) != 1 || log.waits[0] != time.Second {
		t.Fatalf("requests = %d attempts = %d waits = %v, want 2 calls, 2 attempts and one 1s wait", len(client.requests), receipt.Attempts, log.waits)
	}
}

func TestSynthesizeServesTheAnswerAfterAServerErrorFirstCall(t *testing.T) {
	t.Parallel()
	output := validSynthesisOutput()
	output.ClaimedFacts = []contextfabric.ClaimedFact{}
	client := &scriptedLLM{model: "gpt-5.6-luna", replies: []llmReply{
		{err: clientError(llm.ClassServer)},
		{result: llm.Result{Text: jsonOf(t, output)}},
	}}
	runtime, log := retryingLLMRuntime(t, client, 2)
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput()); err != nil {
		t.Fatalf("err = %v, want the answer served after one retried 5xx", err)
	}
	if len(client.requests) < 2 || len(log.waits) != 1 || log.waits[0] != 500*time.Millisecond {
		t.Fatalf("requests = %d waits = %v, want a retried call and one 500ms wait", len(client.requests), log.waits)
	}
}

func TestInterpretDoesNotRetryAModelRefusal(t *testing.T) {
	t.Parallel()
	client := &scriptedLLM{model: "gpt-5.6-luna", replies: []llmReply{{err: clientError(llm.ClassOutput)}}}
	runtime, log := retryingLLMRuntime(t, client, 2)
	_, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if !errors.Is(err, contextfabric.ErrModelOutput) {
		t.Fatalf("err = %v, want ErrModelOutput", err)
	}
	if len(client.requests) != 1 || len(log.waits) != 0 {
		t.Fatalf("requests = %d waits = %v, want 1 call and no wait for a refusal", len(client.requests), log.waits)
	}
}
