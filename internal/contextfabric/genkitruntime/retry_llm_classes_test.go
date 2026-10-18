package genkitruntime

import (
	"context"
	"errors"
	"log/slog"
	"strings"
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

func TestWithRetryOnlyRetriesWhenTheDeadlineCoversTheWaitAndTheNextAttempt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		deadline  time.Duration
		fallback  bool
		wantCalls int
	}{
		{"no deadline retries", 0, false, 2},
		{"room for the wait and one attempt", 3 * time.Second, false, 2},
		{"wait plus attempt does not fit", 1200 * time.Millisecond, false, 1},
		{"room for the fallback attempt too", 4 * time.Second, true, 2},
		{"fallback attempt would be starved", 2200 * time.Millisecond, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			override := Config{MaxAttempts: 2}
			if tc.fallback {
				override.Fallback = erroringFallbackRuntime{err: errors.New("fallback")}
			}
			runtime := mustRuntime(t, &generatorStub{}, override)
			runtime.sleep = (&sleepLog{}).sleep
			ctx := context.Background()
			if tc.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.deadline)
				defer cancel()
			}
			calls := 0
			_, _ = runtime.withRetry(ctx, func(context.Context) error {
				calls++
				return clientError(llm.ClassServer)
			})
			if calls != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}

func TestWithRetryRetriesEveryClientClassAtEveryAttemptBound(t *testing.T) {
	t.Parallel()
	retried := []llm.ErrorClass{llm.ClassRateLimit, llm.ClassServer, llm.ClassTimeout, llm.ClassTransport}
	refused := []llm.ErrorClass{llm.ClassAuth, llm.ClassModelNotFound, llm.ClassInvalidRequest, llm.ClassContextLength, llm.ClassOutput, llm.ClassGeneric, llm.ClassBatchTimeout}
	for maxAttempts := 1; maxAttempts <= 3; maxAttempts++ {
		wantRetried := min(maxAttempts, 2)
		for _, class := range retried {
			runtime := mustRuntime(t, &generatorStub{}, Config{MaxAttempts: maxAttempts})
			runtime.sleep = (&sleepLog{}).sleep
			calls := 0
			_, _ = runtime.withRetry(context.Background(), func(context.Context) error {
				calls++
				return clientError(class)
			})
			if calls != wantRetried {
				t.Fatalf("class %s at MaxAttempts %d: calls = %d, want %d", class, maxAttempts, calls, wantRetried)
			}
		}
		for _, class := range refused {
			runtime := mustRuntime(t, &generatorStub{}, Config{MaxAttempts: maxAttempts})
			runtime.sleep = (&sleepLog{}).sleep
			calls := 0
			_, _ = runtime.withRetry(context.Background(), func(context.Context) error {
				calls++
				return clientError(class)
			})
			if calls != 1 {
				t.Fatalf("class %s at MaxAttempts %d: calls = %d, want 1", class, maxAttempts, calls)
			}
		}
	}
}

func TestSleepContextStopsWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := sleepContext(ctx, time.Minute); !errors.Is(err, context.Canceled) || time.Since(started) > 5*time.Second {
		t.Fatalf("sleepContext on a cancelled context = %v after %s, want context.Canceled at once", err, time.Since(started))
	}
	if err := sleepContext(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("sleepContext = %v, want nil after the wait", err)
	}
}

func TestWithRetryTagsEveryDeclinedClientRetryAsExhausted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		maxAttempts int
		failures    []error
		deadline    time.Duration
		wantAttempt int
	}{
		{"client retry used at MaxAttempts 3", 3, []error{clientError(llm.ClassServer), clientError(llm.ClassServer), nil}, 0, 2},
		{"Retry-After beyond the bound", 3, []error{rateLimitWithRetryAfter(30 * time.Second)}, 0, 1},
		{"deadline too short for the retry", 3, []error{clientError(llm.ClassServer)}, 1200 * time.Millisecond, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runtime := mustRuntime(t, &generatorStub{}, Config{MaxAttempts: tc.maxAttempts})
			runtime.sleep = (&sleepLog{}).sleep
			ctx := context.Background()
			if tc.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.deadline)
				defer cancel()
			}
			call := 0
			outcomes, err := runtime.withRetry(ctx, func(context.Context) error {
				failure := tc.failures[call]
				call++
				return failure
			})
			if len(outcomes) != tc.wantAttempt || !errors.Is(err, contextfabric.ErrModelRetriesExhausted) {
				t.Fatalf("attempts = %d err = %v, want %d attempts and ErrModelRetriesExhausted", len(outcomes), err, tc.wantAttempt)
			}
		})
	}
}

