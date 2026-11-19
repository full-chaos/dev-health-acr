package startterm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// A start that ran into its caller's deadline once read only "create container: context deadline exceeded" after
// 180 s on a hosted runner; nothing told a stalled image pull from a stalled create, start or readiness wait.

// lifecycleStep is how far a fake start runs the request's lifecycle hooks before it waits for its context to end.
type lifecycleStep int

const (
	beforeHooks lifecycleStep = iota
	afterPreCreate
	afterPostCreate
	afterPostStart
)

// runHooksThenWait runs the hooks testcontainers would run up to step, in its order, then waits for ctx to end and
// returns the error testcontainers wraps a context end in.
func runHooksThenWait(ctx context.Context, t *testing.T, req testcontainers.GenericContainerRequest, c testcontainers.Container, step lifecycleStep) error {
	t.Helper()
	for _, h := range req.LifecycleHooks {
		if step >= afterPreCreate {
			for _, f := range h.PreCreates {
				if err := f(ctx, req.ContainerRequest); err != nil {
					t.Fatalf("pre-create hook: %v", err)
				}
			}
		}
		if step >= afterPostCreate {
			for _, f := range h.PostCreates {
				if err := f(ctx, c); err != nil {
					t.Fatalf("post-create hook: %v", err)
				}
			}
		}
		if step >= afterPostStart {
			for _, f := range h.PostStarts {
				if err := f(ctx, c); err != nil {
					t.Fatalf("post-start hook: %v", err)
				}
			}
		}
	}
	<-ctx.Done()
	return fmt.Errorf("create container: %w", ctx.Err())
}

func captureLogf(t *testing.T) *[]string {
	t.Helper()
	var lines []string
	prev := logf
	logf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { logf = prev })
	return &lines
}

var stageCells = []struct {
	step  lifecycleStep
	stage string
}{
	{beforeHooks, "during image inspect or pull (no create hook ran)"},
	{afterPreCreate, "during container create"},
	{afterPostCreate, "during container start"},
	{afterPostStart, "during readiness wait"},
}

func requireStageNamed(t *testing.T, err error, lines []string, image, stage, cause string, ctxErr error) {
	t.Helper()
	if !errors.Is(err, ctxErr) {
		t.Fatalf("the context error must stay in the chain: %v", err)
	}
	for _, want := range []string{stage, "ended by the caller's context (" + cause + ")"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the start error must name %q: %v", want, err)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("a start ended by its caller's context must log exactly one line, got %d: %q", len(lines), lines)
	}
	for _, want := range []string{stage, cause, fmt.Sprintf("%q", image)} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("the log line must name %q: %s", want, lines[0])
		}
	}
}

func TestGenericStartEndedByTheDeadlineNamesTheStageItWasIn(t *testing.T) {
	const image = "clickhouse/clickhouse-server:26.7"
	for _, cell := range stageCells {
		t.Run(cell.stage, func(t *testing.T) {
			lines := captureLogf(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			fake := &fakeContainer{}
			_, err := generic(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: image}},
				func(ctx context.Context, req testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
					return fake, runHooksThenWait(ctx, t, req, fake, cell.step)
				})
			requireStageNamed(t, err, *lines, image, cell.stage, "context deadline exceeded", context.DeadlineExceeded)
			if got := fake.terminated.Load(); got != 1 {
				t.Fatalf("the container of a start ended by the deadline must still be terminated once, got %d", got)
			}
		})
	}
}

func TestPostgresStartEndedByTheDeadlineNamesTheStageItWasIn(t *testing.T) {
	const image = "postgres:18-alpine"
	for _, cell := range stageCells {
		t.Run(cell.stage, func(t *testing.T) {
			lines := captureLogf(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			_, err := postgresRun(ctx, image, []testcontainers.ContainerCustomizer{tcpostgres.WithDatabase("acr")},
				func(ctx context.Context, img string, opts ...testcontainers.ContainerCustomizer) (*tcpostgres.PostgresContainer, error) {
					req := testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: img}}
					for _, opt := range opts {
						if err := opt.Customize(&req); err != nil {
							t.Fatalf("customize: %v", err)
						}
					}
					return nil, fmt.Errorf("run postgres: generic container: %w", runHooksThenWait(ctx, t, req, &fakeContainer{}, cell.step))
				})
			requireStageNamed(t, err, *lines, image, cell.stage, "context deadline exceeded", context.DeadlineExceeded)
		})
	}
}

func TestStartEndedByACancelNamesTheCancel(t *testing.T) {
	const image = "clickhouse/clickhouse-server:26.7"
	lines := captureLogf(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := generic(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: image}},
		func(ctx context.Context, req testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
			cancel()
			return nil, runHooksThenWait(ctx, t, req, nil, afterPreCreate)
		})
	requireStageNamed(t, err, *lines, image, "during container create", "context canceled", context.Canceled)
}

func TestStartThatFailsWhileTheContextIsLiveIsNotReportedAsEndedByTheCaller(t *testing.T) {
	lines := captureLogf(t)
	probeErr := errors.New("readiness probe failed")
	_, err := generic(context.Background(), testcontainers.GenericContainerRequest{},
		func(ctx context.Context, req testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
			return nil, probeErr
		})
	if err != probeErr {
		t.Fatalf("a start that failed on its own must return its error unchanged: %v", err)
	}
	_, err = postgresRun(context.Background(), "postgres:18-alpine", nil,
		func(context.Context, string, ...testcontainers.ContainerCustomizer) (*tcpostgres.PostgresContainer, error) {
			return nil, probeErr
		})
	if err != probeErr {
		t.Fatalf("a postgres start that failed on its own must return its error unchanged: %v", err)
	}
	if len(*lines) != 0 {
		t.Fatalf("no line may be logged for a start whose context is live: %q", *lines)
	}
}

func TestStageHooksAreAddedWithoutDroppingTheCallersHooks(t *testing.T) {
	ran := 0
	own := testcontainers.ContainerLifecycleHooks{PreCreates: []testcontainers.ContainerRequestHook{
		func(context.Context, testcontainers.ContainerRequest) error { ran++; return nil },
	}}
	callerHooks := make([]testcontainers.ContainerLifecycleHooks, 1, 4)
	callerHooks[0] = own
	req := testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{LifecycleHooks: callerHooks}}
	_, err := generic(context.Background(), req, func(ctx context.Context, got testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		for _, h := range got.LifecycleHooks {
			for _, f := range h.PreCreates {
				_ = f(ctx, got.ContainerRequest)
			}
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ran != 1 {
		t.Fatalf("the caller's own hook must still run once, ran %d", ran)
	}
	if len(callerHooks[:cap(callerHooks)][1].PreCreates) != 0 {
		t.Fatal("the stage hooks were written into the caller's backing array")
	}
}
