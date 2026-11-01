package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/authprojection"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The publisher's two statements, exported so its privilege set is derived from the text it executes.
const (
	// LifecycleOutboxClaimSQL takes the oldest unpublished event whose aggregate is not in $1 (the publisher's held
	// set) and has no earlier unpublished revision. A head another pod holds stays unpublished, so its later
	// revisions stay ineligible: SKIP LOCKED never reorders an aggregate. The row lock is the claim.
	LifecycleOutboxClaimSQL = `SELECT o.aggregate_id, o.revision, o.payload::text FROM acr.credential_lifecycle_outbox o
WHERE o.published_at IS NULL
  AND o.aggregate_id <> ALL($1::text[])
  AND NOT EXISTS (SELECT 1 FROM acr.credential_lifecycle_outbox p
                  WHERE p.aggregate_id = o.aggregate_id AND p.revision < o.revision AND p.published_at IS NULL)
ORDER BY o.created_at, o.aggregate_id, o.revision
LIMIT 1
FOR UPDATE OF o SKIP LOCKED`
	// LifecycleOutboxMarkSQL marks the claimed event published.
	LifecycleOutboxMarkSQL = `UPDATE acr.credential_lifecycle_outbox SET published_at = now()
WHERE aggregate_id = $1 AND revision = $2 AND published_at IS NULL`

	// outboxPublishStatementTimeoutMS and outboxPublishIdleInTxTimeoutMS bound one claim: the transaction is idle
	// while the POST runs (authprojection.RequestTimeout, 5 s), so the idle limit sits well above it.
	outboxPublishStatementTimeoutMS = 10000
	outboxPublishIdleInTxTimeoutMS  = 20000
)

// OutboxPublisher is the store side of the lifecycle outbox publisher.
type OutboxPublisher struct{ db *sql.DB }

func NewOutboxPublisher(db *sql.DB) (*OutboxPublisher, error) {
	if db == nil {
		return nil, storage.ErrInvalidCredentialLifecycle
	}
	return &OutboxPublisher{db: db}, nil
}

// PublishNext is one event per transaction: claim, send, mark, commit. A send that does not ack rolls back and
// leaves the row unpublished. A crash or a failed commit after an ack re-sends that one event, which the endpoint
// answers as a duplicate.
func (p *OutboxPublisher) PublishNext(ctx context.Context, held []string, send func(context.Context, authprojection.Event) bool) (bool, authprojection.Event, error) {
	if p == nil || p.db == nil || send == nil {
		return false, authprojection.Event{}, storage.ErrInvalidCredentialLifecycle
	}
	if held == nil {
		held = []string{}
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, authprojection.Event{}, fmt.Errorf("begin lifecycle outbox publish: %w", sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	for _, limit := range []string{
		fmt.Sprintf("SET LOCAL statement_timeout = %d", outboxPublishStatementTimeoutMS),
		fmt.Sprintf("SET LOCAL idle_in_transaction_session_timeout = %d", outboxPublishIdleInTxTimeoutMS),
	} {
		if _, err := tx.ExecContext(ctx, limit); err != nil {
			return false, authprojection.Event{}, fmt.Errorf("bound lifecycle outbox publish: %w", sanitizeDatabaseError(err))
		}
	}
	var event authprojection.Event
	var body string
	err = tx.QueryRowContext(ctx, LifecycleOutboxClaimSQL, held).Scan(&event.AggregateID, &event.Revision, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return false, authprojection.Event{}, nil
	}
	if err != nil {
		return false, authprojection.Event{}, fmt.Errorf("claim lifecycle outbox event: %w", sanitizeDatabaseError(err))
	}
	event.Body = []byte(body)
	if !send(ctx, event) {
		return true, event, nil
	}
	result, err := tx.ExecContext(ctx, LifecycleOutboxMarkSQL, event.AggregateID, event.Revision)
	if err != nil {
		return true, event, fmt.Errorf("mark lifecycle outbox event published: %w", sanitizeDatabaseError(err))
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return true, event, fmt.Errorf("mark lifecycle outbox event published: %w", storage.ErrInvalidCredentialLifecycle)
	}
	if err := tx.Commit(); err != nil {
		return true, event, fmt.Errorf("commit lifecycle outbox publish: %w", sanitizeDatabaseError(err))
	}
	return true, event, nil
}
