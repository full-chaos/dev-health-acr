package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/storage/internal/credentiallifecycle"
	"github.com/jackc/pgx/v5/pgconn"
)

// lifecycleSink appends credential lifecycle events to the outbox inside the
// caller's transaction: the row commits with the mutation or not at all.
type lifecycleSink struct {
	tx    *sql.Tx
	newID func() (string, error)
}

func (a *AuditStore) lifecycleSink(tx *sql.Tx) lifecycleSink {
	return lifecycleSink{tx: tx, newID: a.GenerateID}
}

// appendLifecycle writes one lifecycle event (and, for a revocation of a
// credential the stream has not seen, its revision 1) in tx. Any error rolls
// the mutation back.
func (a *AuditStore) appendLifecycle(ctx context.Context, tx *sql.Tx, facts credentiallifecycle.Facts) error {
	if err := credentiallifecycle.Append(ctx, a.lifecycleSink(tx), facts); err != nil {
		return fmt.Errorf("record credential lifecycle event: %w", err)
	}
	return nil
}

func (a *AuditStore) ensureLifecycleIssued(ctx context.Context, tx *sql.Tx, facts credentiallifecycle.Facts) error {
	if err := credentiallifecycle.EnsureIssued(ctx, a.lifecycleSink(tx), facts.Credential); err != nil {
		return fmt.Errorf("record credential lifecycle event: %w", err)
	}
	return nil
}

func (s lifecycleSink) MaxRevision(ctx context.Context, aggregateID string) (int, error) {
	var max int
	err := s.tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(revision), 0) FROM acr.credential_lifecycle_outbox WHERE aggregate_id = $1`, aggregateID).Scan(&max)
	if err != nil {
		return 0, fmt.Errorf("read lifecycle revision: %w", sanitizeDatabaseError(err))
	}
	return max, nil
}

func (s lifecycleSink) Insert(ctx context.Context, row credentiallifecycle.Row) error {
	id, err := s.newID()
	if err != nil {
		return fmt.Errorf("generate lifecycle outbox id: %w", err)
	}
	_, err = s.tx.ExecContext(ctx, `
INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at)
VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::jsonb, $7)`,
		id, row.OrgID, row.AggregateID, row.Revision, row.EventType, string(row.Payload), row.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return credentiallifecycle.ErrRevisionConflict
		}
		return fmt.Errorf("insert lifecycle outbox row: %w", sanitizeDatabaseError(err))
	}
	return nil
}
