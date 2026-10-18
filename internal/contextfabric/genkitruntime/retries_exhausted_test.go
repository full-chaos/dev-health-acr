package genkitruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The runtime says its attempt budget is used only when the last attempt ended
// on a retryable class; the fact survives classification, which drops the
// cause.
func TestWithRetryNamesAnExhaustedAttemptBudget(t *testing.T) {
	t.Parallel()
	retryable := newModelStatusError(statusUnavailable, "provider down")
	cases := []struct {
		name         string
		maxAttempts  int
		failures     []error
		wantAttempts int
		wantTag      bool
	}{
		{"every attempt retryable", 2, []error{retryable, retryable}, 2, true},
		{"one attempt allowed, retryable", 1, []error{retryable}, 1, true},
		{"retryable then cancelled on the last attempt", 2, []error{retryable, context.Canceled}, 2, true},
		{"first of two attempts not retryable", 2, []error{newModelStatusError(statusInvalidArgument, "bad")}, 1, false},
		{"a later attempt serves", 2, []error{retryable, nil}, 2, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runtime := mustRuntime(t, &generatorStub{}, Config{MaxAttempts: testCase.maxAttempts})
			call := 0
			outcomes, err := runtime.withRetry(context.Background(), func(context.Context) error {
				failure := testCase.failures[call]
				call++
				return failure
			})
			if len(outcomes) != testCase.wantAttempts {
				t.Fatalf("attempts = %d, want %d", len(outcomes), testCase.wantAttempts)
			}
			if got := errors.Is(err, contextfabric.ErrModelRetriesExhausted); got != testCase.wantTag {
				t.Fatalf("exhausted tag = %v, want %v (err %v)", got, testCase.wantTag, err)
			}
			if err != nil {
				classified := classifyModelError(err)
				if got := errors.Is(classified, contextfabric.ErrModelRetriesExhausted); got != testCase.wantTag {
					t.Fatalf("classified exhausted tag = %v, want %v (err %v)", got, testCase.wantTag, classified)
				}
			}
		})
	}
}
