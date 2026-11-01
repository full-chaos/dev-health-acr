package hosted

import (
	"context"
	"log/slog"
	"time"

	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
)

const (
	defaultCredentialLifecycleTickInterval   = 30 * time.Second
	defaultCredentialLifecycleTickBatchLimit = 100
	credentialLifecycleTickFailureMessage    = "credential lifecycle tick failed; retrying on next tick"
	// credentialLifecycleTickFullMessage is the fixed line of a batch that
	// reached its limit: the retention deletes wait for the expired events.
	credentialLifecycleTickFullMessage = "credential lifecycle tick batch full; more rows wait for the next tick"
	// credentialLifecycleTickSkippedMessage is the fixed line of credentials no
	// event can describe (an empty scope list): one warning with the count every
	// tick until each is revoked, and the retention gate keeps them undeleted.
	// It names no credential; docs/operations.md gives the query that finds them.
	credentialLifecycleTickSkippedMessage = "credential lifecycle tick skipped credentials no event can describe (empty scope list); revoke them, see the operations guide"
)

// credentialLifecycleTicker is *storagepostgres.LifecycleTick's tick.
type credentialLifecycleTicker interface {
	Tick(ctx context.Context, now time.Time, limit int) (storagepostgres.LifecycleTickResult, error)
}

// startCredentialLifecycleTick runs the tick in every acr-api pod (the sweep's
// home): the transaction's SKIP LOCKED selects keep pods off the same row and
// the writer's terminal rule keeps a second pod from writing a second expired
// row. A tick that wrote anything logs one line with the counts only, in the
// ack sweep's form; a batch that reached its limit logs the fixed line.
func startCredentialLifecycleTick(ctx context.Context, ticker credentialLifecycleTicker, logger *slog.Logger, observe packetPurgeFailureObserver) (func() error, error) {
	purge := func(ctx context.Context, now time.Time, limit int) (int, error) {
		result, err := ticker.Tick(ctx, now, limit)
		if logger != nil {
			if result.Expired > 0 || result.FirstTouch > 0 || result.Skipped > 0 {
				logger.InfoContext(ctx, "credential lifecycle tick", "source", "tick", "expired", result.Expired, "first_touch", result.FirstTouch, "skipped", result.Skipped)
			}
			if result.Skipped > 0 {
				logger.WarnContext(ctx, credentialLifecycleTickSkippedMessage, "reason", "empty_scope", "skipped", result.Skipped)
			}
			if result.Full {
				logger.InfoContext(ctx, credentialLifecycleTickFullMessage)
			}
		}
		return result.Expired + result.FirstTouch, err
	}
	return startBoundedPurgeLoop(ctx, purge, nil, observe, defaultCredentialLifecycleTickInterval, defaultCredentialLifecycleTickBatchLimit, credentialLifecycleTickFailureMessage)
}
