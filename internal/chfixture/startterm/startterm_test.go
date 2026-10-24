package startterm

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
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

type nilPtrContainer struct{ testcontainers.Container }

func (c *nilPtrContainer) Terminate(context.Context, ...testcontainers.TerminateOption) error {
	_ = c.Container
	return nil
}

func genericReturning(c testcontainers.Container, err error) genericFunc {
	return func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		return c, err
	}
}

func postgresReturning(c *tcpostgres.PostgresContainer, err error) postgresFunc {
	return func(context.Context, string, ...testcontainers.ContainerCustomizer) (*tcpostgres.PostgresContainer, error) {
		return c, err
	}
}

func TestGenericFailedStartTerminatesOnceEvenWhenTheCallerContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &fakeContainer{}
	probeErr := errors.New("clickhouse not serving")
	_, err := generic(ctx, testcontainers.GenericContainerRequest{}, genericReturning(fake, probeErr))
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

func TestGenericFailedStartReportsATerminateFailure(t *testing.T) {
	fake := &fakeContainer{termErr: errors.New("daemon gone")}
	_, err := generic(context.Background(), testcontainers.GenericContainerRequest{}, genericReturning(fake, errors.New("not serving")))
	if err == nil || !strings.Contains(err.Error(), "not serving") || !strings.Contains(err.Error(), "daemon gone") {
		t.Fatalf("both the start error and the terminate error must surface: %v", err)
	}
}

func TestGenericFailedStartWithNilAndTypedNilContainersDoesNotPanic(t *testing.T) {
	startErr := errors.New("create container: no daemon")
	var typedNil *nilPtrContainer
	for name, c := range map[string]testcontainers.Container{"nil": nil, "typed nil": typedNil} {
		_, err := generic(context.Background(), testcontainers.GenericContainerRequest{}, genericReturning(c, startErr))
		if !errors.Is(err, startErr) {
			t.Fatalf("%s: the start error must be returned: %v", name, err)
		}
	}
}

func TestGenericSuccessfulStartIsNotTerminated(t *testing.T) {
	fake := &fakeContainer{}
	got, err := generic(context.Background(), testcontainers.GenericContainerRequest{}, genericReturning(fake, nil))
	if err != nil || got != testcontainers.Container(fake) {
		t.Fatalf("the container must be handed back: %v", err)
	}
	if fake.terminated.Load() != 0 {
		t.Fatal("a serving container must stay up until the test ends")
	}
}

func TestPostgresFailedStartTerminatesTheModuleContainerOnceOnAFreshContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &fakeContainer{}
	runErr := errors.New("inspect postgres: boom")
	pc, err := postgresRun(ctx, "img", nil, postgresReturning(&tcpostgres.PostgresContainer{Container: fake}, runErr))
	if !errors.Is(err, runErr) || pc == nil {
		t.Fatalf("the start error and the wrapper must be returned: pc=%v err=%v", pc, err)
	}
	if fake.terminated.Load() != 1 || !fake.termCtxAlive.Load() {
		t.Fatalf("terminate count=%d freshCtx=%v", fake.terminated.Load(), fake.termCtxAlive.Load())
	}
}

func TestPostgresFailedStartWithoutAWrapperDoesNotPanic(t *testing.T) {
	runErr := errors.New("run postgres: no daemon")
	if _, err := postgresRun(context.Background(), "img", nil, postgresReturning(nil, runErr)); !errors.Is(err, runErr) {
		t.Fatalf("the start error must be returned: %v", err)
	}
}

func TestPostgresSuccessfulStartIsNotTerminated(t *testing.T) {
	fake := &fakeContainer{}
	if _, err := postgresRun(context.Background(), "img", nil, postgresReturning(&tcpostgres.PostgresContainer{Container: fake}, nil)); err != nil {
		t.Fatal(err)
	}
	if fake.terminated.Load() != 0 {
		t.Fatal("a serving container must stay up until the test ends")
	}
}
