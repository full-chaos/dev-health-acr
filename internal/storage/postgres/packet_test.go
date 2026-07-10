package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestPacketStorePostgres_roundTripScopesAndExpiry(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	db, state := openPacketDB(t)
	current := now
	store, err := NewPacketStore(db, func() time.Time { return current })
	if err != nil {
		t.Fatal(err)
	}
	owner := storage.Principal{OrgID: "00000000-0000-0000-0000-000000000001", RepositoryScopes: []string{"example-org/widget-service"}}
	packet := postgresPacket(now, "pkt-postgres-001")
	if err := store.SaveSnapshot(context.Background(), owner, packet, now.Add(30*24*time.Hour)); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	want, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	packet.Goal = "mutated after save"
	got, err := store.GetSnapshot(context.Background(), owner, "pkt-postgres-001")
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
	if _, err := store.GetSnapshot(context.Background(), storage.Principal{OrgID: "00000000-0000-0000-0000-000000000002", RepositoryScopes: []string{"*"}}, got.ContextPacketID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("foreign organization lookup error = %v, want not found", err)
	}
	if _, err := store.GetSnapshot(context.Background(), storage.Principal{OrgID: owner.OrgID, RepositoryScopes: []string{"example-org/other-service"}}, got.ContextPacketID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("same-organization foreign repository lookup error = %v, want not found", err)
	}
	if err := store.SaveSnapshot(context.Background(), owner, got, now.Add(30*24*time.Hour)); err != nil {
		t.Fatalf("identical snapshot retry: %v", err)
	}
	conflicting := got
	conflicting.Summary = "a conflicting retry"
	if err := store.SaveSnapshot(context.Background(), owner, conflicting, now.Add(30*24*time.Hour)); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("conflicting snapshot retry error = %v, want conflict", err)
	}
	if err := store.SaveSnapshot(context.Background(), storage.Principal{OrgID: "00000000-0000-0000-0000-000000000002", RepositoryScopes: []string{"*"}}, got, now.Add(30*24*time.Hour)); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("cross-organization save error = %v, want conflict", err)
	}
	expired := postgresPacket(now, "pkt-postgres-expired")
	current = now.Add(-time.Minute)
	if err := store.SaveSnapshot(context.Background(), owner, expired, now); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	current = now
	if _, err := store.GetSnapshot(context.Background(), owner, expired.ContextPacketID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expired lookup error = %v, want not found", err)
	}
	if removed, err := store.PurgeExpired(context.Background(), now, 1); err != nil || removed != 1 {
		t.Fatalf("purge expired = (%d, %v), want (1, nil)", removed, err)
	}
	state.mu.Lock()
	if len(state.audits) != 1 || state.audits[0].packetID != expired.ContextPacketID {
		state.mu.Unlock()
		t.Fatalf("unexpected purge audits: %#v", state.audits)
	}
	state.mu.Unlock()
}

func TestPacketStorePostgres_rejectsMalformedAndConstraintViolatingSnapshots(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	db, state := openPacketDB(t)
	store, err := NewPacketStore(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	principal := storage.Principal{OrgID: "00000000-0000-0000-0000-000000000001", RepositoryScopes: []string{"example-org/widget-service"}}

	malformed := postgresPacket(now, "pkt-postgres-malformed")
	malformed.Status = "not-a-packet-status"
	if err := store.SaveSnapshot(context.Background(), principal, malformed, now.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "invalid packet snapshot") {
		t.Fatalf("malformed snapshot error = %v, want validation rejection", err)
	}
	state.mu.Lock()
	if state.insertAttempts != 0 {
		state.mu.Unlock()
		t.Fatalf("malformed snapshot reached database %d times", state.insertAttempts)
	}
	state.mu.Unlock()

	invalidOrg := principal
	invalidOrg.OrgID = "not-a-uuid"
	if err := store.SaveSnapshot(context.Background(), invalidOrg, postgresPacket(now, "pkt-postgres-invalid-org"), now.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "invalid input syntax for type uuid") {
		t.Fatalf("invalid org UUID error = %v, want PostgreSQL UUID cast rejection", err)
	}

	state.mu.Lock()
	state.insertError = errors.New("new row for relation \"context_packet_snapshots\" violates check constraint \"context_packet_snapshots_status_check\"")
	state.mu.Unlock()
	if err := store.SaveSnapshot(context.Background(), principal, postgresPacket(now, "pkt-postgres-constraint"), now.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "violates check constraint") {
		t.Fatalf("database constraint error = %v, want propagated constraint rejection", err)
	}
}

func TestPacketStore_canonicalJSONTreatsKeyOrderAsIdempotent(t *testing.T) {
	if !sameJSON([]byte(`{"a":1,"b":2}`), []byte(`{"b":2,"a":1}`)) {
		t.Fatal("equivalent JSON key orders did not compare equal")
	}
}

func TestPostgresRepoID_handlesUnresolvedScopeIdentity(t *testing.T) {
	first := postgresRepoID("unresolved:pkt-example")
	if len(first) != 36 || first != postgresRepoID("unresolved:pkt-example") || first == postgresRepoID("repo-other") {
		t.Fatalf("unsafe repository storage id: %q", first)
	}
}

func TestPostgresRepoID_preservesCanonicalUUID(t *testing.T) {
	const repoID = "00000000-0000-0000-0000-000000000101"
	if got := postgresRepoID(repoID); got != repoID {
		t.Fatalf("resolved repository id = %q, want %q", got, repoID)
	}
}

func postgresPacket(now time.Time, id string) contractsv1.ContextPacket {
	return contractsv1.ContextPacket{
		SchemaVersion: contractsv1.ContextPacketSchema, ContextPacketID: id, RequestID: "req-postgres-001", GeneratedAt: now,
		Status: contractsv1.PacketComplete, Goal: "Investigate the fixture", Repository: contractsv1.RepositoryRef{RepoID: "00000000-0000-0000-0000-000000000101", Slug: "example-org/widget-service"},
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "00000000-0000-0000-0000-000000000101", RepoSlug: "example-org/widget-service", Resolution: contractsv1.ScopeBranchFiltered, FallbackReasons: []string{}},
		QueryVersion:  "context-query.v1", RankingVersion: "context-ranking.v1", Summary: "Saved exactly.", Items: []contractsv1.ContextPacketItem{},
		RequiredChecks: []contractsv1.RequiredCheck{}, RecommendedNextSteps: []contractsv1.RecommendedStep{}, Freshness: contractsv1.Freshness{AsOf: now, Watermarks: []contractsv1.SourceWatermark{}},
		Coverage: contractsv1.Coverage{SourcesConsidered: []string{}, SourcesAvailable: []string{}, SourcesUnavailable: []contractsv1.UnavailableSource{}, DegradedReasons: []string{}},
		Budget:   contractsv1.PacketBudget{MaxItems: 1, MaxOutputTokens: 500, MaxSerializedBytes: 8192}, Warnings: []string{},
		Compatibility: contractsv1.Compatibility{ServiceVersion: "test", MinimumSidecarVersion: "0.1.0", SupportedSchemaVersions: []string{contractsv1.ContextPacketSchema}},
	}
}
