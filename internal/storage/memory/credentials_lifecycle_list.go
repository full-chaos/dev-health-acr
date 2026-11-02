package memory

import (
	"context"
	"fmt"
	"sort"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ListCredentialLifecycle implements storage.CredentialLifecycleLister over
// the outbox rows written so far: the same keyset, status and organization
// rules as the PostgreSQL statement.
func (s *AuditStore) ListCredentialLifecycle(_ context.Context, orgID, after string, limit int) ([]contractsv1.CredentialLifecycleItem, error) {
	if s == nil {
		return nil, storage.ErrUnavailable
	}
	latest := map[string]storage.LifecycleOutboxRow{}
	for _, row := range s.ledger.Rows() {
		if row.OrgID != orgID || row.AggregateID <= after {
			continue
		}
		if current, ok := latest[row.AggregateID]; !ok || row.Revision > current.Revision {
			latest[row.AggregateID] = row
		}
	}
	ids := make([]string, 0, len(latest))
	for id := range latest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	items := make([]contractsv1.CredentialLifecycleItem, 0, len(ids))
	for _, id := range ids {
		row := latest[id]
		status, ok := storage.CredentialLifecycleStatusOfEventType(row.EventType)
		if !ok {
			return nil, fmt.Errorf("list credential lifecycle: unknown event type for %s", id)
		}
		items = append(items, contractsv1.CredentialLifecycleItem{CredentialID: id, Revision: row.Revision, Status: status})
	}
	return items, nil
}
