package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// LifecycleOutboxReapSQL deletes WHOLE aggregates of the lifecycle outbox: every row published, the newest published
// before $1, and no credential row left (so a terminal row stays while its credential exists: the retention deletes read
// the expired row). No row is required to end terminal: an unrevoked credential a purge deleted gets no terminal event.
// Rows of one aggregate go together because the writer's revision and terminal reads need the whole history. $2 bounds
// the aggregates a run takes, oldest first.
const LifecycleOutboxReapSQL = `WITH agg AS (
    SELECT aggregate_id, max(published_at) AS last_published
    FROM acr.credential_lifecycle_outbox
    GROUP BY aggregate_id
    HAVING bool_and(published_at IS NOT NULL) AND max(published_at) < $1
), doomed AS (
    SELECT a.aggregate_id, a.last_published FROM agg a
    WHERE NOT EXISTS (SELECT 1 FROM acr.client_credentials c WHERE c.credential_id = a.aggregate_id)
    ORDER BY a.last_published, a.aggregate_id
    LIMIT $2
), deleted AS (
    DELETE FROM acr.credential_lifecycle_outbox d USING doomed
    WHERE d.aggregate_id = doomed.aggregate_id
    RETURNING d.aggregate_id
)
SELECT count(DISTINCT aggregate_id), count(*) FROM deleted`

// outboxReapStatementTimeoutMS bounds one run: the statement reads the whole outbox once.
const outboxReapStatementTimeoutMS = 60000

// ReapResult counts one run: the aggregates and the rows it deleted.
type ReapResult struct{ Aggregates, Rows int }

// OutboxReaper is the store side of the lifecycle outbox reaper.
type OutboxReaper struct{ db *sql.DB }

func NewOutboxReaper(db *sql.DB) (*OutboxReaper, error) {
	if db == nil {
		return nil, storage.ErrInvalidCredentialLifecycle
	}
	return &OutboxReaper{db: db}, nil
}

// ReapPublished runs the one statement in its own bounded transaction. A failure deletes nothing.
func (r *OutboxReaper) ReapPublished(ctx context.Context, olderThan time.Time, limit int) (ReapResult, error) {
	if r == nil || r.db == nil || olderThan.IsZero() || limit <= 0 {
		return ReapResult{}, storage.ErrInvalidCredentialLifecycle
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ReapResult{}, fmt.Errorf("begin lifecycle outbox reap: %w", sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", outboxReapStatementTimeoutMS)); err != nil {
		return ReapResult{}, fmt.Errorf("bound lifecycle outbox reap: %w", sanitizeDatabaseError(err))
	}
	var result ReapResult
	if err := tx.QueryRowContext(ctx, LifecycleOutboxReapSQL, olderThan.UTC(), limit).Scan(&result.Aggregates, &result.Rows); err != nil {
		return ReapResult{}, fmt.Errorf("reap lifecycle outbox: %w", sanitizeDatabaseError(err))
	}
	if err := tx.Commit(); err != nil {
		return ReapResult{}, fmt.Errorf("commit lifecycle outbox reap: %w", sanitizeDatabaseError(err))
	}
	return result, nil
}
