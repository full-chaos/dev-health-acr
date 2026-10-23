package falkorfixture

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/testcontainers/testcontainers-go"
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
