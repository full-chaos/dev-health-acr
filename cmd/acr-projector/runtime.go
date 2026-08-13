package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	runtimeclickhouse "github.com/full-chaos/dev-health-acr/internal/runtime/clickhouse"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

// Runtime is the composed acr-projector process. Coordinator is nil when
// projection is disabled or not fully configured -- see openRuntime -- so
// the binary always comes up and stays healthy rather than crash-looping
// over an intentionally-unconfigured dependency (mirrors falkorgraph.Configured's
// "an unset deployment should never fail closed" posture, carried over from
// ADR 0007 and restated for FalkorDB in ADR 0009).
type Runtime struct {
	Coordinator *projectionrun.Coordinator
	Checks      []api.ReadinessCheck
	closers     []func() error
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	var errs []error
	for i := len(r.closers) - 1; i >= 0; i-- {
		errs = append(errs, r.closers[i]())
	}
	return errors.Join(errs...)
}

func openRuntime(ctx context.Context, cfg config.ProjectorConfig, logger *slog.Logger) (*Runtime, error) {
	runtime := &Runtime{}
	if !cfg.ProjectionEnabled {
		logger.InfoContext(ctx, "context fabric projection is disabled", cfg.SafeAttributes()...)
		return runtime, nil
	}
	if cfg.PostgresDSN == "" || cfg.ClickHouseDSN == "" {
		logger.WarnContext(ctx, "context fabric projection is enabled but Postgres/ClickHouse are not configured; running disabled", cfg.SafeAttributes()...)
		return runtime, nil
	}

	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{
		DSN: cfg.PostgresDSN, PoolerAdminDSN: cfg.PostgresPoolerAdminDSN,
		MaxOpenConns: cfg.PostgresMaxOpenConns, MaxIdleConns: cfg.PostgresMaxIdleConns, MaxIdleConnsSet: cfg.PostgresMaxIdleConnsConfigured,
		ConnMaxLifetime: cfg.PostgresConnMaxLifetime, ConnMaxIdleTime: cfg.PostgresConnMaxIdleTime, PingTimeout: cfg.PostgresPingTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	runtime.closers = append(runtime.closers, db.Close)
	runner, err := migrations.Embedded()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("load postgres migration contract: %w", err), runtime.Close())
	}

	tlsConfig, err := runtimeclickhouse.TLSConfigFromCABundle(cfg.ClickHouseCACertPath)
	if err != nil {
		return nil, errors.Join(err, runtime.Close())
	}
	clickhouseClient, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{DSN: cfg.ClickHouseDSN, TLS: tlsConfig})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open clickhouse: %w", err), runtime.Close())
	}
	runtime.closers = append(runtime.closers, clickhouseClient.Close)

	backend, falkorCheck, err := openProjectionBackend()
	if err != nil {
		return nil, errors.Join(err, runtime.Close())
	}
	if backend == nil {
		logger.WarnContext(ctx, "context fabric projection is enabled but no graph backend is configured (ACR_CONTEXT_FABRIC_FALKOR_ADDR unset); running disabled")
		return runtime, nil
	}

	checkpoints, err := pgprojection.NewCheckpointStore(db)
	if err != nil {
		return nil, errors.Join(err, runtime.Close())
	}
	rebuildMarkers, err := pgprojection.NewRebuildMarkerStore(db)
	if err != nil {
		return nil, errors.Join(err, runtime.Close())
	}
	episodeRows, err := storagepostgres.NewEpisodeStore(db)
	if err != nil {
		return nil, errors.Join(err, runtime.Close())
	}
	locker, err := projectionrun.NewPostgresOrgLocker(db)
	if err != nil {
		return nil, errors.Join(err, runtime.Close())
	}
	// CHAOS-3782: reuses pginvestigation.Store purely as a
	// contextfabric.ReuseInvalidator here -- acr-projector never saves or
	// reads investigation results itself, so WithAnswerReuse is
	// deliberately not passed (InvalidateOrganizationReuse works
	// regardless of whether reuse is "enabled" on a Store; only
	// Save/FindReusable's reuse bookkeeping needs that option).
	reuseInvalidator, err := pginvestigation.NewStore(db)
	if err != nil {
		return nil, errors.Join(err, runtime.Close())
	}

	clickhouseSource, err := devhealthsource.NewClickHouseProjectionSource(clickhouseClient)
	if err != nil {
		return nil, errors.Join(err, runtime.Close())
	}
	episodesSource, err := devhealthsource.NewEpisodesProjectionSource(episodeRows)
	if err != nil {
		return nil, errors.Join(err, runtime.Close())
	}
	teamsProjectsSource := devhealthsource.NewTeamsProjectsSource(cfg.TeamsProjectsEnabled)

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: cfg.OrgIDs,
		Sources: []projectionrun.SourcePair{
			{Name: devhealthsource.SourceName, Source: clickhouseSource},
			{Name: devhealthsource.EpisodesSourceName, Source: episodesSource},
			{Name: devhealthsource.TeamsProjectsSourceName, Source: teamsProjectsSource},
		},
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: rebuildMarkers, Locker: locker,
		ReuseInvalidator: reuseInvalidator,
		PollInterval:     cfg.PollInterval, Concurrency: cfg.Concurrency, Logger: logger,
	})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("build projection coordinator: %w", err), runtime.Close())
	}
	runtime.Coordinator = coordinator
	runtime.Checks = []api.ReadinessCheck{
		api.CheckFunc{CheckName: "postgres", Fn: func(ctx context.Context) error { return checkPostgresRuntime(ctx, db, runner) }},
		api.CheckFunc{CheckName: "clickhouse", Fn: clickhouseClient.Ping},
		api.CheckFunc{CheckName: "falkordb", Fn: falkorCheck},
	}
	return runtime, nil
}

func checkPostgresRuntime(ctx context.Context, db *sql.DB, runner *migrations.Runner) error {
	if err := db.PingContext(ctx); err != nil {
		return errors.New("postgresql runtime is unavailable")
	}
	if runner == nil || runner.VerifyCurrent(ctx, db) != nil {
		return errors.New("postgresql runtime schema is unavailable")
	}
	return nil
}

// probeOrg/probeSource never correspond to a real organization; ProjectionWatermark
// against them is expected to return falkorgraph.ErrNotFound on a healthy backend
// (the marker node simply doesn't exist), which openProjectionBackend's check
// treats as reachable, not as a failure.
const probeOrg, probeSource = "acr-projector-readiness-probe", "readiness"

// openProjectionBackend constructs the production contextfabric.ProjectionBackend
// (the FalkorDB adapter, per ADR 0009, which supersedes ADR 0007's Zep Cloud
// decision) when configured, and returns a nil backend -- not an error --
// when it isn't: an unconfigured FalkorDB endpoint is an accepted,
// intentionally-disabled state (see ADR 0009's "Deployment topology"), not a
// startup failure.
func openProjectionBackend() (contextfabric.ProjectionBackend, func(context.Context) error, error) {
	if !falkorgraph.Configured(os.LookupEnv) {
		return nil, nil, nil
	}
	falkorConfig, err := falkorgraph.ConfigFromEnv(os.LookupEnv)
	if err != nil {
		return nil, nil, fmt.Errorf("falkordb configuration: %w", err)
	}
	adapter, err := falkorgraph.New(falkorConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("open falkordb graph backend: %w", err)
	}
	check := func(ctx context.Context) error {
		_, err := adapter.ProjectionWatermark(ctx, probeOrg, probeSource)
		if err == nil || errors.Is(err, falkorgraph.ErrNotFound) {
			return nil
		}
		return err
	}
	return adapter, check, nil
}
