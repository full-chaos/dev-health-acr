package hosted

import (
	"context"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
)

const (
	defaultCredentialOutboxReapInterval   = time.Hour
	defaultCredentialOutboxReapBatchLimit = 100
	credentialOutboxRetention             = 7 * 24 * time.Hour
	credentialOutboxReapFailureMessage    = "credential outbox reap tick failed; retrying on next tick"
	credentialOutboxReapFullMessage       = "credential outbox reap batch full; more aggregates wait for the next tick"
)

type credentialOutboxReaper interface {
	ReapPublished(ctx context.Context, olderThan time.Time, limit int) (storagepostgres.ReapResult, error)
}

func startCredentialOutboxReaper(context.Context, credentialOutboxReaper, *slog.Logger, packetPurgeFailureObserver, time.Duration) func() error {
	return func() error { return nil }
}

func buildCredentialOutboxReaper(config.Config, credentialOutboxReaper) credentialOutboxReaper {
	return nil
}
