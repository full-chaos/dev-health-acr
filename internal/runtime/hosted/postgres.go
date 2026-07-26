package hosted

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
	postgresmigrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

const defaultPostgresReadinessTimeout = 5 * time.Second

func openPostgres(ctx context.Context, cfg config.Config, logger *slog.Logger) (postgresComponents, error) {
	database, err := runtimepostgres.Open(ctx, runtimepostgres.Config{
		DSN: cfg.PostgresDSN, PoolerAdminDSN: cfg.PostgresPoolerAdminDSN,
		AllowInsecure: cfg.AllowInsecurePostgres,
		MaxOpenConns:  cfg.PostgresMaxOpenConns, MaxIdleConns: cfg.PostgresMaxIdleConns, MaxIdleConnsSet: cfg.PostgresMaxIdleConnsConfigured,
		ConnMaxLifetime: cfg.PostgresConnMaxLifetime, ConnMaxIdleTime: cfg.PostgresConnMaxIdleTime, PingTimeout: cfg.PostgresPingTimeout,
	})
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
	packets, err := storagepostgres.NewPacketStore(database, nil)
	if err != nil {
		return fail(fmt.Errorf("create packet store: %w", err))
	}
	episodes, err := storagepostgres.NewEpisodeStore(database)
	if err != nil {
		return fail(fmt.Errorf("create episode store: %w", err))
	}
	stopPurgeLoop, err := startPacketPurgeLoop(ctx, packets.PurgeExpiredWithAudit, nil, packetPurgeSlogObserver(logger))
	if err != nil {
		return fail(fmt.Errorf("purge expired packet snapshots: %w", err))
	}
	readinessTimeout := cfg.PostgresPingTimeout
	if readinessTimeout <= 0 {
		readinessTimeout = defaultPostgresReadinessTimeout
	}
	return postgresComponents{
		credentials: credentials, devices: devices, audit: audit, packets: packets, episodes: episodes,
		check: func(ctx context.Context) error {
			checkContext, cancel := context.WithTimeout(ctx, readinessTimeout)
			defer cancel()
			return checkPostgresRuntime(checkContext, database, runner, cfg.EnableEpisodeWriteback)
		},
		close: func() error {
			return errors.Join(stopPurgeLoop(), database.Close())
		},
	}, nil
}