func TestSynthesizeAnswerTagsASharedClientFailureThatUsedItsRetry(t *testing.T) {
	t.Parallel()
	client := &scriptedLLM{model: "gpt-5.6-luna", replies: []llmReply{{err: clientError(llm.ClassServer)}}}
	runtime, _ := retryingLLMRuntime(t, client, 3)
	_, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
	if !errors.Is(err, contextfabric.ErrModelRetriesExhausted) || len(client.requests) != 2 {
		t.Fatalf("err = %v requests = %d, want ErrModelRetriesExhausted after 2 calls at MaxAttempts 3", err, len(client.requests))
	}
}

func TestWithRetryReservesTheFallbackOnlyForACallThatHasOne(t *testing.T) {
	t.Parallel()
	for _, fallbackFollows := range []bool{false, true} {
		runtime := mustRuntime(t, &generatorStub{}, Config{MaxAttempts: 2, Fallback: erroringFallbackRuntime{err: errors.New("fallback")}})
		runtime.sleep = (&sleepLog{}).sleep
		ctx, cancel := context.WithTimeout(context.Background(), 1800*time.Millisecond)
		calls := 0
		_, _ = runtime.withRetryFor(ctx, fallbackFollows, func(context.Context) error {
			calls++
			return rateLimitWithRetryAfter(100 * time.Millisecond)
		})
		cancel()
		want := 2
		if fallbackFollows {
			want = 1
		}
		if calls != want {
			t.Fatalf("fallbackFollows=%v calls = %d, want %d", fallbackFollows, calls, want)
		}
	}
}

func TestPhraseStructureOffersRetriesWithAFallbackConfigured(t *testing.T) {
	t.Parallel()
	client := &scriptedLLM{model: "gpt-5.6-luna", replies: []llmReply{
		{err: rateLimitWithRetryAfter(100 * time.Millisecond)},
		{result: llm.Result{Text: jsonOf(t, phrasingOutput{})}},
	}}
	runtime, _ := retryingLLMRuntime(t, client, 2)
	runtime.config.Fallback = erroringFallbackRuntime{err: errors.New("fallback")}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_, receipt, _ := runtime.PhraseStructureOffers(ctx, storage.Principal{OrgID: "org_1"}, validPhrasingInput())
	if len(client.requests) != 2 || receipt.Attempts != 2 {
		t.Fatalf("requests = %d attempts = %d, want the phrase call retried although a fallback is configured", len(client.requests), receipt.Attempts)
	}
}

func TestRetryDecisionIsLoggedAtInfo(t *testing.T) {
	t.Parallel()
	client := &scriptedLLM{model: "gpt-5.6-luna", replies: []llmReply{
		{err: rateLimitWithRetryAfter(10 * time.Millisecond)},
		{result: llm.Result{Text: jsonOf(t, validInterpretationOutput())}},
	}}
	runtime, _ := retryingLLMRuntime(t, client, 2)
	var buf strings.Builder
	runtime.config.Logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
		t.Fatal(err)
	}
	logged := buf.String()
	for _, want := range []string{`"msg":"context fabric model call retry decision"`, `"error_class":"rate_limit"`, `"wait_ms":10`, `"retrying":true`, `"reason":"wait_then_retry"`} {
		if !strings.Contains(logged, want) {
			t.Fatalf("Info log lacks %s:\n%s", want, logged)
		}
	}
}
