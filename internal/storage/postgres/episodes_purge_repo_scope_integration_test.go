package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestPurgeExpired_underscoreScopeDoesNotTombstoneOtherRepositories is the
// regression test for review finding NEW-3: purgeExpired uses the identical
// LIKE-wildcard-vulnerable repository-scope predicate List's EXISTS clause
// used before review finding NEW-2's fix (repo_slug LIKE
// replace(allowed.scope, '/*', '/%'), where an unescaped '_' in an exact
// scope acts as a SQL LIKE single-character wildcard) -- but on a direct
// UPDATE with no Go-side re-check afterward, unlike List's
// scanAuthorizedEpisode belt-and-braces filter. A credential scoped to
// exactly "acme/my_repo" could therefore tombstone -- destructively,
// irreversibly -- expired episodes belonging to a completely different
// repository like "acme/myxrepo" that it was never authorized for.
//
// This uses a real PostgreSQL container (not the fake sql.Driver every
// other purge test in this package uses, which can't exercise what the
// LIKE pattern actually matches against a real row set).
func TestPurgeExpired_underscoreScopeDoesNotTombstoneOtherRepositories(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}

	const orgID = "00000000-0000-0000-0000-0000000000cc"
	seeder := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/my_repo", "acme/myxrepo"}}
	pastExpiry := time.Now().Add(-time.Hour)

	// An expired episode in a DIFFERENT repository -- one character away
	// from the scope, at exactly the underscore's position -- that the
	// credential under test is never authorized for.
	foreignRepoEpisode, _, err := store.CreateIdempotent(ctx, seeder, repoScopeEpisodeCreate("acme/myxrepo", "myxrepo-expired"), &pastExpiry)
	if err != nil {
		t.Fatalf("seed acme/myxrepo expired episode: %v", err)
	}

	// The credential under test is scoped ONLY to the underscore-containing
	// exact slug -- never to acme/myxrepo, and never via an owner/* wildcard.
	testPrincipal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/my_repo"}}

	purged, err := store.PurgeExpiredForPrincipal(ctx, testPrincipal, time.Now(), 10)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if purged != 0 {
		t.Fatalf("purged = %d, want 0 -- a credential scoped to acme/my_repo must never tombstone acme/myxrepo episodes via an unescaped '_' LIKE wildcard", purged)
	}

	// The foreign-repository episode must not have been tombstoned. It is
	// already expired by construction (that's what makes it a purge
	// candidate at all), so GetByEpisodeID's own "expired collapses to
	// not-found" behavior can't distinguish "still active, just expired"
	// from "tombstoned" here -- read redaction_state directly off the row.
	if state := redactionStateFor(t, ctx, db, foreignRepoEpisode.EpisodeID); state == "purged_tombstone" {
		t.Fatalf("foreign-repository episode redaction_state = %q, want it to survive an out-of-scope purge call untouched (not purged_tombstone)", state)
	}
}

// redactionStateFor reads redaction_state directly off acr.agent_episodes,
// bypassing GetByEpisodeID's org/scope/expiry/tombstone collapse to
// not-found -- needed because an already-expired episode reads as
// not-found through the normal API whether or not it was ever tombstoned.
func redactionStateFor(t *testing.T, ctx context.Context, db *sql.DB, episodeID string) string {
	t.Helper()
	var state string
	if err := db.QueryRowContext(ctx, `SELECT redaction_state FROM acr.agent_episodes WHERE episode_id = $1`, episodeID).Scan(&state); err != nil {
		t.Fatalf("read redaction_state for %s: %v", episodeID, err)
	}
	return state
}

// TestPurgeExpired_stillPurgesTheCredentialsOwnUnderscoreRepository proves
// the fix for NEW-3 doesn't overcorrect: the credential's OWN
// underscore-containing repository must still purge normally.
func TestPurgeExpired_stillPurgesTheCredentialsOwnUnderscoreRepository(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	store, err := NewEpisodeStore(db)
	if err != nil {
		t.Fatal(err)
	}

	const orgID = "00000000-0000-0000-0000-0000000000dd"
	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/my_repo"}}
	pastExpiry := time.Now().Add(-time.Hour)

	ownEpisode, _, err := store.CreateIdempotent(ctx, principal, repoScopeEpisodeCreate("acme/my_repo", "my-repo-expired"), &pastExpiry)
	if err != nil {
		t.Fatalf("seed acme/my_repo expired episode: %v", err)
	}

	purged, err := store.PurgeExpiredForPrincipal(ctx, principal, time.Now(), 10)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged = %d, want 1 -- the credential's own expired episode in its own scoped repository must still purge", purged)
	}

	purgedEpisode, err := store.GetByEpisodeID(ctx, principal, ownEpisode.EpisodeID)
	if !errors.Is(err, storage.ErrNotFound) || purgedEpisode.EpisodeID != "" {
		t.Fatalf("read after purge = (%#v, %v), want ErrNotFound", purgedEpisode, err)
	}
}
