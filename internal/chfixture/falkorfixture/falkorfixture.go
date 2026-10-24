// Package falkorfixture starts FalkorDB test containers. It lives apart from chfixture so the container-start
// gate (RequireContainers) can be imported by the one function that starts the container.
package falkorfixture

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/startterm"
	"github.com/testcontainers/testcontainers-go"
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

// startRetrying starts a container and, only when the wait proved the server answers inside the container while
// its published port never did, starts one more on a new port mapping with the rest of the 2-minute budget. Every
// other failure is final.
func startRetrying(ctx context.Context, t logger, image string, opts []Option, create startFunc) (testcontainers.Container, error) {
	t.Helper()
	began := time.Now()
	container, err := start(ctx, withBudget(request(image, opts), serveBudget), create)
	if err == nil || !errors.Is(err, chfixture.ErrPublishedPortUnreachable) {
		return container, err
	}
	budget := max(serveBudget-time.Since(began), minRetryBudget)
	var unreachable *chfixture.PortUnreachableError
	if errors.As(err, &unreachable) {
		t.Logf("falkordb-fixture: published port unreachable, restarting once port=%s tries=%d elapsed=%s", unreachable.Port, unreachable.Tries, unreachable.Elapsed)
	} else {
		t.Logf("falkordb-fixture: published port unreachable, restarting once port=unknown tries=0 elapsed=%s", time.Since(began).Round(time.Millisecond))
	}
	t.Logf("falkordb-fixture: first failure (retry budget %s):\n%v", budget, err)
	return start(ctx, withBudget(request(image, opts), budget), create)
}

func withBudget(req testcontainers.GenericContainerRequest, budget time.Duration) testcontainers.GenericContainerRequest {
	req.WaitingFor = chfixture.FalkorReadyWithin(budget)
	return req
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
