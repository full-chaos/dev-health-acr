package memory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestPacketStore_roundTripScopesAndPreservesSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := NewPacketStore(func() time.Time { return now })
	owner := storage.Principal{OrgID: "org-owner", RepositoryScopes: []string{"example-org/widget-service"}}
	packet := snapshotPacket(now)
	if err := store.SaveSnapshot(context.Background(), owner, packet, now.Add(30*24*time.Hour)); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	want, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	packet.Goal = "mutated after save"
	got, err := store.GetSnapshot(context.Background(), owner, "pkt-snapshot-001")
	if err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	actual, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(want) {
		t.Fatalf("snapshot changed after save\n got: %s\nwant: %s", actual, want)
	}
	if _, err := store.GetSnapshot(context.Background(), storage.Principal{OrgID: "org-foreign", RepositoryScopes: []string{"*"}}, got.ContextPacketID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("foreign organization lookup error = %v, want not found", err)
	}
	if _, err := store.GetSnapshot(context.Background(), storage.Principal{OrgID: owner.OrgID, RepositoryScopes: []string{"other/repo"}}, got.ContextPacketID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("foreign repository lookup error = %v, want not found", err)
	}
	if err := store.SaveSnapshot(context.Background(), storage.Principal{OrgID: "org-foreign", RepositoryScopes: []string{"*"}}, got, now.Add(30*24*time.Hour)); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("cross-organization save error = %v, want conflict", err)
	}
	if err := store.SaveSnapshot(context.Background(), owner, got, now.Add(30*24*time.Hour)); err != nil {
		t.Fatalf("idempotent save: %v", err)
	}
	got.Summary = "conflicting retry"
	if err := store.SaveSnapshot(context.Background(), owner, got, now.Add(30*24*time.Hour)); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("conflicting save error = %v, want conflict", err)
	}
}

func TestPacketStore_deniesExpiredAndPurgesSnapshots(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	current := now.Add(-time.Minute)
	audit := NewAuditStore()
	store := NewPacketStoreWithAudit(func() time.Time { return current }, audit)
	principal := storage.Principal{OrgID: "org-owner", RepositoryScopes: []string{"*"}}
	packet := snapshotPacket(now)
	if err := store.SaveSnapshot(context.Background(), principal, packet, now); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	current = now
	if _, err := store.GetSnapshot(context.Background(), principal, packet.ContextPacketID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expired lookup error = %v, want not found", err)
	}
	if removed, err := store.PurgeExpired(context.Background(), now, 1); err != nil || removed != 1 {
		t.Fatalf("purge expired = (%d, %v), want (1, nil)", removed, err)
	}
	if events := audit.Events(); len(events) != 1 || events[0].ResourceID != packet.ContextPacketID {
		t.Fatalf("unexpected purge audit events: %#v", events)
	}
	packet.SchemaVersion = "invalid"
	if err := store.SaveSnapshot(context.Background(), principal, packet, now.Add(time.Hour)); err == nil {
		t.Fatal("malformed packet save succeeded")
	}
	packet = snapshotPacket(now)
	packet.Status = "invalid"
	if err := store.SaveSnapshot(context.Background(), principal, packet, now.Add(time.Hour)); err == nil {
		t.Fatal("invalid packet status save succeeded")
	}
	packet = snapshotPacket(now)
	if err := store.SaveSnapshot(context.Background(), principal, packet, now); err == nil {
		t.Fatal("already-expired packet save succeeded")
	}
}

func TestPacketStore_canonicalJSONTreatsKeyOrderAsIdempotent(t *testing.T) {
	if !sameJSON([]byte(`{"a":1,"b":2}`), []byte(`{"b":2,"a":1}`)) {
		t.Fatal("equivalent JSON key orders did not compare equal")
	}
}

func TestPacketStore_rejectsCancelledOperations(t *testing.T) {
	store := NewPacketStore(time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	principal := storage.Principal{OrgID: "org-owner", RepositoryScopes: []string{"*"}}
	if err := store.SaveSnapshot(ctx, principal, snapshotPacket(time.Now()), time.Now().Add(time.Hour)); !errors.Is(err, context.Canceled) {
		t.Fatalf("save error = %v, want canceled", err)
	}
	if _, err := store.GetSnapshot(ctx, principal, "pkt-snapshot-001"); !errors.Is(err, context.Canceled) {
		t.Fatalf("get error = %v, want canceled", err)
	}
	if _, err := store.PurgeExpired(ctx, time.Now(), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("purge error = %v, want canceled", err)
	}
}

func TestPacketStore_rejectsUnauditedPurge(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	current := now.Add(-time.Minute)
	store := NewPacketStore(func() time.Time { return current })
	principal := storage.Principal{OrgID: "org-owner", RepositoryScopes: []string{"*"}}
	if err := store.SaveSnapshot(context.Background(), principal, snapshotPacket(now), now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PurgeExpired(context.Background(), now, 1); err == nil {
		t.Fatal("unaudited purge succeeded")
	}
}

func TestPacketStore_rejectsFallibleAuditBeforePurge(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	current := now.Add(-time.Minute)
	failing := &failingAuditStore{}
	store := NewPacketStoreWithAudit(func() time.Time { return current }, failing)
	principal := storage.Principal{OrgID: "org-owner", RepositoryScopes: []string{"*"}}
	if err := store.SaveSnapshot(context.Background(), principal, snapshotPacket(now), now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PurgeExpired(context.Background(), now, 1); err == nil || failing.calls != 0 {
		t.Fatalf("fallible audit purge = (%v, %d calls), want error before audit", err, failing.calls)
	}
	store.audit = NewAuditStore()
	if count, err := store.PurgeExpired(context.Background(), now, 1); err != nil || count != 1 {
		t.Fatalf("snapshot remained after rejected purge: (%d, %v)", count, err)
	}
}

type failingAuditStore struct{ calls int }

func (s *failingAuditStore) Record(context.Context, storage.AuditEvent) error {
	s.calls++
	return errors.New("audit unavailable")
}

func snapshotPacket(now time.Time) contractsv1.ContextPacket {
	return contractsv1.ContextPacket{
		SchemaVersion: contractsv1.ContextPacketSchema, ContextPacketID: "pkt-snapshot-001", RequestID: "req-snapshot-001", GeneratedAt: now,
		Status: contractsv1.PacketComplete, Goal: "Investigate the fixture", Repository: contractsv1.RepositoryRef{RepoID: "repo-001", Slug: "example-org/widget-service"},
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo-001", RepoSlug: "example-org/widget-service", Resolution: contractsv1.ScopeBranchFiltered, FallbackReasons: []string{}},
		QueryVersion:  "context-query.v1", RankingVersion: "context-ranking.v1", Summary: "Saved exactly.", Items: []contractsv1.ContextPacketItem{},
		RequiredChecks: []contractsv1.RequiredCheck{}, RecommendedNextSteps: []contractsv1.RecommendedStep{}, Freshness: contractsv1.Freshness{AsOf: now, Watermarks: []contractsv1.SourceWatermark{}},
		Coverage: contractsv1.Coverage{SourcesConsidered: []string{}, SourcesAvailable: []string{}, SourcesUnavailable: []contractsv1.UnavailableSource{}, DegradedReasons: []string{}},
		Budget:   contractsv1.PacketBudget{MaxItems: 1, MaxOutputTokens: 500, MaxSerializedBytes: 8192}, Warnings: []string{},
		Compatibility: contractsv1.Compatibility{ServiceVersion: "test", MinimumSidecarVersion: "0.1.0", SupportedSchemaVersions: []string{contractsv1.ContextPacketSchema}},
	}
}
