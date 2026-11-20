package startterm

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// callerHookCells: a caller hook that fails on the ended context in each phase names the stage that phase ran in.
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
	{"PostCreates", "during container create", func(fail func() error) testcontainers.ContainerLifecycleHooks {
		return testcontainers.ContainerLifecycleHooks{PostCreates: []testcontainers.ContainerHook{
			func(context.Context, testcontainers.Container) error { return fail() },
		}}
	}},
	{"PostStarts", "during container start", func(fail func() error) testcontainers.ContainerLifecycleHooks {
		return testcontainers.ContainerLifecycleHooks{PostStarts: []testcontainers.ContainerHook{
			func(context.Context, testcontainers.Container) error { return fail() },
		}}
	}},
	{"PostReadies", "during post-ready hooks", func(fail func() error) testcontainers.ContainerLifecycleHooks {
		return testcontainers.ContainerLifecycleHooks{PostReadies: []testcontainers.ContainerHook{
			func(context.Context, testcontainers.Container) error { return fail() },
		}}
	}},
}

// runAllHooks runs the hooks as testcontainers does: phase by phase in lifecycle order, every callback of a phase
// even after one fails, the joined error returned at the end of the first failing phase.
func runAllHooks(ctx context.Context, req testcontainers.GenericContainerRequest) error {
	return runHooks(ctx, req, true)
}

// runAllPhases is runAllHooks that goes on past a failing phase, to reach callbacks of later phases.
func runAllPhases(ctx context.Context, req testcontainers.GenericContainerRequest) error {
	return runHooks(ctx, req, false)
}

func runHooks(ctx context.Context, req testcontainers.GenericContainerRequest, stopAtFailure bool) error {
	var c testcontainers.Container = &fakeContainer{}
	var failures []error
	phase := func(name string, run func(h testcontainers.ContainerLifecycleHooks) []error) bool {
		var errs []error
		for _, h := range req.LifecycleHooks {
			errs = append(errs, run(h)...)
		}
		if len(errs) == 0 {
			return true
		}
		failures = append(failures, fmt.Errorf("%s hook: %w", name, errors.Join(errs...)))
		return !stopAtFailure
	}
	request := func(hooks []testcontainers.ContainerRequestHook) (errs []error) {
		for _, f := range hooks {
			if err := f(ctx, req.ContainerRequest); err != nil {
				errs = append(errs, err)
			}
		}
		return errs
	}
	container := func(hooks []testcontainers.ContainerHook) (errs []error) {
		for _, f := range hooks {
			if err := f(ctx, c); err != nil {
				errs = append(errs, err)
			}
		}
		return errs
	}
	_ = phase("pre-create", func(h testcontainers.ContainerLifecycleHooks) []error { return request(h.PreCreates) }) &&
		phase("post-create", func(h testcontainers.ContainerLifecycleHooks) []error { return container(h.PostCreates) }) &&
		phase("post-start", func(h testcontainers.ContainerLifecycleHooks) []error { return container(h.PostStarts) }) &&
		phase("post-ready", func(h testcontainers.ContainerLifecycleHooks) []error { return container(h.PostReadies) })
	return errors.Join(failures...)
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
			_, err := postgresRun(ctx, image, opts, fakePostgresRun(t, runAllHooks))
			if ran != 1 {
				t.Fatalf("the caller's hook must run once, ran %d", ran)
			}
			requireStageNamed(t, err, *lines, image, cell.stage, "context canceled", context.Canceled)
		})
	}
}

func fakePostgresRun(t *testing.T, run func(context.Context, testcontainers.GenericContainerRequest) error) postgresFunc {
	return func(ctx context.Context, img string, opts ...testcontainers.ContainerCustomizer) (*tcpostgres.PostgresContainer, error) {
		req := testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: img}}
		for _, opt := range opts {
			if err := opt.Customize(&req); err != nil {
				t.Fatalf("customize: %v", err)
			}
		}
		return nil, fmt.Errorf("run postgres: generic container: %w", run(ctx, req))
	}
}

// A customizer may assign the request's hook list instead of adding to it. Its hooks are still tracked, and its
// backing array is not written.
func TestPostgresCallerCustomizerThatAssignsTheHooksStillNamesItsOwnPhase(t *testing.T) {
	const image = "postgres:18-alpine"
	for _, cell := range callerHookCells {
		t.Run(cell.phase, func(t *testing.T) {
			lines := captureLogf(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			assigned := make([]testcontainers.ContainerLifecycleHooks, 1, 4)
			assigned[0] = cell.hooks(func() error { cancel(); return ctx.Err() })
			assign := testcontainers.CustomizeRequestOption(func(req *testcontainers.GenericContainerRequest) error {
				req.LifecycleHooks = assigned
				return nil
			})
			_, err := postgresRun(ctx, image, []testcontainers.ContainerCustomizer{assign}, fakePostgresRun(t, runAllHooks))
			requireStageNamed(t, err, *lines, image, cell.stage, "context canceled", context.Canceled)
			if len(assigned) != 1 || cap(assigned) != 4 {
				t.Fatalf("the caller's hook slice changed: len %d cap %d", len(assigned), cap(assigned))
			}
			for i, h := range assigned[:cap(assigned)][1:] {
				if len(h.PreCreates)+len(h.PostCreates)+len(h.PostStarts)+len(h.PostReadies) != 0 {
					t.Fatalf("a stage hook was written into the caller's backing array at spare slot %d", i+1)
				}
			}
		})
	}
}

// The first failing caller hook names the stage; a failing hook of a later phase does not overwrite it.
func TestFirstFailingCallerHookNamesTheStage(t *testing.T) {
	const image = "clickhouse/clickhouse-server:26.7"
	lines := captureLogf(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fail := func() error { cancel(); return ctx.Err() }
	first := callerHookCells[0].hooks(fail)
	later := callerHookCells[3].hooks(fail)
	req := testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{
		Image: image, LifecycleHooks: []testcontainers.ContainerLifecycleHooks{first, later},
	}}
	_, err := generic(ctx, req, func(ctx context.Context, req testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		return nil, runAllPhases(ctx, req)
	})
	requireStageNamed(t, err, *lines, image, "during container create", "context canceled", context.Canceled)
}
