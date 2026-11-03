package falkorfixture

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// CHAOS-12855: this fixture hands testcontainers.GenericContainer to its own budgeted wrapper, so the startterm
// retry did not reach it (Codex r1 P2 on #2006). The image retry runs outside the budget: inside it, its pauses and
// starts stretched the budget on three paths (Codex r2 and r3).

func imageMissing() error {
	daemon := fmt.Errorf("Error response from daemon: No such image: falkordb/falkordb:latest: %w", errdefs.ErrNotFound)
	return fmt.Errorf("create container: container create: %w", daemon)
}

func missingImageCreate(calls *int, servesOn int) startFunc {
	return func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		*calls++
		if servesOn > 0 && *calls >= servesOn {
			return &fakeContainer{}, nil
		}
		return nil, imageMissing()
	}
}

func TestStartServingRunsTheBudgetedStartAgainWhenCreateFindsNoImage(t *testing.T) {
	refused, served := &fakeContainer{}, &fakeContainer{}
	var calls int
	got, err := startServing(context.Background(), &recordingT{}, "falkordb/falkordb", nil, func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		calls++
		if calls == 1 {
			return refused, imageMissing()
		}
		return served, nil
	})
	if err != nil || got != testcontainers.Container(served) || calls != 2 {
		t.Fatalf("container=%v err=%v starts=%d, want the second start to serve", got, err, calls)
	}
	if refused.terminated.Load() != 1 || served.terminated.Load() != 0 {
		t.Fatalf("terminated refused=%d served=%d, want 1 and 0", refused.terminated.Load(), served.terminated.Load())
	}
}

func TestEachBudgetedStartGetsItsWholeBudgetAndTheImageRetryNoneOfIt(t *testing.T) {
	// Every budgeted start begins with the full total; the image retry's pause sits between budgeted starts.
	var budgets []time.Duration
	strategy := func(d time.Duration) wait.Strategy { budgets = append(budgets, d); return nil }
	var calls int
	total := 3 * time.Second
	_, err := startServingWithin(context.Background(), &recordingT{}, "falkordb/falkordb", nil, missingImageCreate(&calls, 2), strategy, total, time.Second)
	if err != nil || calls != 2 || len(budgets) != 2 || budgets[0] != total || budgets[1] != total {
		t.Fatalf("starts=%d budgets=%v err=%v, want two budgeted starts of %s each", calls, budgets, err, total)
	}
}

func TestABudgetedStartNeverRetriesAnImage(t *testing.T) {
	// Codex r2 and r3 on #2006: an image retry inside the budget ran past it on three paths. The invariant: a
	// budgeted start makes no image retry at all, however much budget is left, on the first start or on the
	// port-unreachable restart; the refusal ends it at once, for startServing to start the whole of it again.
	strategy := func(time.Duration) wait.Strategy { return nil }
	var calls int
	began := time.Now()
	_, err := startRetryingWithin(context.Background(), &recordingT{}, "falkordb/falkordb", nil, missingImageCreate(&calls, 0), strategy, time.Minute, time.Second)
	if elapsed := time.Since(began); elapsed > time.Second || calls != 1 || !errdefs.IsNotFound(err) {
		t.Fatalf("first start: elapsed=%s starts=%d err=%v, want the refusal after one start", elapsed, calls, err)
	}

	calls = 0
	restart := func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		calls++
		if calls == 1 {
			return &fakeContainer{}, &chfixture.PortUnreachableError{Port: "1", Tries: 1, Elapsed: time.Millisecond, Err: fmt.Errorf("w: %w", chfixture.ErrPublishedPortUnreachable)}
		}
		return nil, imageMissing()
	}
	began = time.Now()
	_, err = startRetryingWithin(context.Background(), &recordingT{}, "falkordb/falkordb", nil, restart, strategy, time.Minute, time.Second)
	if elapsed := time.Since(began); elapsed > time.Second || calls != 2 || !errdefs.IsNotFound(err) {
		t.Fatalf("restart: elapsed=%s starts=%d err=%v, want the restart's refusal after two starts", elapsed, calls, err)
	}
}

func TestStartServingDoesNotStartAgainOverAContainerItCouldNotRemove(t *testing.T) {
	stuck := &fakeContainer{termErr: errors.New("daemon refused remove")}
	var calls int
	_, err := startServing(context.Background(), &recordingT{}, "falkordb/falkordb", nil, func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		calls++
		if calls == 1 {
			return stuck, imageMissing()
		}
		return &fakeContainer{}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "daemon refused remove") || calls != 1 {
		t.Fatalf("err=%v starts=%d, want the removal failure after one start", err, calls)
	}
}

func TestTheLiveTestsImageRetriedCreateStartsAgainWhenCreateFindsNoImage(t *testing.T) {
	// Codex r2 P3 on #2006: the live readiness test starts through start with imageRetried.
	refused, served := &fakeContainer{}, &fakeContainer{}
	var calls int
	create := func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		calls++
		if calls == 1 {
			return refused, imageMissing()
		}
		return served, nil
	}
	got, err := start(context.Background(), request("falkordb/falkordb", nil), imageRetried(create))
	if err != nil || got != testcontainers.Container(served) || calls != 2 {
		t.Fatalf("container=%v err=%v starts=%d, want the second start to serve", got, err, calls)
	}
	if refused.terminated.Load() != 1 || served.terminated.Load() != 0 {
		t.Fatalf("terminated refused=%d served=%d, want the refused container removed before the next start", refused.terminated.Load(), served.terminated.Load())
	}
}
