package genkitruntime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestAttemptOutcomeClassNamesOnlyTheModelsOwnTimeout(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"model timeout, tag over the deadline", fmt.Errorf("%w: %w", contextfabric.ErrModelTimeout, context.DeadlineExceeded), "timeout"},
		{"bare deadline stays cancelled", context.DeadlineExceeded, "cancelled"},
		{"cancel stays cancelled", context.Canceled, "cancelled"},
		{"unavailable stays unavailable", fmt.Errorf("%w: down", contextfabric.ErrModelUnavailable), "unavailable"},
		{"success", nil, "success"},
	}
	for _, testCase := range cases {
		if got := attemptOutcomeClass(testCase.err); got != testCase.want {
			t.Errorf("%s: attemptOutcomeClass = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

func TestDecisionEventSeparatesTheModelsTimeoutFromTheRequestsDeadline(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		modelBudget   time.Duration
		requestBudget time.Duration
		wantAttempts  string
	}{
		{"model deadline passes first", time.Second, time.Minute, "1:timeout"},
		{"request deadline passes first", time.Minute, 30 * time.Millisecond, "1:cancelled"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			handler, logger := newCaptureLogger()
			generator := &inCallCancelGenerator{started: make(chan struct{})}
			runtime := mustRuntime(t, generator, Config{Logger: logger, MaxAttempts: 1, Timeout: testCase.modelBudget})
			ctx, cancel := context.WithTimeout(context.Background(), testCase.requestBudget)
			defer cancel()

			if _, _, err := runtime.InterpretQuestion(ctx, storage.Principal{OrgID: "org_1"}, validRequest()); err == nil {
				t.Fatal("InterpretQuestion() error = nil, want the deadline surfaced")
			}

			attrs := onlyDecisionEvent(t, handler).Attrs
			if got := attrString(t, attrs, "attempt_outcomes"); got != testCase.wantAttempts {
				t.Fatalf("attempt_outcomes = %q, want %q", got, testCase.wantAttempts)
			}
			if got := attrString(t, attrs, "outcome"); got != "unavailable" {
				t.Fatalf("outcome = %q, want unavailable (the terminal outcome is unchanged)", got)
			}
		})
	}
}
