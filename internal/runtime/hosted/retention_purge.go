package hosted

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
)

// The Ask Dev retention job (CHAOS-11952, plan §2.6 and D-22): a bounded
// purge loop like the packet, OAuth and browser family purges, in every
// acr-api replica, on a pool of its own connected as the retention role
// (ACR_POSTGRES_RETENTION_DSN, internal/dbacl RetentionGrants), started only
// when ACR_ASK_DEV_RETENTION_ENABLED is on. At startup the grant probe runs,
// then a first pass: a role missing a grant fails startup loudly. Each pass handles
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

// RetentionPurgeLogMessage is the retention pass's line: one per pass at Info,
// a heartbeat, with the counts only (never an id, a question or an owner) and
// outcome "removed" or "empty", so a pass that silently stops finding due rows
// is visible at Info (codex #1808 r2). A failed pass is the loop's Warn line.
const RetentionPurgeLogMessage = "context fabric retention sweep"

// retentionPurgeFunc adapts a contextfabric.RetentionSweeper to the bounded
// purge loop.
func retentionPurgeFunc(sweeper contextfabric.RetentionSweeper, logger *slog.Logger) packetPurgeFunc {
	return func(ctx context.Context, now time.Time, limit int) (int, error) {
		sweepCtx, cancel := context.WithTimeout(ctx, retentionPurgeTimeout)
		defer cancel()
		sweep, err := sweeper.SweepRetention(sweepCtx, now, limit)
		if err == nil && logger != nil {
			outcome := "empty"
			if sweep.Changed() || sweep.Fenced > 0 {
				outcome = "removed"
			}
			logger.InfoContext(ctx, RetentionPurgeLogMessage, "outcome", outcome,
				"threads_removed", sweep.ThreadsRemoved, "threads_erased", sweep.ThreadsErased,
				"records_removed", sweep.RecordsRemoved, "results_removed", sweep.ResultsRemoved, "fenced", sweep.Fenced)
		}
		return sweep.ThreadsRemoved + sweep.ThreadsErased + sweep.RecordsRemoved, err
	}
}

// startRetentionPurgeLoop starts the retention job; see the file comment. The
// grant probe runs first, so a role missing a grant fails startup even when
// nothing is due yet (an empty first pass runs no DELETE).
func startRetentionPurgeLoop(ctx context.Context, sweeper contextfabric.RetentionSweeper, logger *slog.Logger, now func() time.Time) (func() error, error) {
	probeCtx, cancel := context.WithTimeout(ctx, retentionPurgeTimeout)
	err := sweeper.ProbeRetention(probeCtx)
	cancel()
	if err != nil {
		return nil, err
	}
	return startBoundedPurgeLoop(ctx, retentionPurgeFunc(sweeper, logger), now, packetPurgeSlogObserver(logger), defaultRetentionPurgeInterval, contextfabric.RetentionSweepBatch, retentionPurgeFailureMessage)
}

// startConfiguredRetention starts the retention job when
// ACR_ASK_DEV_RETENTION_ENABLED is on: it opens a pool of its own from
// ACR_POSTGRES_RETENTION_DSN (the retention role, internal/dbacl
// RetentionGrants), runs the grant probe and a first pass, then the loop. Off,
// it opens nothing, probes nothing, removes nothing, and logs once that
// retention is off. The returned close stops the loop and closes the pool.
func startConfiguredRetention(ctx context.Context, cfg config.Config, logger *slog.Logger) (func() error, error) {
	if !cfg.AskDevRetentionEnabled {
		if logger != nil {
			logger.InfoContext(ctx, RetentionPurgeLogMessage, "outcome", "disabled", "reason", "ACR_ASK_DEV_RETENTION_ENABLED is off; no row is removed")
		}
		return func() error { return nil }, nil
	}
	pool, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: cfg.AskDevRetentionPostgresDSN, PingTimeout: cfg.PostgresPingTimeout})
	if err != nil {
		return nil, fmt.Errorf("open the retention role's pool: %w", err)
	}
	store, err := pginvestigation.NewStore(pool, pginvestigation.WithUnthreadedRecordRetention(true))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create the retention store: %w", err), pool.Close())
	}
	stop, err := startRetentionPurgeLoop(ctx, store, logger, nil)
	if err != nil {
		return nil, errors.Join(err, pool.Close())
	}
	return func() error { return errors.Join(stop(), pool.Close()) }, nil
}
