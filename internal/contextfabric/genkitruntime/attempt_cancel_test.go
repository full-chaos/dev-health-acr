package genkitruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/llm"
)

// CHAOS-11911 (INV-7): a cancelled run starts no model attempt. The run is
// cancelled while the runtime waits out the backoff after a retryable
// failure; the wait must end at the cancel (the production sleep is
// cancel-aware) and no second attempt may start. Each attempt that starts
// logs one `context fabric model attempt started` line, so the proof is read
// from the logs, the way the live proof reads it: every started_at is before
// the cancel, and there is exactly one.
func TestNoModelAttemptStartsAfterTheRunIsCancelledInTheBackoff(t *testing.T) {
	var (
		logMu sync.Mutex
		logs  bytes.Buffer
	)
	runtime := mustRuntime(t, &generatorStub{}, Config{MaxAttempts: 3, Logger: slog.New(slog.NewJSONHandler(&lockedWriter{mu: &logMu, w: &logs}, nil))})
	runtime.now = time.Now
	runtime.sleep = sleepContext

	const requestID = "req_0123456789abcdef0123456789abcdef"
	ctx, cancel := context.WithCancel(observability.WithRequestID(context.Background(), requestID))
	defer cancel()
	var cancelledAt time.Time
	calls := 0
	started := time.Now()
	_, err := runtime.withRetry(ctx, func(context.Context) error {
		calls++
		// A retryable shared-client failure: the runtime waits 500 ms
		// before the next attempt. The run is cancelled 50 ms into it.
		time.AfterFunc(50*time.Millisecond, func() {
			logMu.Lock()
			cancelledAt = time.Now()
			logMu.Unlock()
			cancel()
		})
		return clientError(llm.ClassServer)
	})
	waited := time.Since(started)
	if err == nil {
		t.Fatal("withRetry returned no error for a run cancelled in its backoff")
	}
	if calls != 1 {
		t.Fatalf("the model was called %d times; a cancelled run starts no further attempt", calls)
	}
	if waited >= baseLLMRetryWait {
		t.Fatalf("withRetry returned after %s, not at the cancel: the backoff did not see the cancellation", waited)
	}

	logMu.Lock()
	defer logMu.Unlock()
	var attempts []time.Time
	for _, raw := range strings.Split(logs.String(), "\n") {
		var line map[string]any
		if json.Unmarshal([]byte(raw), &line) != nil || line["msg"] != "context fabric model attempt started" {
			continue
		}
		if line["request_id"] != requestID {
			t.Fatalf("attempt line %v does not carry the request id", line)
		}
		at, err := time.Parse(time.RFC3339Nano, line["started_at"].(string))
		if err != nil {
			t.Fatal(err)
		}
		attempts = append(attempts, at)
	}
	if len(attempts) != 1 {
		t.Fatalf("%d attempt lines, want exactly 1:\n%s", len(attempts), logs.String())
	}
	if cancelledAt.IsZero() || !attempts[0].Before(cancelledAt) {
		t.Fatalf("attempt started at %s, cancel at %s: an attempt started after the cancel", attempts[0], cancelledAt)
	}
}

// TestEveryModelAttemptLogsItsStart: two attempts, two lines, in order.
func TestEveryModelAttemptLogsItsStart(t *testing.T) {
	var logs bytes.Buffer
	runtime := mustRuntime(t, &generatorStub{}, Config{MaxAttempts: 2, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	runtime.sleep = (&sleepLog{}).sleep
	calls := 0
	if _, err := runtime.withRetry(context.Background(), func(context.Context) error {
		calls++
		if calls == 1 {
			return clientError(llm.ClassServer)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var numbers []float64
	for _, raw := range strings.Split(logs.String(), "\n") {
		var line map[string]any
		if json.Unmarshal([]byte(raw), &line) == nil && line["msg"] == "context fabric model attempt started" {
			numbers = append(numbers, line["attempt"].(float64))
		}
	}
	if len(numbers) != 2 || numbers[0] != 1 || numbers[1] != 2 {
		t.Fatalf("attempt lines %v, want attempts 1 and 2", numbers)
	}
}

// TestTheModelAttemptLineCarriesNoInputValue: the attempt line has a fixed
// key set, and its request id is the context's, never the operation input's,
// so a forged input request id reaches no attempt line (the CHAOS-5544
// sanitizer tables count only the lines an input can reach).
func TestTheModelAttemptLineCarriesNoInputValue(t *testing.T) {
	var logs bytes.Buffer
	runtime := mustRuntime(t, &generatorStub{synthesis: validSynthesisOutput()}, Config{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	input := validSynthesisInput()
	input.Request.RequestID = "req_a\r\n{\"level\":\"INFO\",\"msg\":\"forged\"}"
	const contextRequestID = "req_0123456789abcdef0123456789abcdef"
	ctx := observability.WithRequestID(context.Background(), contextRequestID)
	if _, _, err := runtime.SynthesizeAnswer(ctx, storage.Principal{OrgID: "org_1"}, input); err != nil {
		t.Fatalf("SynthesizeAnswer: %v", err)
	}
	attempts := 0
	for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("log line %q is not one JSON object: %v", raw, err)
		}
		if !isModelAttemptLine(line) {
			continue
		}
		attempts++
		keys := make([]string, 0, len(line))
		for key := range line {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"attempt", "level", "msg", "request_id", "started_at", "time"}) {
			t.Fatalf("attempt line keys %v, want exactly attempt, level, msg, request_id, started_at, time", keys)
		}
		if line["request_id"] != contextRequestID {
			t.Fatalf("attempt line request_id %v, want the context's %q", line["request_id"], contextRequestID)
		}
	}
	if attempts == 0 {
		t.Fatalf("no attempt line was logged:\n%s", logs.String())
	}
}

// lockedWriter serializes writes from the cancel timer and the runtime.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
