package hosted

import (
	"context"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// The Ask Dev retention job (CHAOS-11952, plan §2.6 and D-22): a bounded
// purge loop like the packet, OAuth and browser family purges, in every
// acr-api replica. Its first pass runs synchronously at startup and doubles as
// the DELETE-privilege proof: a runtime role missing a grant on the backing
// set fails startup loudly (internal/dbacl runtime grants). Each pass handles
// up to contextfabric.RetentionSweepBatch units, each in its own transaction
// under the organization's admission lock, so replicas sweeping at once
// remove each unit once.
const (
	defaultRetentionPurgeInterval = 5 * time.Minute
	// retentionPurgeTimeout bounds one pass: a batch of units, each a short
	// transaction.
	retentionPurgeTimeout        = 2 * time.Minute
	retentionPurgeFailureMessage = "ask dev retention tick failed; retrying on next tick"
)

// RetentionPurgeLogMessage is the retention pass's line: Info with the counts
// only (never an id, a question or an owner) when a pass removed, erased or
// fenced anything. A pass that found nothing writes nothing.
const RetentionPurgeLogMessage = "context fabric retention sweep"

// retentionPurgeFunc adapts a contextfabric.RetentionSweeper to the bounded
// purge loop.
func retentionPurgeFunc(sweeper contextfabric.RetentionSweeper, logger *slog.Logger) packetPurgeFunc {
	return func(ctx context.Context, now time.Time, limit int) (int, error) {
		sweepCtx, cancel := context.WithTimeout(ctx, retentionPurgeTimeout)
		defer cancel()
		sweep, err := sweeper.SweepRetention(sweepCtx, now, limit)
		if (sweep.Changed() || sweep.Fenced > 0) && logger != nil {
			logger.InfoContext(ctx, RetentionPurgeLogMessage,
				"threads_removed", sweep.ThreadsRemoved, "threads_erased", sweep.ThreadsErased,
				"records_removed", sweep.RecordsRemoved, "results_removed", sweep.ResultsRemoved, "fenced", sweep.Fenced)
		}
		return sweep.ThreadsRemoved + sweep.ThreadsErased + sweep.RecordsRemoved, err
	}
}

// startRetentionPurgeLoop starts the retention job; see the file comment.
func startRetentionPurgeLoop(ctx context.Context, sweeper contextfabric.RetentionSweeper, logger *slog.Logger, now func() time.Time) (func() error, error) {
	return startBoundedPurgeLoop(ctx, retentionPurgeFunc(sweeper, logger), now, packetPurgeSlogObserver(logger), defaultRetentionPurgeInterval, contextfabric.RetentionSweepBatch, retentionPurgeFailureMessage)
}
