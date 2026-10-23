package falkorfixture

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const liveFalkorImage = "falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3"

type neverReady struct{}

func (neverReady) WaitUntilReady(context.Context, wait.StrategyTarget) error {
	return errors.New("readiness probe failed on purpose")
}

func TestLiveFailedReadinessLeavesNoContainerBehind(t *testing.T) {
	chfixture.RequireContainers(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	req := request(liveFalkorImage, nil)
	req.WaitingFor = neverReady{}
	container, err := start(ctx, req, testcontainers.GenericContainer)
	if err == nil || !strings.Contains(err.Error(), "readiness probe failed on purpose") {
		t.Fatalf("the failing probe must fail the start: %v", err)
	}
	if container == nil {
		t.Fatal("testcontainers must hand back the container whose readiness failed; without it nothing could terminate it")
	}
	state, stateErr := container.State(context.Background())
	if stateErr == nil {
		t.Fatalf("a container whose readiness failed is still known to the daemon (running=%v status=%q): the start path leaked it", state.Running, state.Status)
	}
	if !errdefs.IsNotFound(stateErr) {
		t.Fatalf("the container is not shown to be gone: the daemon answered %v, not a not-found; a daemon or network error is not proof of removal", stateErr)
	}
}
