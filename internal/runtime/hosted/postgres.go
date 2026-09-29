package hosted

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
	postgresmigrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

const defaultPostgresReadinessTimeout = 5 * time.Second

// CHAOS-7168: acr-api crashed twice at startup right after the helm migrate
// hook ("PostgreSQL is unavailable", prod 2026-09-29 12:28:42Z) before going
// Ready. Only the reachability failure is retried, bounded, with one loud
// line per attempt; config/validation errors fail at once.
const (
	postgresOpenAttempts = 5
	postgresOpenBackoff  = 2 * time.Second
	// postgresUnavailableMessage is runtimepostgres.Open's fixed, secret-free
	// reachability failure text.
	postgresUnavailableMessage = "PostgreSQL is unavailable"
)

// postgresOpenFn/postgresOpenSleep are test seams.
var (
	postgresOpenFn    = runtimepostgres.Open
	postgresOpenSleep = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
)

func openPostgresWithRetry(ctx context.Context, cfg runtimepostgres.Config, logger *slog.Logger) (*sql.DB, error) {
	var lastErr error
	for attempt := 1; attempt <= postgresOpenAttempts; attempt++ {
		database, err := postgresOpenFn(ctx, cfg)
		if err == nil {
			if attempt > 1 && logger != nil {
				logger.InfoContext(ctx, "postgres connected after retry", "attempt", attempt, "max_attempts", postgresOpenAttempts)
			}
			return database, nil
		}
		lastErr = err
		if err.Error() != postgresUnavailableMessage || attempt == postgresOpenAttempts {
			break
		}
		if logger != nil {
			logger.WarnContext(ctx, "postgres unavailable at startup; retrying", "attempt", attempt, "max_attempts", postgresOpenAttempts, "backoff_ms", postgresOpenBackoff.Milliseconds(), "failure_class", "postgres_unavailable")
		}
		if serr := postgresOpenSleep(ctx, postgresOpenBackoff); serr != nil {
			return nil, lastErr
		}
	}
	return nil, lastErr
}

func openPostgres(ctx context.Context, cfg config.Config, logger *slog.Logger) (postgresComponents, error) {
	database, err := openPostgresWithRetry(ctx, runtimepostgres.Config{
		DSN: cfg.PostgresDSN, PoolerAdminDSN: cfg.PostgresPoolerAdminDSN,
		MaxOpenConns: cfg.PostgresMaxOpenConns, MaxIdleConns: cfg.PostgresMaxIdleConns, MaxIdleConnsSet: cfg.PostgresMaxIdleConnsConfigured,
		ConnMaxLifetime: cfg.PostgresConnMaxLifetime, ConnMaxIdleTime: cfg.PostgresConnMaxIdleTime, PingTimeout: cfg.PostgresPingTimeout,
	}, logger)
	if err != nil {
		return postgresComponents{}, err
	}
	fail := func(cause error) (postgresComponents, error) {
		return postgresComponents{}, errors.Join(cause, database.Close())
	}
	runner, err := postgresmigrations.Embedded()
	if err != nil {
		return fail(errors.New("load PostgreSQL migration contract"))
	}
	audit, err := storagepostgres.NewAuditStore(database)
	if err != nil {
		return fail(fmt.Errorf("create audit store: %w", err))
	}
	credentials, err := storagepostgres.NewCredentialStore(database, audit)
	if err != nil {
		return fail(fmt.Errorf("create credential store: %w", err))
	}
	devices, err := storagepostgres.NewDeviceAuthorizationStore(database, audit)
	if err != nil {
		return fail(fmt.Errorf("create device authorization store: %w", err))
	}
	oauth, err := storagepostgres.NewOAuthStore(database)
	if err != nil {
		return fail(fmt.Errorf("create oauth store: %w", err))
	}
	packets, err := storagepostgres.NewPacketStore(database, nil)
	if err != nil {
		return fail(fmt.Errorf("create packet store: %w", err))
	}
	episodes, err := storagepostgres.NewEpisodeStore(database)
	if err != nil {
		return fail(fmt.Errorf("create episode store: %w", err))
	}
	workloadBindings, err := storagepostgres.NewWorkloadBindingStore(database)
	if err != nil {
		return fail(fmt.Errorf("create workload binding store: %w", err))
	}
	stopPurgeLoop, err := startPacketPurgeLoop(ctx, packets.PurgeExpiredWithAudit, nil, packetPurgeSlogObserver(logger))
	if err != nil {
		return fail(fmt.Errorf("purge expired packet snapshots: %w", err))
	}
	// Started ONLY when CHAOS-4013 is actually configured
	// (workloadTokenExchangeConfigured, the SAME gate buildWorkloadTokenExchange
	// uses): its initial synchronous purge issues a DELETE against
	// acr.client_credentials, and an unconfigured deployment's runtime DB
	// role has never needed (and per this repo's own least-privilege
	// fixture, never been granted) DELETE on that table -- only
	// SELECT/UPDATE, for the pre-existing revoke/rotate paths. Starting
	// this unconditionally would fail startup for every deployment that
	// has not opted into workload token exchange, violating the
	// "unconfigured deployment never fails closed" convention every other
	// optional dependency in this codebase follows.
	stopWorkloadCredentialPurgeLoop := func() error { return nil }
	if workloadTokenExchangeConfigured(os.LookupEnv) {
		stopWorkloadCredentialPurgeLoop, err = startWorkloadCredentialPurgeLoop(ctx, storagepostgres.NewWorkloadCredentialPurger(database), nil, packetPurgeSlogObserver(logger))
		if err != nil {
			return fail(errors.Join(fmt.Errorf("purge expired workload credentials: %w", err), stopPurgeLoop()))
		}
	}
	readinessTimeout := cfg.PostgresPingTimeout
	if readinessTimeout <= 0 {
		readinessTimeout = defaultPostgresReadinessTimeout
	}
	return postgresComponents{
		credentials: credentials, devices: devices, oauth: oauth, audit: audit, packets: packets, episodes: episodes,
		workloadBindings: workloadBindings, db: database,
		check: func(ctx context.Context) error {
			checkContext, cancel := context.WithTimeout(ctx, readinessTimeout)
			defer cancel()
			return checkPostgresRuntime(checkContext, database, runner, cfg.EnableEpisodeWriteback)
		},
		close: func() error {
			return errors.Join(stopPurgeLoop(), stopWorkloadCredentialPurgeLoop(), database.Close())
		},
	}, nil
}
