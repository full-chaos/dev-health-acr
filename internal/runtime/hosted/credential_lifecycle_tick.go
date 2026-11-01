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
	// credentialLifecycleTickSkippedMessage is the fixed line of a credential no
	// event can describe (an empty scope list): it names the credential every
	// tick until someone revokes it, and the retention gate keeps it undeleted.
	credentialLifecycleTickSkippedMessage = "credential lifecycle tick skipped a credential no event can describe; revoke it"
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
			for _, id := range result.SkippedIDs {
				logger.WarnContext(ctx, credentialLifecycleTickSkippedMessage, "credential_id", id)
			}
			if result.Full {
				logger.InfoContext(ctx, credentialLifecycleTickFullMessage)
			}
		}
		return result.Expired + result.FirstTouch, err
	}
	return startBoundedPurgeLoop(ctx, purge, nil, observe, defaultCredentialLifecycleTickInterval, defaultCredentialLifecycleTickBatchLimit, credentialLifecycleTickFailureMessage)
}
