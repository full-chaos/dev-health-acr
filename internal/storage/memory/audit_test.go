package memory

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestAuditStoreCopiesMetadataAtItsBoundary(t *testing.T) {
	store := NewAuditStore()
	metadata := map[string]any{"reason": "retention expired"}
	if err := store.Record(context.Background(), storage.AuditEvent{Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	metadata["reason"] = "mutated by caller"

	events := store.Events()
	events[0].Metadata["reason"] = "mutated by reader"
	if got := store.Events()[0].Metadata["reason"]; got != "retention expired" {
		t.Fatalf("audit metadata mutation escaped store: %q", got)
	}
}
