package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestEpisodeStoreScopesDuplicatesRedactsAndPurges(t *testing.T) {
	store := NewEpisodeStore()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	create := testEpisodeCreate()
	expiresAt := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	created, duplicate, err := store.CreateIdempotent(context.Background(), principal, create, &expiresAt)
	if err != nil || duplicate || created.RedactionState != "active" {
		t.Fatalf("create = (%#v, %t, %v)", created, duplicate, err)
	}
	created.Artifacts.FilesTouched[0] = "mutated"
	stored, err := store.GetByClientEpisodeID(context.Background(), principal, create.ClientEpisodeID)
	if err != nil || stored.Artifacts.FilesTouched[0] != "main.go" {
		t.Fatalf("stored projection = (%#v, %v)", stored, err)
	}
	retry, duplicate, err := store.CreateIdempotent(context.Background(), principal, create, &expiresAt)
	if err != nil || !duplicate || retry.EpisodeID != stored.EpisodeID {
		t.Fatalf("retry = (%#v, %t, %v)", retry, duplicate, err)
	}
	conflict := create
	conflict.Summary = "different body"
	if _, _, err := store.CreateIdempotent(context.Background(), principal, conflict, &expiresAt); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("conflicting retry error = %v", err)
	}
	duplicateID := create
	duplicateID.ClientEpisodeID = "episode_02"
	if _, _, err := store.CreateIdempotent(context.Background(), principal, duplicateID, &expiresAt); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("idempotency-key collision error = %v", err)
	}
	duplicateClient := create
	duplicateClient.IdempotencyKey = "idempotency_02"
	if _, _, err := store.CreateIdempotent(context.Background(), principal, duplicateClient, &expiresAt); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("client-episode collision error = %v", err)
	}
	if _, err := store.GetByClientEpisodeID(context.Background(), storage.Principal{OrgID: "org_2", RepositoryScopes: []string{"owner/repo"}}, create.ClientEpisodeID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-org read error = %v", err)
	}
	if _, err := store.GetByClientEpisodeID(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"elsewhere/repo"}}, create.ClientEpisodeID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-repository read error = %v", err)
	}
	redacted, err := store.Redact(context.Background(), principal, stored.EpisodeID, "request")
	if err != nil || redacted.Goal != redactedEpisodeText || redacted.Transcript.Mode != "none" || len(redacted.Artifacts.TestsRun) != 0 {
		t.Fatalf("redaction = (%#v, %v)", redacted, err)
	}
	purged, err := store.PurgeExpiredForPrincipal(context.Background(), principal, expiresAt, 10)
	if err != nil || purged != 1 {
		t.Fatalf("purge = (%d, %v)", purged, err)
	}
	if _, err := store.GetByClientEpisodeID(context.Background(), principal, create.ClientEpisodeID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("purged read error = %v", err)
	}
}

func TestEpisodeStoreNoPersistRetainsOnlyIdempotencyTombstone(t *testing.T) {
	store := NewEpisodeStore()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	create := testEpisodeCreate()
	create.RetentionClass = "no_persist"
	created, duplicate, err := store.CreateIdempotent(context.Background(), principal, create, nil)
	if err != nil || duplicate || created.EpisodeID != "" {
		t.Fatalf("no_persist create = (%#v, %t, %v)", created, duplicate, err)
	}
	if _, duplicate, err = store.CreateIdempotent(context.Background(), principal, create, nil); err != nil || !duplicate {
		t.Fatalf("no_persist retry = (%t, %v)", duplicate, err)
	}
	if _, err := store.GetByClientEpisodeID(context.Background(), principal, create.ClientEpisodeID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("no_persist read error = %v", err)
	}
}

func TestEpisodeStorePurgeRequiresRepositoryScope(t *testing.T) {
	store := NewEpisodeStore()
	expiresAt := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	owner := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	other := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/other"}}
	first := testEpisodeCreate()
	if _, _, err := store.CreateIdempotent(context.Background(), owner, first, &expiresAt); err != nil {
		t.Fatal(err)
	}
	second := testEpisodeCreate()
	second.ClientEpisodeID, second.IdempotencyKey, second.Repository.Slug = "episode_02", "idempotency_02", "owner/other"
	if _, _, err := store.CreateIdempotent(context.Background(), other, second, &expiresAt); err != nil {
		t.Fatal(err)
	}
	purged, err := store.PurgeExpiredForPrincipal(context.Background(), owner, expiresAt, 10)
	if err != nil || purged != 1 {
		t.Fatalf("scoped purge = (%d, %v)", purged, err)
	}
	if _, err := store.GetByClientEpisodeID(context.Background(), other, second.ClientEpisodeID); err != nil {
		t.Fatalf("foreign repository episode was purged: %v", err)
	}
}

