package startterm

import (
	"context"
	"fmt"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// callerHookCells are the phases in which a caller's own hook can fail on the ended context before the stage
// hook of that phase would run if it came after the caller's.
var callerHookCells = []struct {
	phase string
	stage string
	hooks func(fail func() error) testcontainers.ContainerLifecycleHooks
}{
	{"PreCreates", "during container create", func(fail func() error) testcontainers.ContainerLifecycleHooks {
		return testcontainers.ContainerLifecycleHooks{PreCreates: []testcontainers.ContainerRequestHook{
			func(context.Context, testcontainers.ContainerRequest) error { return fail() },
		}}
	}},
	{"PostReadies", "during post-ready hooks", func(fail func() error) testcontainers.ContainerLifecycleHooks {
		return testcontainers.ContainerLifecycleHooks{PostReadies: []testcontainers.ContainerHook{
			func(context.Context, testcontainers.Container) error { return fail() },
		}}
	}},
}

// runAllHooks runs every hook of every phase in testcontainers' phase order and, per phase, in slice order, and
// returns the first hook error wrapped as testcontainers wraps it.
func runAllHooks(ctx context.Context, req testcontainers.GenericContainerRequest) error {
	var c testcontainers.Container = &fakeContainer{}
	for _, h := range req.LifecycleHooks {
		for _, f := range h.PreCreates {
			if err := f(ctx, req.ContainerRequest); err != nil {
				return fmt.Errorf("pre-create hook: %w", err)
			}
		}
	}
	for _, h := range req.LifecycleHooks {
		for _, f := range h.PostCreates {
			if err := f(ctx, c); err != nil {
				return fmt.Errorf("post-create hook: %w", err)
			}
		}
	}
	for _, h := range req.LifecycleHooks {
		for _, f := range h.PostStarts {
			if err := f(ctx, c); err != nil {
				return fmt.Errorf("post-start hook: %w", err)
			}
		}
	}
	for _, h := range req.LifecycleHooks {
		for _, f := range h.PostReadies {
			if err := f(ctx, c); err != nil {
				return fmt.Errorf("post-ready hook: %w", err)
			}
		}
	}
	return nil
}

func TestGenericCallerHookThatFailsOnTheEndedContextNamesItsOwnPhase(t *testing.T) {
	const image = "clickhouse/clickhouse-server:26.7"
	for _, cell := range callerHookCells {
		t.Run(cell.phase, func(t *testing.T) {
			lines := captureLogf(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ran := 0
			caller := cell.hooks(func() error { ran++; cancel(); return ctx.Err() })
			req := testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{
				Image: image, LifecycleHooks: []testcontainers.ContainerLifecycleHooks{caller},
			}}
			_, err := generic(ctx, req, func(ctx context.Context, req testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
				return nil, runAllHooks(ctx, req)
			})
			if ran != 1 {
				t.Fatalf("the caller's hook must run once, ran %d", ran)
			}
			requireStageNamed(t, err, *lines, image, cell.stage, "context canceled", context.Canceled)
		})
	}
}

func TestPostgresCallerHookThatFailsOnTheEndedContextNamesItsOwnPhase(t *testing.T) {
	const image = "postgres:18-alpine"
	for _, cell := range callerHookCells {
		t.Run(cell.phase, func(t *testing.T) {
			lines := captureLogf(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ran := 0
			opts := []testcontainers.ContainerCustomizer{
				tcpostgres.WithDatabase("acr"),
				testcontainers.WithAdditionalLifecycleHooks(cell.hooks(func() error { ran++; cancel(); return ctx.Err() })),
			}
			_, err := postgresRun(ctx, image, opts,
				func(ctx context.Context, img string, opts ...testcontainers.ContainerCustomizer) (*tcpostgres.PostgresContainer, error) {
					req := testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: img}}
					for _, opt := range opts {
						if err := opt.Customize(&req); err != nil {
							t.Fatalf("customize: %v", err)
						}
					}
					return nil, fmt.Errorf("run postgres: generic container: %w", runAllHooks(ctx, req))
				})
			if ran != 1 {
				t.Fatalf("the caller's hook must run once, ran %d", ran)
			}
			requireStageNamed(t, err, *lines, image, cell.stage, "context canceled", context.Canceled)
		})
	}
}
