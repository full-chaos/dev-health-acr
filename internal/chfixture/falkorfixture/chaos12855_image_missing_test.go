package falkorfixture

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// CHAOS-12855 (Codex r1 P2 on #2006): this fixture hands testcontainers.GenericContainer to its own wrapper, so the
// startterm retry did not reach it. A create the daemon refuses because the image is missing after testcontainers'
// pull must be started again here too, and a port-unreachable restart must still follow its own rule.
func TestStartStartsAgainWhenCreateFindsNoImageAfterThePull(t *testing.T) {
	served := &fakeContainer{}
	var calls int
	got, err := startRetrying(context.Background(), &recordingT{}, "falkordb/falkordb", nil, func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		calls++
		if calls == 1 {
			daemon := fmt.Errorf("Error response from daemon: No such image: falkordb/falkordb:latest: %w", errdefs.ErrNotFound)
			return nil, fmt.Errorf("create container: container create: %w", daemon)
		}
		return served, nil
	})
	if err != nil || got != testcontainers.Container(served) || calls != 2 {
		t.Fatalf("a start whose create found no image must be tried again and serve: err=%v calls=%d", err, calls)
	}
	if served.terminated.Load() != 0 {
		t.Fatal("the serving container must stay up")
	}
}

func missingImageCreate(calls *int, servesOn int) startFunc {
	return func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		*calls++
		if servesOn > 0 && *calls >= servesOn {
			return &fakeContainer{}, nil
		}
		daemon := fmt.Errorf("Error response from daemon: No such image: falkordb/falkordb:latest: %w", errdefs.ErrNotFound)
		return nil, fmt.Errorf("create container: container create: %w", daemon)
	}
}

func TestStartImageRetryNeverRunsPastTheTotalBudget(t *testing.T) {
	// Codex r2 P2 on #2006: the image retry's pauses must stay inside the fixture's total budget.
	var calls int
	began := time.Now()
	strategy := func(time.Duration) wait.Strategy { return nil }
	_, err := startRetryingWithin(context.Background(), &recordingT{}, "falkordb/falkordb", nil, missingImageCreate(&calls, 0), strategy, 80*time.Millisecond, 40*time.Millisecond)
	if elapsed := time.Since(began); elapsed > 80*time.Millisecond || calls != 1 || err == nil || !errdefs.IsNotFound(err) {
		t.Fatalf("elapsed=%s starts=%d err=%v, want the refusal inside the 80ms budget after one start", elapsed, calls, err)
	}
	if !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("the refusal must say no start fits before the deadline: %v", err)
	}
}

func TestStartImageRetryWaitsOnlyForWhatIsLeftOfTheBudget(t *testing.T) {
	var budgets []time.Duration
	strategy := func(d time.Duration) wait.Strategy { budgets = append(budgets, d); return nil }
	var calls int
	total := 3 * time.Second
	_, err := startRetryingWithin(context.Background(), &recordingT{}, "falkordb/falkordb", nil, missingImageCreate(&calls, 2), strategy, total, time.Second)
	if err != nil || calls != 2 || len(budgets) != 2 {
		t.Fatalf("starts=%d budgets=%v err=%v, want the second start to serve", calls, budgets, err)
	}
	if budgets[0] != total || budgets[1] <= 0 || budgets[1] > total-2*time.Second {
		t.Fatalf("the start after a pause waits only for what is left of the total: %v", budgets)
	}
}

func TestARefusedStartsContainerIsRemovedBeforeTheNextStart(t *testing.T) {
	refused, served := &fakeContainer{}, &fakeContainer{}
	var calls int
	got, err := startRetrying(context.Background(), &recordingT{}, "falkordb/falkordb", nil, func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		calls++
		if calls == 1 {
			daemon := fmt.Errorf("Error response from daemon: No such image: falkordb/falkordb:latest: %w", errdefs.ErrNotFound)
			return refused, fmt.Errorf("create container: container create: %w", daemon)
		}
		return served, nil
	})
	if err != nil || got != testcontainers.Container(served) || calls != 2 {
		t.Fatalf("container=%v err=%v starts=%d, want the second container", got, err, calls)
	}
	if refused.terminated.Load() != 1 || served.terminated.Load() != 0 {
		t.Fatalf("terminated refused=%d served=%d, want 1 and 0", refused.terminated.Load(), served.terminated.Load())
	}
}

func TestTheBareStartHelperAlsoStartsAgainWhenCreateFindsNoImage(t *testing.T) {
	// Codex r2 P3 on #2006: the live readiness test starts through start, which has no budget.
	var calls int
	got, err := start(context.Background(), request("falkordb/falkordb", nil), missingImageCreate(&calls, 2))
	if err != nil || got == nil || calls != 2 {
		t.Fatalf("container=%v err=%v starts=%d, want the second start to serve", got, err, calls)
	}
}

func TestTheRestartsImageRetryAlsoStaysInsideTheTotalBudget(t *testing.T) {
	// The port-unreachable restart gets what is left of the total; an image refusal on it must not pause past it.
	var calls int
	create := func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		calls++
		if calls == 1 {
			time.Sleep(time.Second)
			return &fakeContainer{}, &chfixture.PortUnreachableError{Port: "1", Tries: 1, Elapsed: time.Second, Err: fmt.Errorf("w: %w", chfixture.ErrPublishedPortUnreachable)}
		}
		daemon := fmt.Errorf("Error response from daemon: No such image: falkordb/falkordb:latest: %w", errdefs.ErrNotFound)
		return nil, fmt.Errorf("create container: container create: %w", daemon)
	}
	total := 2500 * time.Millisecond
	began := time.Now()
	strategy := func(time.Duration) wait.Strategy { return nil }
	_, err := startRetryingWithin(context.Background(), &recordingT{}, "falkordb/falkordb", nil, create, strategy, total, 500*time.Millisecond)
	if elapsed := time.Since(began); elapsed > total || calls != 2 || err == nil || !errdefs.IsNotFound(err) {
		t.Fatalf("elapsed=%s starts=%d err=%v, want the restart's refusal inside the %s budget", elapsed, calls, err, total)
	}
}
