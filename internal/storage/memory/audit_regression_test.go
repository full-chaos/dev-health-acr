package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type namedTimePointer *time.Time

func TestAuditStore_deep_copies_metadata_on_record_and_read(t *testing.T) {
	// Given
	store := NewAuditStore()
	expiresAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	wantExpiresAt := expiresAt
	metadata := map[string]any{
		"scopes":  []string{"context:read"},
		"expires": &expiresAt,
		"nested":  map[string]any{"values": []string{"one"}},
	}
	if err := store.Record(context.Background(), storage.AuditEvent{Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	metadata["scopes"].([]string)[0] = "episode:write"
	*metadata["expires"].(*time.Time) = expiresAt.Add(time.Hour)
	metadata["nested"].(map[string]any)["values"].([]string)[0] = "two"

	// When
	events := store.Events()
	events[0].Metadata["scopes"].([]string)[0] = "mutated"
	events[0].Metadata["expires"].(*time.Time).Add(time.Hour)
	events[0].Metadata["nested"].(map[string]any)["values"].([]string)[0] = "mutated"
	stored := store.Events()[0]

	// Then
	if got := stored.Metadata["scopes"].([]string)[0]; got != "context:read" {
		t.Fatalf("stored scope = %q, want context:read", got)
	}
	if got := *stored.Metadata["expires"].(*time.Time); !got.Equal(wantExpiresAt) {
		t.Fatalf("stored expiry = %s, want %s", got, wantExpiresAt)
	}
	if got := stored.Metadata["nested"].(map[string]any)["values"].([]string)[0]; got != "one" {
		t.Fatalf("stored nested value = %q, want one", got)
	}
}

func TestAuditStore_recursivelySnapshotsTypedMutableMetadata(t *testing.T) {
	// Given
	store := NewAuditStore()
	when := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	metadata := map[string]any{
		"bytes":  []byte("safe"),
		"labels": map[string]string{"environment": "test"},
		"nested": []map[string][]int{{"values": {1, 2}}},
		"when":   when,
	}
	if err := store.Record(context.Background(), storage.AuditEvent{Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	metadata["bytes"].([]byte)[0] = 'x'
	metadata["labels"].(map[string]string)["environment"] = "changed"
	metadata["nested"].([]map[string][]int)[0]["values"][0] = 9

	// When
	events := store.Events()
	events[0].Metadata["bytes"].([]byte)[1] = 'x'
	events[0].Metadata["labels"].(map[string]string)["environment"] = "returned"
	events[0].Metadata["nested"].([]map[string][]int)[0]["values"][1] = 9
	stored := store.Events()[0]

	// Then
	if got := string(stored.Metadata["bytes"].([]byte)); got != "safe" {
		t.Fatalf("stored bytes = %q, want safe", got)
	}
	if got := stored.Metadata["labels"].(map[string]string)["environment"]; got != "test" {
		t.Fatalf("stored label = %q, want test", got)
	}
	if got := stored.Metadata["nested"].([]map[string][]int)[0]["values"]; got[0] != 1 || got[1] != 2 {
		t.Fatalf("stored nested values = %#v, want [1 2]", got)
	}
	if got := stored.Metadata["when"].(time.Time); !got.Equal(when) {
		t.Fatalf("stored time = %s, want %s", got, when)
	}
}

func TestAuditStore_rejectsUnsupportedOrCyclicMutableMetadata(t *testing.T) {
	// Given
	store := NewAuditStore()
	cyclic := map[string]any{}
	cyclic["self"] = cyclic

	// When
	unsupportedErr := store.Record(context.Background(), storage.AuditEvent{Metadata: map[string]any{"channel": make(chan string)}})
	cyclicErr := store.Record(context.Background(), storage.AuditEvent{Metadata: cyclic})

	// Then
	if unsupportedErr == nil || cyclicErr == nil {
		t.Fatalf("Record() errors = %v, %v; want unsupported and cyclic metadata rejected", unsupportedErr, cyclicErr)
	}
}

func TestAuditStore_preservesNamedPointerMetadataType(t *testing.T) {
	// Given
	store := NewAuditStore()
	when := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	pointer := namedTimePointer(&when)

	// When
	err := store.Record(context.Background(), storage.AuditEvent{Metadata: map[string]any{"when": pointer}})

	// Then
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := store.Events()[0].Metadata["when"].(namedTimePointer)
	if !ok || !time.Time(*stored).Equal(when) {
		t.Fatalf("stored named time pointer = %#v; want preserved immutable type", store.Events()[0].Metadata["when"])
	}
	*stored = time.Time(*stored).Add(time.Hour)
	unchanged := store.Events()[0].Metadata["when"].(namedTimePointer)
	if !time.Time(*unchanged).Equal(when) {
		t.Fatalf("stored named time pointer mutated through returned event: %s", time.Time(*unchanged))
	}
}

func TestAuditStore_rejectsReservedCredentialLifecycleActions(t *testing.T) {
	// Given
	store := NewAuditStore()

	// When
	err := store.Record(context.Background(), storage.AuditEvent{Action: "credential_created"})

	// Then
	if err == nil || len(store.Events()) != 0 {
		t.Fatalf("Record() = %v, events = %#v; want reserved lifecycle action rejected", err, store.Events())
	}
}

func TestAuditStore_rejectsCanceledContextWithoutRecording(t *testing.T) {
	// Given
	store := NewAuditStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	err := store.Record(ctx, storage.AuditEvent{Action: "credential_used"})

	// Then
	if !errors.Is(err, context.Canceled) || len(store.Events()) != 0 {
		t.Fatalf("Record() = %v, events = %#v; want canceled with no event", err, store.Events())
	}
}
