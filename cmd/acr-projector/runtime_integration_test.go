package main

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestOpenRuntimeWiresPostgresAndClickHouseThenDisablesWithoutAFalkorBackend
// proves the real composition path -- Postgres connects, migrations apply,
// the ClickHouse client constructs -- against a real PostgreSQL instance.
// It stops short of a live graph backend: this test does not set
// ACR_CONTEXT_FABRIC_FALKOR_ADDR, so this is the disabled/unconfigured seam
// (falkorgraph's own live lifecycle test, gated on a real testcontainers
// FalkorDB, proves the configured path -- see
// internal/contextfabric/falkorgraph/adapter_live_integration_test.go).
// Wiring a real falkorgraph.Adapter here would need
// ACR_CONTEXT_FABRIC_FALKOR_ADDR (plus optional
// ACR_CONTEXT_FABRIC_FALKOR_PASSWORD), at which point openRuntime's own
// falkorgraph.Configured/falkorgraph.New path (identical to production)
// takes over.
func TestOpenRuntimeWiresPostgresAndClickHouseThenDisablesWithoutAFalkorBackend(t *testing.T) {
	ctx := context.Background()
	// CHAOS-4855: pinned by digest (was a bare tag) so
	// TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX resolves this to the ghcr.io
	// mirror by digest, same as every other postgres:18-alpine pull in
	// this module.
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44",
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
		t.Fatal("coordinator must not start without a configured FalkorDB graph backend")
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