func TestEpisodeStoreScopedPurgeRejectsEmptyRepositoryScope(t *testing.T) {
	store := NewEpisodeStore()
	expiresAt := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	create := testEpisodeCreate()
	if _, _, err := store.CreateIdempotent(context.Background(), principal, create, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PurgeExpiredForPrincipal(context.Background(), storage.Principal{OrgID: "org_1"}, expiresAt, 1); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("empty-scope purge error = %v", err)
	}
	if _, err := store.GetByClientEpisodeID(context.Background(), principal, create.ClientEpisodeID); err != nil {
		t.Fatalf("empty-scope purge mutated episode: %v", err)
	}
}

func TestEpisodeStoreScopedPurgeRejectsEmptyOrganization(t *testing.T) {
	store := NewEpisodeStore()
	expiresAt := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	create := testEpisodeCreate()
	if _, _, err := store.CreateIdempotent(context.Background(), principal, create, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PurgeExpiredForPrincipal(context.Background(), storage.Principal{RepositoryScopes: principal.RepositoryScopes}, expiresAt, 1); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("empty-org purge error = %v", err)
	}
	if _, err := store.GetByClientEpisodeID(context.Background(), principal, create.ClientEpisodeID); err != nil {
		t.Fatalf("empty-org purge mutated episode: %v", err)
	}
}

func TestEpisodeStoreExpiredEpisodesCannotBeReadOrRedacted(t *testing.T) {
	store := NewEpisodeStore()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	create := testEpisodeCreate()
	expiresAt := time.Now().Add(-time.Minute)
	created, _, err := store.CreateIdempotent(context.Background(), principal, create, &expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetByClientEpisodeID(context.Background(), principal, create.ClientEpisodeID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expired get error = %v", err)
	}
	if _, err := store.Redact(context.Background(), principal, created.EpisodeID, "request"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expired redact error = %v", err)
	}
}

func TestEpisodeStoreRawPurgeMethodsFailClosed(t *testing.T) {
	store := NewEpisodeStore()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	create := testEpisodeCreate()
	expiresAt := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	if _, _, err := store.CreateIdempotent(context.Background(), principal, create, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PurgeExpired(context.Background(), expiresAt, 1); err == nil {
		t.Fatal("global purge was accepted")
	}
	if _, err := store.PurgeExpiredForOrg(context.Background(), principal, expiresAt, 1); err == nil {
		t.Fatal("org-only purge was accepted")
	}
	if _, err := store.GetByClientEpisodeID(context.Background(), principal, create.ClientEpisodeID); err != nil {
		t.Fatalf("raw purge bypass mutated episode: %v", err)
	}
}

func TestEpisodeStoreCreateRequiresRepositoryScope(t *testing.T) {
	store := NewEpisodeStore()
	if _, _, err := store.CreateIdempotent(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/other"}}, testEpisodeCreate(), nil); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("unauthorized direct create error = %v", err)
	}
}

func testEpisodeCreate() contractsv1.AgentEpisodeCreate {
	return contractsv1.AgentEpisodeCreate{
		SchemaVersion: contractsv1.AgentEpisodeCreateSchema, ClientEpisodeID: "episode_01", IdempotencyKey: "idempotency_01", ContextPacketID: "packet_01",
		Goal: "Persist agent work", Summary: "Saved bounded result", Repository: contractsv1.RepositoryRef{Slug: "owner/repo", RepoID: "repo_01"},
		Client: contractsv1.EpisodeClient{Name: "test", Version: "1", SidecarVersion: "1"}, StartedAt: time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC),
		EndedAt: time.Date(2026, 7, 10, 10, 1, 0, 0, time.UTC), Outcome: "succeeded", RetentionClass: "default_90d",
		Artifacts:  contractsv1.EpisodeArtifacts{FilesTouched: []string{"main.go"}, ArtifactURIs: []string{"https://example.test/pr/1"}, TestsRun: []string{"go test ./..."}},
		Transcript: contractsv1.TranscriptRef{Mode: "none"},
	}
}
