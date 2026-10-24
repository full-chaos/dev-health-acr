package falkorfixture

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type fakeContainer struct {
	testcontainers.Container
	terminated   atomic.Int32
	termCtxAlive atomic.Bool
	termErr      error
}

func (f *fakeContainer) Terminate(ctx context.Context, _ ...testcontainers.TerminateOption) error {
	f.terminated.Add(1)
	f.termCtxAlive.Store(ctx.Err() == nil)
	return f.termErr
}

func TestFailedStartTerminatesTheContainerEvenWhenTheCallerContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &fakeContainer{}
	probeErr := errors.New("falkordb not serving: PING: EOF")
	_, err := start(ctx, request("falkordb/falkordb", nil), func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		return fake, probeErr
	})
	if !errors.Is(err, probeErr) {
		t.Fatalf("the start error must be returned: %v", err)
	}
	if got := fake.terminated.Load(); got != 1 {
		t.Fatalf("a container whose start failed must be terminated once, got %d", got)
	}
	if !fake.termCtxAlive.Load() {
		t.Fatal("terminate ran on a cancelled context, which cannot remove the container")
	}
}

func TestFailedStartReportsATerminateFailure(t *testing.T) {
	fake := &fakeContainer{termErr: errors.New("daemon gone")}
	_, err := start(context.Background(), request("falkordb/falkordb", nil), func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		return fake, errors.New("not serving")
	})
	if err == nil || !strings.Contains(err.Error(), "not serving") || !strings.Contains(err.Error(), "daemon gone") {
		t.Fatalf("both the start error and the terminate error must surface: %v", err)
	}
}

func TestFailedStartWithoutAContainerDoesNotPanic(t *testing.T) {
	_, err := start(context.Background(), request("falkordb/falkordb", nil), func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		return nil, errors.New("create container: no daemon")
	})
	if err == nil {
		t.Fatal("the start error must be returned")
	}
}

func TestSuccessfulStartIsNotTerminatedByTheHelper(t *testing.T) {
	fake := &fakeContainer{}
	got, err := start(context.Background(), request("falkordb/falkordb", nil), func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		return fake, nil
	})
	if err != nil || got != testcontainers.Container(fake) {
		t.Fatalf("start must hand the container back: %v", err)
	}
	if fake.terminated.Load() != 0 {
		t.Fatal("a serving container must stay up until the test ends")
	}
}

func TestRequestWaitsForAServingServerAndKeepsLabels(t *testing.T) {
	req := request("img", []Option{WithLabels(map[string]string{"lane": "x"})})
	if req.WaitingFor == nil || !req.Started || req.Labels["lane"] != "x" || len(req.ExposedPorts) != 1 || req.ExposedPorts[0] != "6379/tcp" {
		t.Fatalf("unexpected request: %+v", req)
	}
}

type nilPtrContainer struct{ testcontainers.Container }

func (c *nilPtrContainer) Terminate(context.Context, ...testcontainers.TerminateOption) error {
	_ = c.Container
	return nil
}

func TestFailedStartWithATypedNilContainerDoesNotPanic(t *testing.T) {
	startErr := errors.New("create container: no daemon")
	_, err := start(context.Background(), request("falkordb/falkordb", nil), func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		var typedNil *nilPtrContainer
		return typedNil, startErr
	})
	if !errors.Is(err, startErr) {
		t.Fatalf("the start error must be returned: %v", err)
	}
}

var restartLine = regexp.MustCompile(`^falkordb-fixture: published port unreachable, restarting once port=\S+ tries=\d+ elapsed=\S+$`)

type recordingT struct{ logs []string }

func (r *recordingT) Helper()                 {}
func (r *recordingT) Logf(f string, a ...any) { r.logs = append(r.logs, fmt.Sprintf(f, a...)) }

func TestStartRetriesOnceOnANewPortMappingWhenThePublishedPortIsUnreachable(t *testing.T) {
	first, second := &fakeContainer{}, &fakeContainer{}
	var calls int
	rt := &recordingT{}
	got, err := startRetrying(context.Background(), rt, "falkordb/falkordb", nil, func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		calls++
		if calls == 1 {
			return first, &chfixture.PortUnreachableError{Port: "32785", Tries: 7, Elapsed: 12 * time.Second, Err: fmt.Errorf("wait: %w: PING: i/o timeout", chfixture.ErrPublishedPortUnreachable)}
		}
		return second, nil
	})
	if err != nil || got != testcontainers.Container(second) || calls != 2 {
		t.Fatalf("one retry must hand back the second container: err=%v calls=%d", err, calls)
	}
	if first.terminated.Load() != 1 {
		t.Fatalf("the first container must be terminated, got %d", first.terminated.Load())
	}
	const line = "falkordb-fixture: published port unreachable, restarting once port=32785 tries=7 elapsed=12s"
	if !restartLine.MatchString(rt.logs[0]) {
		t.Fatalf("the restart line must match %s: %q", restartLine, rt.logs[0])
	}
	if len(rt.logs) != 2 || rt.logs[0] != line || !strings.Contains(rt.logs[1], "PING: i/o timeout") {
		t.Fatalf("the retry must be logged with the first failure: %v", rt.logs)
	}
}

