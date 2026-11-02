package postgres

import (
	"context"
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CredentialLifecycleListSQL is one page of the reconciliation listing: $1 the
// organization, $2 the cursor (exclusive), $3 the page size. The inner select
// takes the page's distinct aggregates in byte order through
// ix_acr_credential_lifecycle_outbox_org_aggregate (org_id, aggregate_id
// COLLATE "C"), so the walk is a bounded index range of one organization on a
// table of many; the lateral join then reads each aggregate's newest row
// through the (aggregate_id, revision) unique key. It selects event_type and
// revision, never the payload.
const CredentialLifecycleListSQL = `SELECT a.aggregate_id, l.revision, l.event_type
FROM (SELECT DISTINCT o.aggregate_id COLLATE "C" AS aggregate_id
      FROM acr.credential_lifecycle_outbox o
      WHERE o.org_id = $1::uuid AND o.aggregate_id COLLATE "C" > $2::text COLLATE "C"
      ORDER BY 1
      LIMIT $3) a
CROSS JOIN LATERAL (SELECT n.revision, n.event_type
                    FROM acr.credential_lifecycle_outbox n
                    WHERE n.aggregate_id = a.aggregate_id AND n.org_id = $1::uuid
                    ORDER BY n.revision DESC
                    LIMIT 1) l
ORDER BY a.aggregate_id COLLATE "C"`

// ListCredentialLifecycle implements storage.CredentialLifecycleLister.
func (s *AuditStore) ListCredentialLifecycle(ctx context.Context, orgID, after string, limit int) ([]contractsv1.CredentialLifecycleItem, error) {
	if s == nil || s.DB == nil {
		return nil, storage.ErrUnavailable
	}
	rows, err := s.DB.QueryContext(ctx, CredentialLifecycleListSQL, orgID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list credential lifecycle: %w", sanitizeDatabaseError(err))
	}
	defer rows.Close()
	items := make([]contractsv1.CredentialLifecycleItem, 0, limit)
	for rows.Next() {
		var item contractsv1.CredentialLifecycleItem
		var eventType string
		if err := rows.Scan(&item.CredentialID, &item.Revision, &eventType); err != nil {
			return nil, fmt.Errorf("scan credential lifecycle: %w", sanitizeDatabaseError(err))
		}
		status, ok := storage.CredentialLifecycleStatusOfEventType(eventType)
		if !ok {
			return nil, fmt.Errorf("list credential lifecycle: unknown event type for %s", item.CredentialID)
		}
		item.Status = status
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list credential lifecycle: %w", sanitizeDatabaseError(err))
	}
	return items, nil
}
