package genkitruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestWithRetryTagsOnlyTheModelsOwnDeadline(t *testing.T) {
	t.Parallel()
	blockUntilDone := func(callCtx context.Context) error {
		<-callCtx.Done()
		return callCtx.Err()
	}
	cases := []struct {
		name        string
		parent      func() (context.Context, context.CancelFunc)
		modelBudget time.Duration
		fn          func(context.Context) error
		wantTag     bool
	}{
		{"model deadline shorter than the request's", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 5*time.Second)
		}, time.Second, blockUntilDone, true},
		{"request deadline shorter than the model's", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 20*time.Millisecond)
		}, 5 * time.Second, blockUntilDone, false},
		{"a deadline error the attempt's own context did not raise", func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		}, 5 * time.Second, func(context.Context) error { return context.DeadlineExceeded }, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runtime := mustRuntime(t, &generatorStub{}, Config{MaxAttempts: 1, Timeout: testCase.modelBudget})
			parent, cancel := testCase.parent()
			defer cancel()
			_, err := runtime.withRetry(parent, testCase.fn)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want it to still satisfy context.DeadlineExceeded", err)
			}
			if got := errors.Is(err, contextfabric.ErrModelTimeout); got != testCase.wantTag {
				t.Fatalf("model timeout tag = %v, want %v (err %v)", got, testCase.wantTag, err)
			}
			if got := errors.Is(classifyModelError(err), contextfabric.ErrModelTimeout); got != testCase.wantTag {
				t.Fatalf("tag after classification = %v, want %v", got, testCase.wantTag)
			}
		})
	}
}
