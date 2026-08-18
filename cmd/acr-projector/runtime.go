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
	clickhouseClient, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: cfg.ClickHouseDSN, TLS: tlsConfig, MaxBytesToRead: cfg.ClickHouseMaxBytesToRead,
	})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open clickhouse: %w", err), runtime.Close())
	}
	runtime.closers = append(runtime.closers, clickhouseClient.Close)

	backend, falkorCheck, err := openProjectionBackend(logger)
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
	clickhouseSource.WithLogger(logger)
	episodesSource, err := devhealthsource.NewEpisodesProjectionSource(episodeRows)
	if err != nil {
		return nil, errors.Join(err, runtime.Close())
	}
	teamsProjectsSource, err := devhealthsource.NewTeamsProjectsSource(clickhouseClient, cfg.TeamsProjectsEnabled)
	if err != nil {
		return nil, errors.Join(err, runtime.Close())
	}
	teamsProjectsSource.WithLogger(logger)

	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs:  cfg.OrgIDs,
		Sources: projectionSources(clickhouseSource, episodesSource, teamsProjectsSource),
		Backend: backend, Checkpoints: checkpoints, RebuildMarkers: rebuildMarkers, Locker: locker,
		ReuseInvalidator: reuseInvalidator,
		PollInterval:     cfg.PollInterval, Concurrency: cfg.Concurrency, Logger: logger,
		// Codex round-3 F2: a real observer, not the no-op default. A tick
		// that fails and holds its checkpoint is correct behavior, but only
		// safe behavior if it is visible.
		Observer: projectionrun.SlogObserver{Logger: logger},
	})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("build projection coordinator: %w", err), runtime.Close())
	}
	runtime.Coordinator = coordinator
	runtime.Checks = []api.ReadinessCheck{
		api.CheckFunc{CheckName: "postgres", Fn: func(ctx context.Context) error { return checkPostgresRuntime(ctx, db, runner) }},
		api.CheckFunc{CheckName: "clickhouse", Fn: clickhouseClient.Ping},
		api.CheckFunc{CheckName: "falkordb", Fn: falkorCheck},
		// CHAOS-3882: the readiness-path leg of the checkpoint-vs-store
		// divergence signal -- Tick's own recovery (see
		// projectionrun.Coordinator.recoverFromDivergence) already self-heals
		// within one poll interval, but a probe failing loudly in the
		// meantime is exactly the "never silent" half of this ticket: an
		// operator (or dashboard) sees degraded, not a quietly-empty graph
		// serving clean-looking no-match answers.
		api.CheckFunc{CheckName: "projection_liveness", Fn: coordinator.LivenessCheck},
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
// falkorGraphTelemetry builds the falkorgraph.GraphTelemetry sink this
// binary wires into every graph adapter it constructs. Factored out to a
// named, directly-testable function (CHAOS-3835 round-4 finding 3) rather
// than an inline SlogTelemetry{Logger: logger} literal, so a unit test can
// assert the wiring -- "the constructed sink carries the passed-in logger"
// -- without needing FALKOR_ADDR configured or a live connection.
func falkorGraphTelemetry(logger *slog.Logger) falkorgraph.GraphTelemetry {
	return falkorgraph.SlogTelemetry{Logger: logger}
}

// graphLifecycleTelemetry mirrors falkorGraphTelemetry above for the
// CHAOS-3898 S2a contextfabric.GraphLifecycleTelemetry sink -- see
// internal/runtime/hosted/open.go's identically-named function for the
// instrument-before-flip reasoning (design brief v4.1 F4) this binary
// shares with the hosted API.
func graphLifecycleTelemetry(logger *slog.Logger) contextfabric.GraphLifecycleTelemetry {
	return contextfabric.SlogGraphLifecycleTelemetry{Logger: logger}
}

func openProjectionBackend(logger *slog.Logger) (contextfabric.ProjectionBackend, func(context.Context) error, error) {
	if !falkorgraph.Configured(os.LookupEnv) {
		return nil, nil, nil
	}
	falkorConfig, err := falkorgraph.ConfigFromEnv(os.LookupEnv)
	if err != nil {
		return nil, nil, fmt.Errorf("falkordb configuration: %w", err)
	}
	// Codex round-3 F2: supply a real telemetry sink. Left nil, every graph
	// signal -- including the vector cleared/embedded counts that make a
	// re-embedding backlog visible -- was discarded.
	//
	// CHAOS-3835 round-4 finding 3: the sink must carry THIS process's
	// configured logger, not slog.Default() -- SlogTelemetry{} (no Logger)
	// falls back to slog.Default(), which ignores ACR_LOG_LEVEL and the
	// JSON handler main.go actually wires up to stdout. Every signal this
	// package emits -- including the CHAOS-3835 id-only skip counts, whose
	// entire purpose is being visible at the operator's configured level --
	// was reaching a DIFFERENT, unconfigured logger instead, satisfying
	// internal/contextfabric/AGENTS.md's "reported, never inferred"
	// invariant only cosmetically.
	falkorConfig.Telemetry = falkorGraphTelemetry(logger)
	// CHAOS-3898 S2a (design brief §2.0): startup/config assertion, and the
	// §5b signal sink wired unconditionally -- see graphLifecycleTelemetry's
	// own doc comment. falkorConfig.EpochResolver stays unset: this
	// binary's projection writes stay at epoch 0, byte-identical to
	// pre-CHAOS-3898 output, until a later slice wires a live
	// pglifecycle.Resolver here.
	falkorConfig.LifecycleTelemetry = graphLifecycleTelemetry(logger)
	if err := falkorgraph.AssertResolvedPrefix(logger, falkorConfig.LifecycleTelemetry, falkorConfig.GraphPrefix); err != nil {
		return nil, nil, fmt.Errorf("context fabric graph key prefix: %w", err)
	}
	// CHAOS-3778: the projector writes embeddings only when an embedder is
	// configured. It must agree with the hosted reader (both use
	// EmbedderFromEnv) -- writing vectors nothing queries is wasted work, and
	// querying an index nothing fills is silently degraded retrieval.
	embedderOptions, err := falkorgraph.EmbedderFromEnv(os.LookupEnv)
	if err != nil {
		return nil, nil, fmt.Errorf("context fabric embedder configuration: %w", err)
	}
	adapter, err := falkorgraph.NewWithEmbedder(falkorConfig, embedderOptions)
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

// projectionSources is the composition root's registered ProjectionSource
// list. It is a named function rather than an inline literal so the
// registration itself is testable without a live Postgres/ClickHouse
// (relationship_vocabulary_test.go's sibling,
// TestTeamsProjectsSourceIsRegisteredRegardlessOfItsFeatureFlag).
//
// Every source is registered UNCONDITIONALLY, including the teams/projects
// one. That is the point: ACR_CONTEXT_FABRIC_PROJECT_TEAMS_PROJECTS_ENABLED
// is handed to the source's constructor and gates only whether that source
// yields batches -- it must never decide whether the source appears here.
// Dropping a source from this list on a false flag would strand its
// (org_id, source) projection checkpoint rather than simply idling it, so
// flipping the flag back on would silently resume from a stale watermark
// instead of the full snapshot a never-registered source gets.
func projectionSources(clickhouse, episodes, teamsProjects contextfabric.ProjectionSource) []projectionrun.SourcePair {
	return []projectionrun.SourcePair{
		{Name: devhealthsource.SourceName, Source: clickhouse},
		{Name: devhealthsource.EpisodesSourceName, Source: episodes},
		{Name: devhealthsource.TeamsProjectsSourceName, Source: teamsProjects},
	}
}