func TestStartDoesNotRetryAnyOtherFailureOrMoreThanOnce(t *testing.T) {
	for name, errs := range map[string][]error{
		"not up inside":     {errors.New("falkordb not serving within 2m0s: PING: i/o timeout\ntry 1 at 0ms: PING: EOF; inside the container: exit 1")},
		"unreachable twice": {chfixture.ErrPublishedPortUnreachable, chfixture.ErrPublishedPortUnreachable, nil},
		"create failed":     {errors.New("create container: no daemon")},
	} {
		var calls int
		_, err := startRetrying(context.Background(), &recordingT{}, "falkordb/falkordb", nil, func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
			e := errs[min(calls, len(errs)-1)]
			calls++
			return &fakeContainer{}, e
		})
		want := 1
		if name == "unreachable twice" {
			want = 2
		}
		if name == "not up inside" && (err == nil || !strings.Contains(err.Error(), "try 1 at 0ms")) {
			t.Fatalf("a server that is not up must fail with the try history: %v", err)
		}
		if err == nil || calls != want {
			t.Fatalf("%s: err=%v calls=%d, want a failure after %d call(s)", name, err, calls, want)
		}
	}
}

func TestServerNotUpInsideFailsAtOnceWithTheTryHistoryAndNeverRestarts(t *testing.T) {
	rt := &recordingT{}
	var calls int
	history := "falkordb not serving on localhost:1 within 2m0s: PING: i/o timeout\ntry 1 at 0ms: PING: i/o timeout; inside the container: exit 1"
	_, err := startRetrying(context.Background(), rt, "falkordb/falkordb", nil, func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		calls++
		return &fakeContainer{}, errors.New(history)
	})
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "try 1 at 0ms") || !strings.Contains(err.Error(), "inside the container: exit 1") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	if len(rt.logs) != 0 {
		t.Fatalf("no restart line may be written: %v", rt.logs)
	}
}

func TestStartDoesNotRetryWhenTheFirstContainerCouldNotBeTerminated(t *testing.T) {
	rt := &recordingT{}
	first := &fakeContainer{termErr: errors.New("daemon refused terminate")}
	var calls int
	_, err := startRetrying(context.Background(), rt, "falkordb/falkordb", nil, func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		calls++
		return first, &chfixture.PortUnreachableError{Port: "1", Tries: 1, Elapsed: time.Second, Err: fmt.Errorf("w: %w", chfixture.ErrPublishedPortUnreachable)}
	})
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "daemon refused terminate") {
		t.Fatalf("a failed cleanup must be returned, not retried over: calls=%d err=%v", calls, err)
	}
	if len(rt.logs) != 0 {
		t.Fatalf("no restart line when no restart happens: %v", rt.logs)
	}
}

func unreachableCreate(calls *int, pause time.Duration) startFunc {
	return func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		*calls++
		time.Sleep(pause)
		return &fakeContainer{}, &chfixture.PortUnreachableError{Port: "1", Tries: 1, Elapsed: pause, Err: fmt.Errorf("w: %w", chfixture.ErrPublishedPortUnreachable)}
	}
}

func TestStartRetryNeverExceedsTheTotalBudget(t *testing.T) {
	var budgets []time.Duration
	strategy := func(d time.Duration) wait.Strategy { budgets = append(budgets, d); return nil }
	var calls int
	_, err := startRetryingWithin(context.Background(), &recordingT{}, "falkordb/falkordb", nil, unreachableCreate(&calls, 300*time.Millisecond), strategy, 2*time.Second, time.Second)
	if err == nil || calls != 2 || len(budgets) != 2 {
		t.Fatalf("calls=%d budgets=%v err=%v", calls, budgets, err)
	}
	if budgets[0] != 2*time.Second || budgets[1] > 2*time.Second-300*time.Millisecond || budgets[1] < time.Second {
		t.Fatalf("the retry gets what is left of the total, never more: %v", budgets)
	}
}

func TestStartDoesNotRetryWhenLessThanTheFloorIsLeft(t *testing.T) {
	rt := &recordingT{}
	var calls int
	strategy := func(time.Duration) wait.Strategy { return nil }
	_, err := startRetryingWithin(context.Background(), rt, "falkordb/falkordb", nil, unreachableCreate(&calls, 1500*time.Millisecond), strategy, 2*time.Second, time.Second)
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "no restart") || !errors.Is(err, chfixture.ErrPublishedPortUnreachable) {
		t.Fatalf("with less than the floor left there is no restart: calls=%d err=%v", calls, err)
	}
	if len(rt.logs) != 0 {
		t.Fatalf("no restart line when no restart happens: %v", rt.logs)
	}
}
