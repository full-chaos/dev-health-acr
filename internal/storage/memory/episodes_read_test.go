package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestEpisodeStore_GetByEpisodeID_authorizedRoundTripAndCrossTenantNotFound(t *testing.T) {
	store := NewEpisodeStore()
	owner := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	expiresAt := time.Now().Add(time.Hour)
	created, _, err := store.CreateIdempotent(context.Background(), owner, testEpisodeCreate(), &expiresAt)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Given the owning org and a repository-authorized scope: readable.
	got, err := store.GetByEpisodeID(context.Background(), owner, created.EpisodeID)
	if err != nil || got.EpisodeID != created.EpisodeID {
		t.Fatalf("authorized get = (%#v, %v)", got, err)
	}

	// A different org must get the exact same ErrNotFound a missing ID gets --
	// cross-tenant access is indistinguishable from absence.
	otherOrg := storage.Principal{OrgID: "org_2", RepositoryScopes: []string{"owner/repo"}}
	if _, err := store.GetByEpisodeID(context.Background(), otherOrg, created.EpisodeID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-tenant error = %v, want ErrNotFound", err)
	}
	if _, err := store.GetByEpisodeID(context.Background(), owner, "does-not-exist"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing id error = %v, want ErrNotFound", err)
	}

	// A credential authorized for the org but not this repository must also
	// see ErrNotFound, not a repository_forbidden-shaped error.
	otherRepo := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/other-repo"}}
	if _, err := store.GetByEpisodeID(context.Background(), otherRepo, created.EpisodeID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("out-of-scope repository error = %v, want ErrNotFound", err)
	}
}

func TestEpisodeStore_GetByEpisodeID_appliesRedactionAndExpiryToReads(t *testing.T) {
	store := NewEpisodeStore()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	expiresAt := time.Now().Add(time.Hour)
	created, _, err := store.CreateIdempotent(context.Background(), principal, testEpisodeCreate(), &expiresAt)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.Redact(context.Background(), principal, created.EpisodeID, "user_request"); err != nil {
		t.Fatalf("redact: %v", err)
	}

	// The read path must reflect the redaction, not the original content.
	got, err := store.GetByEpisodeID(context.Background(), principal, created.EpisodeID)
	if err != nil || got.RedactionState != "redacted" || got.Goal != redactedEpisodeText || got.Summary != redactedEpisodeText {
		t.Fatalf("redacted read = (%#v, %v)", got, err)
	}

	// An expired (retention-lapsed) episode must read as not found.
	expiredCreate := testEpisodeCreate()
	expiredCreate.ClientEpisodeID, expiredCreate.IdempotencyKey = "episode_expired", "idempotency_expired"
	past := time.Now().Add(-time.Hour)
	expired, _, err := store.CreateIdempotent(context.Background(), principal, expiredCreate, &past)
	if err != nil {
		t.Fatalf("create expiring episode: %v", err)
	}
	if _, err := store.GetByEpisodeID(context.Background(), principal, expired.EpisodeID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expired episode read error = %v, want ErrNotFound", err)
	}
}

func TestEpisodeStore_List_scopesFiltersOrdersAndExcludesDeletedOrExpired(t *testing.T) {
	store := NewEpisodeStore()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo", "owner/other-repo"}}
	expiresAt := time.Now().Add(time.Hour)

	first := testEpisodeCreate()
	first.ClientEpisodeID, first.IdempotencyKey = "episode_first", "idempotency_first"
	firstStored, _, err := store.CreateIdempotent(context.Background(), principal, first, &expiresAt)
	if err != nil {
		t.Fatalf("create first: %v", err)
	}

	// CreateIdempotent back-dates CreatedAt from expiresAt using the retention
	// class duration (CreatedAt = expiresAt - retention), so a strictly later
	// expiresAt for the second episode gives it a strictly later CreatedAt --
	// a deterministic newest-first order without reaching into store internals.
	second := testEpisodeCreate()
	second.ClientEpisodeID, second.IdempotencyKey = "episode_second", "idempotency_second"
	laterExpiresAt := expiresAt.Add(24 * time.Hour)
	secondStored, _, err := store.CreateIdempotent(context.Background(), principal, second, &laterExpiresAt)
	if err != nil {
		t.Fatalf("create second: %v", err)
	}

	otherRepo := testEpisodeCreate()
	otherRepo.ClientEpisodeID, otherRepo.IdempotencyKey = "episode_other_repo", "idempotency_other_repo"
	otherRepo.Repository = contractsv1.RepositoryRef{Slug: "owner/other-repo"}
	if _, _, err := store.CreateIdempotent(context.Background(), principal, otherRepo, &expiresAt); err != nil {
		t.Fatalf("create other-repo episode: %v", err)
	}

	otherOrgPrincipal := storage.Principal{OrgID: "org_2", RepositoryScopes: []string{"owner/repo"}}
	foreign := testEpisodeCreate()
	foreign.ClientEpisodeID, foreign.IdempotencyKey = "episode_foreign", "idempotency_foreign"
	if _, _, err := store.CreateIdempotent(context.Background(), otherOrgPrincipal, foreign, &expiresAt); err != nil {
		t.Fatalf("create foreign-org episode: %v", err)
	}

	pastExpiry := time.Now().Add(-time.Hour)
	expiring := testEpisodeCreate()
	expiring.ClientEpisodeID, expiring.IdempotencyKey = "episode_expiring", "idempotency_expiring"
	if _, _, err := store.CreateIdempotent(context.Background(), principal, expiring, &pastExpiry); err != nil {
		t.Fatalf("create expiring episode: %v", err)
	}

	// Filtered to owner/repo: only first and second, newest (second) first,
	// never the other-repo, foreign-org, or expired episodes.
	results, err := store.List(context.Background(), principal, "owner/repo", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results) != 2 || results[0].EpisodeID != secondStored.EpisodeID || results[1].EpisodeID != firstStored.EpisodeID {
		t.Fatalf("list results = %#v, want [second, first]", results)
	}

	// Unfiltered (empty repository slug): both of this org's repositories,
	// still never the foreign org's or the expired episode.
	all, err := store.List(context.Background(), principal, "", 10)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("unfiltered list = %#v, want 3 episodes", all)
	}
	for _, episode := range all {
		if episode.EpisodeID == "" {
			t.Fatalf("list leaked an empty/tombstoned episode: %#v", all)
		}
	}

	// A credential with no scope for owner/repo sees none of its episodes.
	unauthorized := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"someone-else/repo"}}
	if results, err := store.List(context.Background(), unauthorized, "", 10); err != nil || len(results) != 0 {
		t.Fatalf("unauthorized list = (%#v, %v), want (empty, nil)", results, err)
	}
}
