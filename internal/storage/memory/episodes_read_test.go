package memory

import (
	"context"
	"errors"
	"fmt"
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

// TestList_defaultsNonPositiveLimit is the regression test for review
// finding M6's default-limit-trigger mutant: a non-positive caller limit
// must be replaced with defaultEpisodeListLimit, not passed through
// unbounded or left at zero (which would return no rows at all).
func TestList_defaultsNonPositiveLimit(t *testing.T) {
	store := NewEpisodeStore()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	expiresAt := time.Now().Add(time.Hour)
	for i := range defaultEpisodeListLimit + 5 {
		create := testEpisodeCreate()
		create.ClientEpisodeID = fmt.Sprintf("episode_default_%02d", i)
		create.IdempotencyKey = fmt.Sprintf("idempotency_default_%02d", i)
		if _, _, err := store.CreateIdempotent(context.Background(), principal, create, &expiresAt); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	for _, limit := range []int{0, -1, -100} {
		results, err := store.List(context.Background(), principal, "", limit)
		if err != nil {
			t.Fatalf("list limit=%d: %v", limit, err)
		}
		if len(results) != defaultEpisodeListLimit {
			t.Fatalf("list limit=%d returned %d results, want the default limit %d", limit, len(results), defaultEpisodeListLimit)
		}
	}
}

// TestList_clampsOverLargeLimitToMax is the regression test for review
// finding M6's max-limit-clamp mutant: a caller-supplied limit above
// maxEpisodeListLimit must be clamped, never trusted unbounded.
func TestList_clampsOverLargeLimitToMax(t *testing.T) {
	store := NewEpisodeStore()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	expiresAt := time.Now().Add(time.Hour)
	for i := range maxEpisodeListLimit + 5 {
		create := testEpisodeCreate()
		create.ClientEpisodeID = fmt.Sprintf("episode_max_%03d", i)
		create.IdempotencyKey = fmt.Sprintf("idempotency_max_%03d", i)
		if _, _, err := store.CreateIdempotent(context.Background(), principal, create, &expiresAt); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	results, err := store.List(context.Background(), principal, "", 10_000)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results) != maxEpisodeListLimit {
		t.Fatalf("list with an over-large limit returned %d results, want clamped to %d", len(results), maxEpisodeListLimit)
	}
}

// TestList_truncatesToLimitWhenMoreMatchesExist is the regression test for
// review finding M6's truncation mutant: when more matching episodes exist
// than the (already-clamped) limit, the result must actually be cut down to
// that limit, not returned in full.
func TestList_truncatesToLimitWhenMoreMatchesExist(t *testing.T) {
	store := NewEpisodeStore()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	expiresAt := time.Now().Add(time.Hour)
	const seeded = 7
	const limit = 3
	for i := range seeded {
		create := testEpisodeCreate()
		create.ClientEpisodeID = fmt.Sprintf("episode_trunc_%02d", i)
		create.IdempotencyKey = fmt.Sprintf("idempotency_trunc_%02d", i)
		if _, _, err := store.CreateIdempotent(context.Background(), principal, create, &expiresAt); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	results, err := store.List(context.Background(), principal, "", limit)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results) != limit {
		t.Fatalf("list returned %d results for %d matching episodes and limit=%d, want exactly %d", len(results), seeded, limit, limit)
	}
}

// TestGetByEpisodeID_rejectsPurgedTombstoneAsNotFound and
// TestList_excludesPurgedTombstone are the regression tests for review
// finding M6's purged_tombstone-guard mutants. Without the guard in
// GetByEpisodeID, a purged episode would return (AgentEpisode{}, nil) --
// 200 OK with an empty body -- instead of ErrNotFound: an existence oracle,
// since it is distinguishable from a truly-unknown ID (which still 404s).
// presentation() independently zeroes a purged_tombstone record's fields,
// but that does not change the *error*, which is what actually determines
// the HTTP status a caller observes (episode_routes.go), so this asserts
// the error, not just the body shape.
func TestGetByEpisodeID_rejectsPurgedTombstoneAsNotFound(t *testing.T) {
	store, created, principal := seedPurgedEpisode(t)
	if _, err := store.GetByEpisodeID(context.Background(), principal, created.EpisodeID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("purged episode read error = %v, want ErrNotFound (not a 200-with-empty-body existence oracle)", err)
	}
}

func TestList_excludesPurgedTombstone(t *testing.T) {
	store, created, principal := seedPurgedEpisode(t)
	results, err := store.List(context.Background(), principal, "", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Asserted by count, not just by matching created.EpisodeID: presentation()
	// independently blanks a purged_tombstone record's EpisodeID to "", so a
	// row-count check is what actually catches the guard being removed from
	// List's own filter -- an ID-based check alone would miss a leaked,
	// ID-blanked row sitting in the results slice.
	if len(results) != 0 {
		t.Fatalf("list = %#v, want zero results (only a purged_tombstone episode exists for this principal, created=%s)", results, created.EpisodeID)
	}
}

// seedPurgedEpisode creates one active episode, then purges it through the
// real retention path (PurgeExpiredForPrincipal), so the resulting
// purged_tombstone record is exactly the shape a real retention purge
// produces -- not a hand-authored fixture.
func seedPurgedEpisode(t *testing.T) (*EpisodeStore, contractsv1.AgentEpisode, storage.Principal) {
	t.Helper()
	store := NewEpisodeStore()
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"owner/repo"}}
	pastExpiry := time.Now().Add(-time.Hour)
	created, _, err := store.CreateIdempotent(context.Background(), principal, testEpisodeCreate(), &pastExpiry)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	purged, err := store.PurgeExpiredForPrincipal(context.Background(), principal, time.Now(), 10)
	if err != nil || purged != 1 {
		t.Fatalf("purge = (%d, %v), want (1, nil)", purged, err)
	}
	return store, created, principal
}
