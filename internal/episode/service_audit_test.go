package episode

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestPurgeReportsAuditFailure(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := memory.NewEpisodeStore(func() time.Time { return now })
	principal := episodePrincipal("org_1")
	creator, err := NewService(store, memory.NewAuditStore(), withPacketStore(ServiceOptions{Now: func() time.Time { return now }}))
	if err != nil {
		t.Fatal(err)
	}
	create := episodeCreate()
	create.RetentionClass = "short_30d"
	if _, _, err := creator.Create(context.Background(), principal, create); err != nil {
		t.Fatal(err)
	}
	purger, err := NewService(store, failingAuditStore{}, withPacketStore(ServiceOptions{Now: func() time.Time { return now }}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := purger.PurgeExpired(context.Background(), principal, now.Add(31*24*time.Hour), 1); err == nil {
		t.Fatal("purge succeeded after its audit write failed")
	}
	if _, err := creator.Get(context.Background(), principal, create.ClientEpisodeID); err != nil {
		t.Fatalf("audit failure still purged episode: %v", err)
	}
}

func TestCreateAndRedactPreflightAuditFailuresLeaveEpisodeUnchanged(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	principal := episodePrincipal("org_1")
	store := memory.NewEpisodeStore(func() time.Time { return now })
	failing, err := NewService(store, failingAuditStore{}, withPacketStore(ServiceOptions{Now: func() time.Time { return now }}))
	if err != nil {
		t.Fatal(err)
	}
	create := episodeCreate()
	if _, _, err := failing.Create(context.Background(), principal, create); err == nil {
		t.Fatal("create succeeded despite audit preflight failure")
	}
	if _, err := store.GetByClientEpisodeID(context.Background(), principal, create.ClientEpisodeID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("audit-preflight create persisted episode: %v", err)
	}
	creator, err := NewService(store, memory.NewAuditStore(), withPacketStore(ServiceOptions{Now: func() time.Time { return now }}))
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := creator.Create(context.Background(), principal, create)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failing.Redact(context.Background(), principal, created.EpisodeID, "request"); err == nil {
		t.Fatal("redact succeeded despite audit preflight failure")
	}
	stored, err := creator.Get(context.Background(), principal, create.ClientEpisodeID)
	if err != nil || stored.RedactionState != "active" {
		t.Fatalf("audit-preflight redact changed episode = (%#v, %v)", stored, err)
	}
}

func TestNoPersistAttemptsAreAudited(t *testing.T) {
	audit := memory.NewAuditStore()
	service, err := NewService(memory.NewEpisodeStore(nil), audit, withPacketStore(ServiceOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	create := episodeCreate()
	create.RetentionClass = "no_persist"
	principal := episodePrincipal("org_1")
	if _, duplicate, err := service.Create(context.Background(), principal, create); !errors.Is(err, ErrNoPersistAccepted) || duplicate {
		t.Fatalf("no-persist create = (%t, %v)", duplicate, err)
	}
	if _, duplicate, err := service.Create(context.Background(), principal, create); !errors.Is(err, ErrNoPersistAccepted) || !duplicate {
		t.Fatalf("no-persist replay = (%t, %v)", duplicate, err)
	}
	if !sameActions(audit.Events(), "agent_episode_create_requested", "agent_episode_tombstoned", "agent_episode_create_requested", "agent_episode_tombstone_replayed") {
		t.Fatalf("no-persist audit events = %#v", audit.Events())
	}
}

func TestCompletionAuditFailureDoesNotReportMutationFailure(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	principal := episodePrincipal("org_1")
	store := memory.NewEpisodeStore(func() time.Time { return now })
	service, err := NewService(store, &failOnNthAudit{store: memory.NewAuditStore(), failAt: 2}, withPacketStore(ServiceOptions{Now: func() time.Time { return now }}))
	if err != nil {
		t.Fatal(err)
	}
	create := episodeCreate()
	created, _, err := service.Create(context.Background(), principal, create)
	if err != nil {
		t.Fatalf("create reported completion-audit failure: %v", err)
	}
	if _, err := service.Get(context.Background(), principal, create.ClientEpisodeID); err != nil {
		t.Fatalf("created episode is unavailable: %v", err)
	}

	redactor, err := NewService(store, &failOnNthAudit{store: memory.NewAuditStore(), failAt: 2}, withPacketStore(ServiceOptions{Now: func() time.Time { return now }}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := redactor.Redact(context.Background(), principal, created.EpisodeID, "request"); err != nil {
		t.Fatalf("redact reported completion-audit failure: %v", err)
	}
	redacted, err := service.Get(context.Background(), principal, create.ClientEpisodeID)
	if err != nil || redacted.RedactionState != "redacted" {
		t.Fatalf("redacted episode = (%#v, %v)", redacted, err)
	}

	purgeCreate := episodeCreate()
	purgeCreate.ClientEpisodeID, purgeCreate.IdempotencyKey, purgeCreate.RetentionClass = "episode_02", "idempotency_02", "short_30d"
	creator, err := NewService(store, memory.NewAuditStore(), withPacketStore(ServiceOptions{Now: func() time.Time { return now }}))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := creator.Create(context.Background(), principal, purgeCreate); err != nil {
		t.Fatal(err)
	}
	purger, err := NewService(store, &failOnNthAudit{store: memory.NewAuditStore(), failAt: 2}, withPacketStore(ServiceOptions{Now: func() time.Time { return now }}))
	if err != nil {
		t.Fatal(err)
	}
	if purged, err := purger.PurgeExpired(context.Background(), principal, now.Add(31*24*time.Hour), 1); err != nil || purged != 1 {
		t.Fatalf("purge reported completion-audit failure = (%d, %v)", purged, err)
	}
	if _, err := service.Get(context.Background(), principal, purgeCreate.ClientEpisodeID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("purged episode is still readable: %v", err)
	}
}
