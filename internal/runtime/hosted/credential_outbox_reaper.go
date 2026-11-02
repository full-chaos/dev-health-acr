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
	// credentialOutboxRetention is how long a fully published aggregate with no credential row stays.
	credentialOutboxRetention          = 7 * 24 * time.Hour
	credentialOutboxReapFailureMessage = "credential outbox reap tick failed; retrying on next tick"
	credentialOutboxReapFullMessage    = "credential outbox reap batch full; more aggregates wait for the next tick"
)

// credentialOutboxReaper is *storagepostgres.OutboxReaper's run.
type credentialOutboxReaper interface {
	ReapPublished(ctx context.Context, olderThan time.Time, limit int) (storagepostgres.ReapResult, error)
}

// startCredentialOutboxReaper runs the reaper every interval in the background, in every pod that has the publisher
// configured (the statement is idempotent: a second pod deletes what is left). Like the publisher it makes no call at
// start, and a failed run (a missing DELETE grant shows as insufficient_privilege on the table) is only logged.
func startCredentialOutboxReaper(ctx context.Context, reaper credentialOutboxReaper, logger *slog.Logger, observe packetPurgeFailureObserver, interval time.Duration) func() error {
	if interval <= 0 {
		interval = defaultCredentialOutboxReapInterval
	}
	purge := func(ctx context.Context, now time.Time, limit int) (int, error) {
		result, err := reaper.ReapPublished(ctx, now.Add(-credentialOutboxRetention), limit)
		if logger != nil {
			if result.Aggregates > 0 {
				logger.InfoContext(ctx, "credential outbox reap", "source", "reaper", "aggregates", result.Aggregates, "rows", result.Rows)
			}
			if result.Aggregates >= limit {
				logger.InfoContext(ctx, credentialOutboxReapFullMessage, "aggregates", result.Aggregates)
			}
		}
		return result.Aggregates, err
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	tick := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer tick.Stop()
		runPurgeTickLoop(loopCtx, tick.C, time.Now, purge, defaultCredentialOutboxReapBatchLimit, credentialOutboxReapFailureMessage, observe)
	}()
	return func() error {
		cancel()
		<-done
		return nil
	}
}

// buildCredentialOutboxReaper returns the reaper for cfg, or nil when ACR_AUTH_PROJECTION_URL is unset: rows are only
// published while the publisher runs, and the DELETE grant is held only then.
func buildCredentialOutboxReaper(cfg config.Config, reaper credentialOutboxReaper) credentialOutboxReaper {
	if !cfg.AuthProjection.Enabled() {
		return nil
	}
	return reaper
}
