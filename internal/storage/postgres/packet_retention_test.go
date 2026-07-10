package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestPacketStorePurgeExpiredWithAudit_deletesAndRecordsEverySnapshot(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	db, state := openPacketDB(t)
	current := now.Add(-time.Minute)
	store, err := NewPacketStore(db, func() time.Time { return current })
	if err != nil {
		t.Fatal(err)
	}
	principal := storage.Principal{OrgID: "00000000-0000-0000-0000-000000000001", RepositoryScopes: []string{"*"}}
	for _, id := range []string{"pkt-purge-001", "pkt-purge-002"} {
		if err := store.SaveSnapshot(context.Background(), principal, postgresPacket(now, id), now); err != nil {
			t.Fatal(err)
		}
	}
	count, err := store.PurgeExpiredWithAudit(context.Background(), now, 10)
	if err != nil || count != 2 {
		t.Fatalf("purge = (%d, %v), want (2, nil)", count, err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.rows) != 0 || len(state.audits) != 2 {
		t.Fatalf("rows/audits = (%d, %d), want (0, 2)", len(state.rows), len(state.audits))
	}
	for _, audit := range state.audits {
		if !strings.HasPrefix(audit.packetID, "pkt-purge-") || !strings.Contains(audit.metadata, "expires_at") || !strings.Contains(audit.metadata, "cutoff") {
			t.Fatalf("unexpected audit: %#v", audit)
		}
	}
}
