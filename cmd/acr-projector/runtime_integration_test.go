package main

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestOpenRuntimeWiresPostgresAndClickHouseThenDisablesWithoutAZepBackend
// proves the real composition path -- Postgres connects, migrations apply,
// the ClickHouse client constructs -- against a real PostgreSQL instance.
// It stops short of a live graph backend: no Zep Cloud credential exists in
// this environment (ADR 0007's documented external blocker), so this is the
// fake/env-gated seam the same way zepgraph.TestLiveZep* is gated. Wiring a
// real zepgraph.Adapter here would need ACR_CONTEXT_FABRIC_ZEP_BASE_URL /
// ACR_CONTEXT_FABRIC_ZEP_API_KEY, at which point openRuntime's own
// zepgraph.Configured/zepgraph.New path (identical to production) takes over.
func TestOpenRuntimeWiresPostgresAndClickHouseThenDisablesWithoutAZepBackend(t *testing.T) {
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	cfg, err := config.LoadProjector()
	require.NoError(t, err)
	cfg.ProjectionEnabled = true
	cfg.PostgresDSN = dsn
	cfg.ClickHouseDSN = "clickhouse://redacted"

	runtime, err := openRuntime(ctx, cfg, discardLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })

	if runtime.Coordinator != nil {
		t.Fatal("coordinator must not start without a configured Zep graph backend")
	}
}

func TestOpenRuntimeSurfacesUnreachablePostgres(t *testing.T) {
	cfg, err := config.LoadProjector()
	require.NoError(t, err)
	cfg.ProjectionEnabled = true
	cfg.PostgresDSN = "postgres://acr:acr@127.0.0.1:1/acr?sslmode=disable"
	cfg.ClickHouseDSN = "clickhouse://redacted"

	_, err = openRuntime(context.Background(), cfg, discardLogger())
	if err == nil {
		t.Fatal("expected an error opening an unreachable postgres instance")
	}
}
