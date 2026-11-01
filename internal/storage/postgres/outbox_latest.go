package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// LifecycleOutboxLatestEventSQL reads the event type of an aggregate's highest
// revision: the writer's terminal rule asks it as a new statement, so it sees a
// commit that landed after the caller selected the credential.
const LifecycleOutboxLatestEventSQL = "SELECT event_type FROM acr.credential_lifecycle_outbox WHERE aggregate_id = $1 ORDER BY revision DESC LIMIT 1"

// LatestEventType is the event type of the aggregate's highest revision, or the
// empty string when it has no row.
func (s lifecycleSink) LatestEventType(ctx context.Context, aggregateID string) (string, error) {
	var eventType string
	err := s.tx.QueryRowContext(ctx, LifecycleOutboxLatestEventSQL, aggregateID).Scan(&eventType)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read latest lifecycle event: %w", sanitizeDatabaseError(err))
	}
	return eventType, nil
}
