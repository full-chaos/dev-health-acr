package episode

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestCreateIsScopedIdempotentAndAudited(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	service, err := NewService(memory.NewEpisodeStore(), audit, ServiceOptions{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := episodePrincipal("00000000-0000-0000-0000-000000000001")
	created, duplicate, err := service.Create(context.Background(), principal, episodeCreate())
	if err != nil || duplicate {
		t.Fatalf("first create = (%#v, %t, %v)", created, duplicate, err)
	}
	retried, duplicate, err := service.Create(context.Background(), principal, episodeCreate())
	if err != nil || !duplicate || retried.EpisodeID != created.EpisodeID {
		t.Fatalf("retry = (%#v, %t, %v)", retried, duplicate, err)
	}
	if _, err := service.Get(context.Background(), episodePrincipal("00000000-0000-0000-0000-000000000002"), created.ClientEpisodeID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-org read error = %v, want not found", err)
	}
	if events := audit.Events(); !sameActions(events, "agent_episode_create_requested", "agent_episode_created", "agent_episode_create_requested", "agent_episode_replayed") {
		t.Fatalf("audit events = %#v", events)
	}
}

func TestCreateRejectsTranscriptContentAndKeepsNoPersistUnreadable(t *testing.T) {
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	principal := episodePrincipal("org_1")
	invalid := episodeCreate()
	invalid.Transcript = contractsv1.TranscriptRef{Mode: "opaque_ref", OpaqueRef: "raw transcript text"}
	if _, _, err := service.Create(context.Background(), principal, invalid); err == nil {
		t.Fatal("raw transcript payload was accepted")
	}
	noPersist := episodeCreate()
	noPersist.RetentionClass = "no_persist"
	if _, duplicate, err := service.Create(context.Background(), principal, noPersist); !errors.Is(err, storage.ErrNotFound) || duplicate {
		t.Fatalf("no_persist create = (%t, %v)", duplicate, err)
	}
	if _, duplicate, err := service.Create(context.Background(), principal, noPersist); !errors.Is(err, storage.ErrNotFound) || !duplicate {
		t.Fatalf("no_persist retry = (%t, %v)", duplicate, err)
	}
}

func TestRedactUsesScopedTombstoneAndSafeAuditMetadata(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	service, err := NewService(memory.NewEpisodeStore(), audit, ServiceOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	principal := episodePrincipal("org_1")
	created, _, err := service.Create(context.Background(), principal, episodeCreate())
	if err != nil {
		t.Fatal(err)
	}
	redacted, err := service.Redact(context.Background(), principal, created.EpisodeID, "customer request")
	if err != nil || redacted.RedactionState != "redacted" || redacted.Goal != "[redacted]" || redacted.Summary != "[redacted]" {
		t.Fatalf("redact = (%#v, %v)", redacted, err)
	}
	if _, err := service.Redact(context.Background(), episodePrincipal("org_2"), created.EpisodeID, "probe"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-org redaction error = %v", err)
	}
	events := audit.Events()
	if len(events) < 4 || !sameActions(events[:4], "agent_episode_create_requested", "agent_episode_created", "agent_episode_redact_requested", "agent_episode_redacted") || events[3].Metadata["reason"] != "customer request" {
		t.Fatalf("redaction audit events = %#v", events)
	}
}

func TestPurgeExpiredIsScopedAndAudited(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	service, err := NewService(memory.NewEpisodeStore(), audit, ServiceOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	principal := episodePrincipal("org_1")
	create := episodeCreate()
	create.RetentionClass = "short_30d"
	if _, _, err := service.Create(context.Background(), principal, create); err != nil {
		t.Fatal(err)
	}
	purged, err := service.PurgeExpired(context.Background(), principal, now.Add(31*24*time.Hour), 1)
	if err != nil || purged != 1 {
		t.Fatalf("purge = (%d, %v)", purged, err)
	}
	events := audit.Events()
	if !sameActions(events, "agent_episode_create_requested", "agent_episode_created", "agent_episode_purge_requested", "agent_episode_purged") || events[3].Metadata["purged_count"] != 1 {
		t.Fatalf("purge audit events = %#v", events)
	}
}

func TestCreateRequiresWriteScopeAndEntitlement(t *testing.T) {
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	missingScope := episodePrincipal("org_1")
	missingScope.Permissions = nil
	if _, _, err := service.Create(context.Background(), missingScope, episodeCreate()); !errors.Is(err, auth.ErrInsufficientScope) {
		t.Fatalf("missing scope error = %v", err)
	}
	missingEntitlement := episodePrincipal("org_1")
	missingEntitlement.ProductEntitlements = nil
	if _, _, err := service.Create(context.Background(), missingEntitlement, episodeCreate()); !errors.Is(err, ErrEntitlementRequired) {
		t.Fatalf("missing entitlement error = %v", err)
	}
}

func TestServiceRequiresAuditStoreAndRepositoryGrantForPurge(t *testing.T) {
	if _, err := NewService(memory.NewEpisodeStore(), nil, ServiceOptions{}); err == nil {
		t.Fatal("service accepted a nil audit store")
	}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := memory.NewEpisodeStore()
	principal := episodePrincipal("org_1")
	service, err := NewService(store, memory.NewAuditStore(), ServiceOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	create := episodeCreate()
	create.RetentionClass = "short_30d"
	if _, _, err := service.Create(context.Background(), principal, create); err != nil {
		t.Fatal(err)
	}
	noScope := principal
	noScope.RepositoryScopes = nil
	if _, err := service.PurgeExpired(context.Background(), noScope, now.Add(31*24*time.Hour), 1); !errors.Is(err, auth.ErrRepositoryForbidden) {
		t.Fatalf("no-scope purge error = %v", err)
	}
	if _, err := service.Get(context.Background(), principal, create.ClientEpisodeID); err != nil {
		t.Fatalf("no-scope purge mutated episode: %v", err)
	}
}

func TestCompletionAuditFailureDoesNotReportMutationFailure(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	principal := episodePrincipal("org_1")
	store := memory.NewEpisodeStore()
	completionFails := &failOnNthAudit{store: memory.NewAuditStore(), failAt: 2}
	service, err := NewService(store, completionFails, ServiceOptions{Now: func() time.Time { return now }})
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

	redactFails := &failOnNthAudit{store: memory.NewAuditStore(), failAt: 2}
	redactor, err := NewService(store, redactFails, ServiceOptions{Now: func() time.Time { return now }})
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
	creator, err := NewService(store, memory.NewAuditStore(), ServiceOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := creator.Create(context.Background(), principal, purgeCreate); err != nil {
		t.Fatal(err)
	}
	purgeFails := &failOnNthAudit{store: memory.NewAuditStore(), failAt: 2}
	purger, err := NewService(store, purgeFails, ServiceOptions{Now: func() time.Time { return now }})
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

func TestBoundedCountsUnicodeRunes(t *testing.T) {
	if !bounded(strings.Repeat("界", 4), 1, 4) {
		t.Fatal("four Unicode runes were rejected")
	}
	if bounded(strings.Repeat("界", 5), 1, 4) {
		t.Fatal("five Unicode runes were accepted")
	}
}

func episodePrincipal(orgID string) storage.Principal {
	return storage.Principal{
		OrgID: orgID, CredentialID: "cred_01", RepositoryScopes: []string{"owner/repo"}, Permissions: []string{auth.ScopeEpisodeWrite},
		ProductEntitlements: []string{"agent_context_runtime"},
	}
}

type failingAuditStore struct{}

func (failingAuditStore) Record(context.Context, storage.AuditEvent) error {
	return errors.New("audit unavailable")
}

type failOnNthAudit struct {
	store  *memory.AuditStore
	failAt int
	calls  int
}

func (s *failOnNthAudit) Record(ctx context.Context, event storage.AuditEvent) error {
	s.calls++
	if s.calls == s.failAt {
		return errors.New("audit completion unavailable")
	}
	return s.store.Record(ctx, event)
}

func sameActions(events []storage.AuditEvent, actions ...string) bool {
	if len(events) != len(actions) {
		return false
	}
	for index, action := range actions {
		if events[index].Action != action {
			return false
		}
	}
	return true
}

func episodeCreate() contractsv1.AgentEpisodeCreate {
	return contractsv1.AgentEpisodeCreate{
		SchemaVersion: contractsv1.AgentEpisodeCreateSchema, ClientEpisodeID: "client_episode_01", IdempotencyKey: "idempotency_01", ContextPacketID: "packet_01",
		Goal: "Persist a scoped episode", Repository: contractsv1.RepositoryRef{Slug: "owner/repo", RepoID: "00000000-0000-0000-0000-000000000010"},
		Scope:     contractsv1.EpisodeScope{Branch: "main", CommitSHA: "aabbccddeeff"},
		Client:    contractsv1.EpisodeClient{Name: "test", Version: "1.0", SidecarVersion: "1.0"},
		StartedAt: time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC), EndedAt: time.Date(2026, 7, 10, 11, 1, 0, 0, time.UTC),
		Outcome: "succeeded", Summary: "Completed persistence", Artifacts: contractsv1.EpisodeArtifacts{FilesTouched: []string{}, ArtifactURIs: []string{}, TestsRun: []string{}},
		Transcript: contractsv1.TranscriptRef{Mode: "none"}, RetentionClass: "default_90d",
	}
}
