package startterm_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/startterm"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const livePostgresImage = "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44"

type neverReady struct{}

func (*neverReady) WaitUntilReady(context.Context, wait.StrategyTarget) error {
	return errors.New("readiness probe failed on purpose")
}

func requireGone(t *testing.T, container testcontainers.Container, err error) {
	t.Helper()
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

func TestLiveClickHouseFailedReadinessLeavesNoContainerBehind(t *testing.T) {
	chfixture.RequireContainers(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := startterm.Generic(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: chfixture.Image, ExposedPorts: []string{"9000/tcp"},
			Env:        map[string]string{"CLICKHOUSE_USER": "acr", "CLICKHOUSE_PASSWORD": "acr", "CLICKHOUSE_DB": "default"},
			WaitingFor: &neverReady{},
		},
		Started: true,
	})
	requireGone(t, container, err)
}

func TestLivePostgresFailedReadinessLeavesNoContainerBehind(t *testing.T) {
	chfixture.RequireContainers(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pc, err := startterm.PostgresRun(ctx, livePostgresImage,
		tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"),
		testcontainers.WithWaitStrategy(&neverReady{}),
	)
	var container testcontainers.Container
	if pc != nil {
		container = pc
	}
	requireGone(t, container, err)
}
