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
	store := NewEpisodeStore(nil)
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	create := testEpisodeCreate()
	expiresAt := time.Now().Add(time.Hour)
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
	store := NewEpisodeStore(nil)
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
	store := NewEpisodeStore(nil)
	expiresAt := time.Now().Add(time.Hour)
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
	store := NewEpisodeStore(nil)
	expiresAt := time.Now().Add(time.Hour)
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
	store := NewEpisodeStore(nil)
	expiresAt := time.Now().Add(time.Hour)
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
	store := NewEpisodeStore(nil)
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

// TestEpisodeStoreExpiryFollowsInjectedClockRegardlessOfWallClock guards
// against a regression where GetByClientEpisodeID and Redact computed expiry
// against the real time.Now() instead of the store's injected clock. That
// bug was invisible while the real wall clock happened to sit before a
// fixture's expiry date and only surfaced once real time passed it -- so the
// clock here is pinned far in the past AND far in the future relative to the
// real wall clock to prove expiry now tracks the injected clock alone.
func TestEpisodeStoreExpiryFollowsInjectedClockRegardlessOfWallClock(t *testing.T) {
	tests := []struct {
		name  string
		clock time.Time
	}{
		{name: "injected clock far in the past relative to the real wall clock", clock: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
		{name: "injected clock far in the future relative to the real wall clock", clock: time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewEpisodeStore(func() time.Time { return test.clock })
			principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
			create := testEpisodeCreate()
			expiresAt := test.clock.Add(time.Hour)
			created, _, err := store.CreateIdempotent(context.Background(), principal, create, &expiresAt)
			if err != nil {
				t.Fatal(err)
			}

			// Before expiry by the injected clock, the episode must be
			// readable regardless of what the real wall clock says.
			if _, err := store.GetByClientEpisodeID(context.Background(), principal, create.ClientEpisodeID); err != nil {
				t.Fatalf("episode unreadable before its injected-clock expiry: %v", err)
			}

			// Advance only the injected clock past expiry -- the real wall
			// clock is untouched. The episode must now read and redact as
			// expired purely because of the injected clock.
			test.clock = expiresAt.Add(time.Minute)
			if _, err := store.GetByClientEpisodeID(context.Background(), principal, create.ClientEpisodeID); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("expired-by-injected-clock episode still readable: %v", err)
			}
			if _, err := store.Redact(context.Background(), principal, created.EpisodeID, "request"); !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("expired-by-injected-clock episode still redactable: %v", err)
			}
		})
	}
}

func TestEpisodeStoreRawPurgeMethodsFailClosed(t *testing.T) {
	store := NewEpisodeStore(nil)
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	create := testEpisodeCreate()
	expiresAt := time.Now().Add(time.Hour)
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
	store := NewEpisodeStore(nil)
	if _, _, err := store.CreateIdempotent(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/other"}}, testEpisodeCreate(), nil); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("unauthorized direct create error = %v", err)
	}
}

func TestEpisodeStoreCreateIdempotentRejectsCanceledContextBeforeMutation(t *testing.T) {
	// Given
	store := NewEpisodeStore(nil)
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// When
	_, _, err := store.CreateIdempotent(ctx, principal, testEpisodeCreate(), nil)

	// Then
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled create error = %v", err)
	}
	if _, getErr := store.GetByClientEpisodeID(context.Background(), principal, "episode_01"); !errors.Is(getErr, storage.ErrNotFound) {
		t.Fatalf("canceled create persisted episode: %v", getErr)
	}
}

func TestEpisodeStoreIdempotencyIsScopedToRepository(t *testing.T) {
	// Given
	store := NewEpisodeStore(nil)
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo", "owner/other"}}
	create := testEpisodeCreate()
	if _, _, err := store.CreateIdempotent(context.Background(), principal, create, nil); err != nil {
		t.Fatal(err)
	}
	other := create
	other.Repository.Slug = "owner/other"

	// When
	preflight, preflightErr := store.PreflightIdempotency(context.Background(), principal, other)
	created, duplicate, createErr := store.CreateIdempotent(context.Background(), principal, other, nil)

	// Then
	if preflightErr != nil || preflight != storage.EpisodePreflightMiss || createErr != nil || duplicate || created.EpisodeID == "" {
		t.Fatalf("cross-repository isolation = (%v, %v, %#v, %t, %v)", preflight, preflightErr, created, duplicate, createErr)
	}
}

func TestEpisodeStorePreflightClassifiesMissIdenticalAndConflictWithoutTombstoneData(t *testing.T) {
	// Given
	store := NewEpisodeStore(nil)
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	create := testEpisodeCreate()
	if result, err := store.PreflightIdempotency(context.Background(), principal, create); err != nil || result != storage.EpisodePreflightMiss {
		t.Fatalf("miss preflight = (%v, %v)", result, err)
	}
	noPersist := create
	noPersist.RetentionClass = "no_persist"
	if _, _, err := store.CreateIdempotent(context.Background(), principal, noPersist, nil); err != nil {
		t.Fatal(err)
	}

	// When
	identical, identicalErr := store.PreflightIdempotency(context.Background(), principal, noPersist)
	conflicting := noPersist
	conflicting.Summary = "different bounded summary"
	conflict, conflictErr := store.PreflightIdempotency(context.Background(), principal, conflicting)

	// Then
	if identicalErr != nil || identical != storage.EpisodePreflightIdentical {
		t.Fatalf("identical preflight = (%v, %v)", identical, identicalErr)
	}
	if conflictErr != nil || conflict != storage.EpisodePreflightConflict {
		t.Fatalf("conflict preflight = (%v, %v)", conflict, conflictErr)
	}
}

