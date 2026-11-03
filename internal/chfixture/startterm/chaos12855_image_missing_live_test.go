package startterm_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/startterm"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
)

// CHAOS-12855: the retry fires only on the daemon's create refusal for a missing image. This asks a real daemon to
// create a container from an image that was never pulled, wraps the refusal exactly as testcontainers v0.44 does
// (generic.go:81, docker.go:1322), and checks the matcher accepts it; and checks it rejects a real not-found that is
// not about an image. A matcher that drifts from the daemon's words would leave the retry dead, silently.
func TestLiveDaemonRefusalOfAMissingImageIsTheOneStarttermRetries(t *testing.T) {
	chfixture.RequireContainers(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	docker, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer func() { _ = docker.Close() }()

	const neverPulled = "acr-chaos12855-never-pulled:none"
	created, createErr := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Image: neverPulled})
	if createErr == nil {
		_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		t.Fatalf("the daemon created a container from %s, an image no pull ever made", neverPulled)
	}
	if refused := fmt.Errorf("create container: container create: %w", createErr); !startterm.ImageMissingAtCreate(refused) {
		t.Fatalf("the daemon's refusal of a missing image is not matched, so the retry would never fire: %v", createErr)
	}

	_, inspectErr := docker.ContainerInspect(ctx, "acr-chaos12855-no-such-container", client.ContainerInspectOptions{})
	if inspectErr == nil {
		t.Fatal("the daemon found a container that does not exist")
	}
	if startterm.ImageMissingAtCreate(fmt.Errorf("create container: container create: %w", inspectErr)) {
		t.Fatalf("a not-found that is not about an image must not be retried: %v", inspectErr)
	}
}
