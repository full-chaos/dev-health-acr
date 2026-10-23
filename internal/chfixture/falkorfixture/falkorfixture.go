// Package falkorfixture starts FalkorDB test containers. It lives apart from chfixture so the container-start
// gate (RequireContainers) can be imported by the one function that starts the container.
package falkorfixture

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/testcontainers/testcontainers-go"
)

const terminateBudget = 30 * time.Second

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
	container, err := start(ctx, request(image, opts), testcontainers.GenericContainer)
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
	if err == nil {
		return container, nil
	}
	if !isNil(container) {
		termCtx, cancel := context.WithTimeout(context.Background(), terminateBudget)
		defer cancel()
		if termErr := container.Terminate(termCtx); termErr != nil {
			err = errors.Join(err, fmt.Errorf("terminate after failed start: %w", termErr))
		}
	}
	return container, err
}

func isNil(c testcontainers.Container) bool {
	if c == nil {
		return true
	}
	v := reflect.ValueOf(c)
	return v.Kind() == reflect.Pointer && v.IsNil()
}
