package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// TestSanitizeDatabaseError_realCheckViolationCarriesSafeErrorClass is
// CHAOS-6278's real-Postgres proof: a genuinely planted CHECK constraint
// violation on acr.oauth_clients (an invalid client_id, violating
// migration 0040's own `client_id ~ '^acrc_[0-9a-f]{32}$'` constraint --
// exactly the shape of real defect CHAOS-6233 cost hours to root-cause
// through Postgres's OWN log instead of acr-api's) must classify, through
// sanitizeDatabaseError, as storage.ErrUnavailable carrying a
// *storage.DependencyErrorClass naming the real SQLSTATE, class, constraint
// and table -- never a bare, unclassified storage.ErrUnavailable.
//
// RED on baseline: before this change, sanitizeDatabaseError collapsed
// every non-unique-violation Postgres failure to a bare storage.ErrUnavailable
// with nothing attached, so errors.As below found nothing and this test
// failed on the require.True line.
func TestSanitizeDatabaseError_realCheckViolationCarriesSafeErrorClass(t *testing.T) {
	// Given a real Postgres database with the acr schema migrated, and a
	// genuine INSERT that violates oauth_clients' own client_id CHECK
	// constraint -- not a hand-authored error, the actual driver error a
	// real constraint violation produces.
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)

	// When
	_, err := db.ExecContext(ctx, `
INSERT INTO acr.oauth_clients (client_id, client_name, redirect_uris, created_at)
VALUES ('not-a-valid-client-id', 'test client', '[]'::jsonb, now())`)
	require.Error(t, err, "the planted client_id must actually violate the real CHECK constraint")
	sanitized := sanitizeDatabaseError(err)

	// Then: still classifies as ErrUnavailable (a check violation is not a
	// conflict -- only 23505 unique_violation maps to storage.ErrConflict)...
	require.ErrorIs(t, sanitized, storage.ErrUnavailable)
	require.False(t, errors.Is(sanitized, storage.ErrConflict), "a check violation must never be misclassified as a conflict")

	// ...and the safe classification survives, extractable by any caller
	// (acr-api's oauth dependency-failure log, in particular) via errors.As.
	var class *storage.DependencyErrorClass
	require.True(t, errors.As(sanitized, &class), "the real Postgres check violation must carry a storage.DependencyErrorClass")
	require.Equal(t, "23514", class.SQLState, "23514 is Postgres's own SQLSTATE for check_violation")
	require.Equal(t, "check_violation", class.Class)
	require.Equal(t, "oauth_clients_client_id_check", class.Constraint)
	require.Equal(t, "oauth_clients", class.Table)
}

// TestSanitizeDatabaseError_uniqueViolationStillConflictsWithNoClassLeak
// proves the pre-existing 23505 -> storage.ErrConflict path is unchanged by
// this class-attaching change (idempotent client_id retry semantics several
// callers rely on via errors.Is), while ALSO now carrying the same safe
// class information as any other classified failure.
func TestSanitizeDatabaseError_uniqueViolationStillConflictsWithNoClassLeak(t *testing.T) {
	// Given a client already registered once.
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	insert := `
INSERT INTO acr.oauth_clients (client_id, client_name, redirect_uris, created_at)
VALUES ('acrc_00000000000000000000000000000000', 'test client', '[]'::jsonb, now())`
	_, err := db.ExecContext(ctx, insert)
	require.NoError(t, err)

	// When the same client_id is inserted again.
	_, err = db.ExecContext(ctx, insert)
	require.Error(t, err)
	sanitized := sanitizeDatabaseError(err)

	// Then it is still ErrConflict (unchanged behavior)...
	require.ErrorIs(t, sanitized, storage.ErrConflict)
	// ...and now also carries the safe class, same as the ErrUnavailable path.
	var class *storage.DependencyErrorClass
	require.True(t, errors.As(sanitized, &class))
	require.Equal(t, "23505", class.SQLState)
	require.Equal(t, "unique_violation", class.Class)
}
