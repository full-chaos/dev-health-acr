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
	service, err := NewService(memory.NewEpisodeStore(), audit, withPacketStore(ServiceOptions{
		Now: func() time.Time { return now },
	}))
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

func TestCreatePreservesDuplicateAndConflictIdempotencyBehavior(t *testing.T) {
	// Given
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), withPacketStore(ServiceOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	principal := episodePrincipal("org_1")
	create := episodeCreate()

	// When
	created, duplicate, err := service.Create(context.Background(), principal, create)
	if err != nil || duplicate {
		t.Fatalf("create = (%#v, %t, %v)", created, duplicate, err)
	}
	retried, duplicate, err := service.Create(context.Background(), principal, create)
	if err != nil || !duplicate || retried.EpisodeID != created.EpisodeID {
		t.Fatalf("retry = (%#v, %t, %v)", retried, duplicate, err)
	}
	conflicting := create
	conflicting.Summary = "different bounded summary"
	_, _, err = service.Create(context.Background(), principal, conflicting)

	// Then
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("conflicting retry error = %v, want conflict", err)
	}
}

func TestCreateRejectsTranscriptContentAndKeepsNoPersistUnreadable(t *testing.T) {
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), withPacketStore(ServiceOptions{}))
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
	if _, duplicate, err := service.Create(context.Background(), principal, noPersist); !errors.Is(err, ErrNoPersistAccepted) || duplicate {
		t.Fatalf("no_persist create = (%t, %v)", duplicate, err)
	}
	if _, duplicate, err := service.Create(context.Background(), principal, noPersist); !errors.Is(err, ErrNoPersistAccepted) || !duplicate {
		t.Fatalf("no_persist retry = (%t, %v)", duplicate, err)
	}
}

func TestRedactUsesScopedTombstoneAndSafeAuditMetadata(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	service, err := NewService(memory.NewEpisodeStore(), audit, withPacketStore(ServiceOptions{Now: func() time.Time { return now }}))
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
	service, err := NewService(memory.NewEpisodeStore(), audit, withPacketStore(ServiceOptions{Now: func() time.Time { return now }}))
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
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), withPacketStore(ServiceOptions{}))
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
	if _, err := NewService(memory.NewEpisodeStore(), nil, withPacketStore(ServiceOptions{})); err == nil {
		t.Fatal("service accepted a nil audit store")
	}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	store := memory.NewEpisodeStore()
	principal := episodePrincipal("org_1")
	service, err := NewService(store, memory.NewAuditStore(), withPacketStore(ServiceOptions{Now: func() time.Time { return now }}))
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

func TestBoundedCountsUnicodeRunes(t *testing.T) {
	if !bounded(strings.Repeat("界", 4), 1, 4) {
		t.Fatal("four Unicode runes were rejected")
	}
	if bounded(strings.Repeat("界", 5), 1, 4) {
		t.Fatal("five Unicode runes were accepted")
	}
}

// episodePrincipal is deliberately granted both episode:write and
// episode:read: it is the shared "fully authorized" fixture used across
// this package's Create/Redact/Purge tests, several of which also call
// Service.Get purely to observe post-mutation state (not to test read
// authorization itself). Tests that specifically probe read-scope or
// entitlement enforcement (e.g.
// TestService_Get_requiresReadScopeAndEntitlement) start from this and
// strip the specific grant under test.
func episodePrincipal(orgID string) storage.Principal {
	return storage.Principal{
		OrgID: orgID, CredentialID: "cred_01", RepositoryScopes: []string{"owner/repo"}, Permissions: []string{auth.ScopeEpisodeWrite, auth.ScopeEpisodeRead},
		ProductEntitlements: []string{"agent_context_runtime"},
	}
}

// TestService_Get_requiresReadScopeAndEntitlement is the regression test for
// the Codex cross-system review finding X3: Service.Get (the client-ID
// lookup) skipped authorizeRead entirely, unlike GetByID/List, which both
// enforce it. Exhaustive grep found zero production callers of Service.Get
// today, so this is defense-in-depth rather than a live bypass -- but the
// ruling was to add the check anyway, for consistency with the other two
// read methods and so any future caller inherits the same guarantee.
func TestService_Get_requiresReadScopeAndEntitlement(t *testing.T) {
	service, err := NewService(memory.NewEpisodeStore(), memory.NewAuditStore(), withPacketStore(ServiceOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	principal := episodePrincipal("org_1")
	create := episodeCreate()
	if _, _, err := service.Create(context.Background(), principal, create); err != nil {
		t.Fatal(err)
	}

	missingScope := principal
	missingScope.Permissions = []string{auth.ScopeEpisodeWrite}
	if _, err := service.Get(context.Background(), missingScope, create.ClientEpisodeID); !errors.Is(err, auth.ErrInsufficientScope) {
		t.Fatalf("missing read scope error = %v, want ErrInsufficientScope", err)
	}

	missingEntitlement := principal
	missingEntitlement.ProductEntitlements = nil
	if _, err := service.Get(context.Background(), missingEntitlement, create.ClientEpisodeID); !errors.Is(err, ErrEntitlementRequired) {
		t.Fatalf("missing entitlement error = %v, want ErrEntitlementRequired", err)
	}

	if _, err := service.Get(context.Background(), principal, create.ClientEpisodeID); err != nil {
		t.Fatalf("fully authorized get failed: %v", err)
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

type matchingPacketStore struct{}

func (matchingPacketStore) SaveSnapshot(context.Context, storage.Principal, contractsv1.ContextPacket, time.Time) error {
	return nil
}

func (matchingPacketStore) GetSnapshot(context.Context, storage.Principal, string) (contractsv1.ContextPacket, error) {
	return contractsv1.ContextPacket{Repository: contractsv1.RepositoryRef{Slug: "owner/repo"}}, nil
}

func (matchingPacketStore) PurgeExpired(context.Context, time.Time, int) (int, error) {
	return 0, nil
}

func withPacketStore(options ServiceOptions) ServiceOptions {
	options.PacketStore = matchingPacketStore{}
	return options
}
