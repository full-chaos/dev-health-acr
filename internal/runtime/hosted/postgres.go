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
// Ready. Only runtimepostgres.ErrUnavailable (classified with errors.Is,
// never by message) is retried, bounded by config (ACR_POSTGRES_STARTUP_
// ATTEMPTS / _BACKOFF, default 5 x 2s), one closed-vocabulary Warn per
// attempt. Config/validation errors fail at once.
const (
	defaultPostgresStartupAttempts = 5
	defaultPostgresStartupBackoff  = 2 * time.Second
	postgresStartupAttemptEvent    = "postgres startup attempt failed"
	postgresStartupFailureClass    = "postgres_unavailable"
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

// OpenPostgresWithRetry is shared by acr-api and acr-projector (CHAOS-7184).
// The credentials CLI deliberately does not use it (one-shot, human-run).
func OpenPostgresWithRetry(ctx context.Context, cfg runtimepostgres.Config, attempts int, backoff time.Duration, logger *slog.Logger) (*sql.DB, error) {
	// Zero value means the default at THIS site: Config literals built
	// without config.Load (tests, future entrypoints) must still retry.
	if attempts < 1 {
		attempts = defaultPostgresStartupAttempts
	}
	if backoff <= 0 {
		backoff = defaultPostgresStartupBackoff
	}
	var lastErr error
	lastAttempt := 0
	// terminal wraps the last error with the attempt count and class so the
	// process's final error line is self-describing at Info and above.
	terminal := func() error {
		return fmt.Errorf("postgres startup failed at attempt %d/%d (%s): %w", lastAttempt, attempts, postgresFailureClass(lastErr), lastErr)
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		lastAttempt = attempt
		database, err := postgresOpenFn(ctx, cfg)
		if err == nil {
			if attempt > 1 && logger != nil {
				logger.InfoContext(ctx, "postgres connected after startup retry", "attempt", attempt, "max_attempts", attempts)
			}
			return database, nil
		}
		lastErr = err
		retryable := errors.Is(err, runtimepostgres.ErrUnavailable)
		outcome := postgresStartupOutcomeRetrying
		switch {
		case !retryable:
			outcome = postgresStartupOutcomeNotRetryable
		case attempt == attempts:
			outcome = postgresStartupOutcomeExhausted
		}
		// One Warn per FAILED attempt, including the terminal one, so the
		// log line count always equals the number of attempts the server saw.
		if logger != nil {
			logger.WarnContext(ctx, postgresStartupAttemptEvent, "attempt", attempt, "max_attempts", attempts, "outcome", outcome, "failure_class", postgresFailureClass(err), "backoff_ms", backoffMillis(outcome, backoff))
		}
		if outcome != postgresStartupOutcomeRetrying {
			break
		}
		if serr := postgresOpenSleep(ctx, backoff); serr != nil {
			return nil, terminal()
		}
	}
	return nil, terminal()
}

// Closed vocabularies for the startup attempt event.
const (
	postgresStartupOutcomeRetrying     = "retrying"
	postgresStartupOutcomeExhausted    = "exhausted"
	postgresStartupOutcomeNotRetryable = "not_retryable"
)

func postgresFailureClass(err error) string {
	switch {
	case errors.Is(err, runtimepostgres.ErrUnavailable):
		return "postgres_unavailable"
	case errors.Is(err, runtimepostgres.ErrRejected):
		return "postgres_rejected"
	default:
		return "postgres_other"
	}
}

func backoffMillis(outcome string, backoff time.Duration) int64 {
	if outcome != postgresStartupOutcomeRetrying {
		return 0
	}
	return backoff.Milliseconds()
}

func openPostgres(ctx context.Context, cfg config.Config, logger *slog.Logger) (postgresComponents, error) {
	database, err := OpenPostgresWithRetry(ctx, runtimepostgres.Config{
		DSN: cfg.PostgresDSN, PoolerAdminDSN: cfg.PostgresPoolerAdminDSN,
		MaxOpenConns: cfg.PostgresMaxOpenConns, MaxIdleConns: cfg.PostgresMaxIdleConns, MaxIdleConnsSet: cfg.PostgresMaxIdleConnsConfigured,
		ConnMaxLifetime: cfg.PostgresConnMaxLifetime, ConnMaxIdleTime: cfg.PostgresConnMaxIdleTime, PingTimeout: cfg.PostgresPingTimeout,
	}, cfg.PostgresStartupAttempts, cfg.PostgresStartupBackoff, logger)
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
	// CHAOS-6191: gated on the OAuth login being configured (see
	// startConfiguredOAuthPurge); its initial purge proves DELETE on both
	// OAuth tables, so a missing grant fails startup loudly.
	stopOAuthPurgeLoop, err := startConfiguredOAuthPurge(ctx, cfg, oauth, logger)
	if err != nil {
		return fail(errors.Join(fmt.Errorf("purge expired oauth rows: %w", err), stopPurgeLoop(), stopWorkloadCredentialPurgeLoop()))
	}
	stopDeviceSweep, err := startDeviceCredentialSweep(ctx, devices, logger, packetPurgeSlogObserver(logger))
	if err != nil {
		return fail(errors.Join(fmt.Errorf("sweep unacknowledged device credentials: %w", err), stopPurgeLoop(), stopWorkloadCredentialPurgeLoop(), stopOAuthPurgeLoop()))
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
			return errors.Join(stopPurgeLoop(), stopWorkloadCredentialPurgeLoop(), stopOAuthPurgeLoop(), stopDeviceSweep(), database.Close())
		},
	}, nil
}
