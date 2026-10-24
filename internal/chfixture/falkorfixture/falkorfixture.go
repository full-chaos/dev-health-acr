// Package falkorfixture starts FalkorDB test containers. It lives apart from chfixture so the container-start
// gate (RequireContainers) can be imported by the one function that starts the container.
package falkorfixture

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/startterm"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type startFunc func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error)

// Option changes the container request.
type Option func(*testcontainers.ContainerRequest)

// WithLabels adds container labels.
func WithLabels(labels map[string]string) Option {
	return func(r *testcontainers.ContainerRequest) { r.Labels = labels }
}

// Start runs a FalkorDB container that is serving, and terminates it when the test ends. A failed start
// (readiness timeout, probe error, cancelled context) terminates the container before the test fails, because
// testcontainers returns the container with the start error and does not remove it.
func Start(ctx context.Context, t testing.TB, image string, opts ...Option) testcontainers.Container {
	t.Helper()
	chfixture.RequireContainers(t)
	container, err := startRetrying(ctx, t, image, opts, testcontainers.GenericContainer)
	if err != nil {
		t.Fatalf("start FalkorDB container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Errorf("terminate FalkorDB container: %v", err)
		}
	})
	return container
}

const (
	serveBudget    = 2 * time.Minute
	minRetryBudget = 45 * time.Second
)

type logger interface {
	Helper()
	Logf(string, ...any)
}

type strategyFunc func(time.Duration) wait.Strategy

func startRetrying(ctx context.Context, t logger, image string, opts []Option, create startFunc) (testcontainers.Container, error) {
	t.Helper()
	return startRetryingWithin(ctx, t, image, opts, create, chfixture.FalkorReadyWithin, serveBudget, minRetryBudget)
}

// startRetryingWithin starts a container and, only when the wait proved the server answers inside the container
// while its published port never did and the failed container was terminated cleanly, starts one more on a new port
// mapping with what is left of total, provided at least floor is left. Every other failure is final.
func startRetryingWithin(ctx context.Context, t logger, image string, opts []Option, create startFunc, strategy strategyFunc, total, floor time.Duration) (testcontainers.Container, error) {
	t.Helper()
	began := time.Now()
	req := request(image, opts)
	req.WaitingFor = strategy(total)
	container, err := create(ctx, req)
	if err == nil {
		return container, nil
	}
	if cleanupErr := startterm.Terminate(container); cleanupErr != nil {
		return container, errors.Join(err, fmt.Errorf("terminate after failed start: %w", cleanupErr))
	}
	if !errors.Is(err, chfixture.ErrPublishedPortUnreachable) {
		return container, err
	}
	budget := total - time.Since(began)
	if budget < floor {
		return container, fmt.Errorf("no restart: %s of the %s budget left, less than %s: %w", budget.Round(time.Millisecond), total, floor, err)
	}
	var unreachable *chfixture.PortUnreachableError
	if errors.As(err, &unreachable) {
		t.Logf("falkordb-fixture: published port unreachable, restarting once port=%s tries=%d elapsed=%s", unreachable.Port, unreachable.Tries, unreachable.Elapsed)
	} else {
		t.Logf("falkordb-fixture: published port unreachable, restarting once port=unknown tries=0 elapsed=%s", time.Since(began).Round(time.Millisecond))
	}
	t.Logf("falkordb-fixture: first failure (retry budget %s):\n%v", budget, err)
	req = request(image, opts)
	req.WaitingFor = strategy(budget)
	return start(ctx, req, create)
}

func request(image string, opts []Option) testcontainers.GenericContainerRequest {
	// Not an `Image:` field on purpose: scripts/ci/test-testcontainers-image-pins.sh checks the pin at each Start call site.
	req := testcontainers.ContainerRequest{
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   chfixture.FalkorReady(),
	}
	req.Image = image
	for _, opt := range opts {
		opt(&req)
	}
	return testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true}
}

// start returns the container that failed to start only so a caller can prove it is gone; it is already terminated.
func start(ctx context.Context, req testcontainers.GenericContainerRequest, create startFunc) (testcontainers.Container, error) {
	container, err := create(ctx, req)
	return container, startterm.TerminateFailed(container, err)
}