func TestEpisodeStoreAllowsSameKeysInSiblingRepositories(t *testing.T) {
	store := NewEpisodeStore(nil)
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo_a", "owner/repo_b"}}

	// Create in repo_a
	create_a := testEpisodeCreate()
	create_a.Repository.Slug = "owner/repo_a"
	if _, _, err := store.CreateIdempotent(context.Background(), principal, create_a, nil); err != nil {
		t.Fatal(err)
	}

	// When: create in repo_b with the same client and idempotency keys.
	create_b := create_a
	create_b.Repository.Slug = "owner/repo_b"
	created, duplicate, err := store.CreateIdempotent(context.Background(), principal, create_b, nil)
	if err != nil || duplicate || created.EpisodeID == "" {
		t.Fatalf("sibling repository create = (%#v, %t, %v)", created, duplicate, err)
	}
}

func TestEpisodeStoreListSinceOrdersPaginatesAndIsolatesByOrganization(t *testing.T) {
	clock := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	store := NewEpisodeStore(func() time.Time { return clock })
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	other := storage.Principal{OrgID: "org_2", RepositoryScopes: []string{"owner/repo"}}

	create := testEpisodeCreate()
	create.ClientEpisodeID, create.IdempotencyKey = "ep-1", "idem-1"
	if _, _, err := store.CreateIdempotent(context.Background(), principal, create, nil); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	create.ClientEpisodeID, create.IdempotencyKey = "ep-2", "idem-2"
	if _, _, err := store.CreateIdempotent(context.Background(), principal, create, nil); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	create.ClientEpisodeID, create.IdempotencyKey = "ep-3", "idem-3"
	if _, _, err := store.CreateIdempotent(context.Background(), other, create, nil); err != nil {
		t.Fatal(err) // a different organization; must never appear in org_1's list
	}

	all, err := store.ListSince(context.Background(), "org_1", time.Time{}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected exactly org_1's 2 episodes, got %d: %+v", len(all), all)
	}
	if !all[0].CreatedAt.Before(all[1].CreatedAt) {
		t.Fatalf("expected ascending (created_at, episode_id) order, got %+v", all)
	}

	// Pagination: page 1 of size 1, then resume from its cursor.
	page1, err := store.ListSince(context.Background(), "org_1", time.Time{}, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 1 || page1[0].EpisodeID != all[0].EpisodeID {
		t.Fatalf("page1 = %+v, want first episode only", page1)
	}
	page2, err := store.ListSince(context.Background(), "org_1", page1[0].CreatedAt, page1[0].EpisodeID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || page2[0].EpisodeID != all[1].EpisodeID {
		t.Fatalf("page2 = %+v, want the second episode only", page2)
	}
}

func TestEpisodeStoreListSinceReportsRedactionAndPurgeStateWithoutLeakingContent(t *testing.T) {
	clock := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	store := NewEpisodeStore(func() time.Time { return clock })
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}

	active := testEpisodeCreate()
	active.ClientEpisodeID, active.IdempotencyKey = "ep-active", "idem-active"
	activeEpisode, _, err := store.CreateIdempotent(context.Background(), principal, active, nil)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	redacted := testEpisodeCreate()
	redacted.ClientEpisodeID, redacted.IdempotencyKey = "ep-redacted", "idem-redacted"
	redactedEpisode, _, err := store.CreateIdempotent(context.Background(), principal, redacted, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Redact(context.Background(), principal, redactedEpisode.EpisodeID, "request"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	purged := testEpisodeCreate()
	purged.ClientEpisodeID, purged.IdempotencyKey = "ep-purged", "idem-purged"
	purged.RetentionClass = "no_persist"
	if _, _, err := store.CreateIdempotent(context.Background(), principal, purged, nil); err != nil {
		t.Fatal(err)
	}

	rows, err := store.ListSince(context.Background(), "org_1", time.Time{}, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected all 3 episodes (including redacted and purged, as tombstone signals), got %d: %+v", len(rows), rows)
	}
	byID := map[string]storage.EpisodeProjectionRecord{}
	for _, row := range rows {
		byID[row.EpisodeID] = row
	}
	if got := byID[activeEpisode.EpisodeID]; got.RedactionState != "active" || got.Goal == "" {
		t.Fatalf("active episode row = %+v", got)
	}
	if got := byID[redactedEpisode.EpisodeID]; got.RedactionState != "redacted" || got.Goal != "" {
		t.Fatalf("redacted episode row must report its state without content: %+v", got)
	}
	foundPurged := false
	for _, row := range rows {
		if row.RedactionState == "purged_tombstone" {
			foundPurged = true
			if row.Goal != "" || row.Summary != "" {
				t.Fatalf("purged episode row must not carry content: %+v", row)
			}
		}
	}
	if !foundPurged {
		t.Fatalf("expected a purged_tombstone row: %+v", rows)
	}
}

func TestEpisodeStoreListSinceRejectsEmptyOrganization(t *testing.T) {
	store := NewEpisodeStore(nil)
	if _, err := store.ListSince(context.Background(), "", time.Time{}, "", 10); err == nil {
		t.Fatal("expected an error for an empty organization")
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
