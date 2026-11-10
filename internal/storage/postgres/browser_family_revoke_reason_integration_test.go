package postgres

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func familyReasonRow(t *testing.T, ctx context.Context, db *sql.DB, familyID string) sql.NullString {
	t.Helper()
	var reason sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT revoked_reason FROM acr.browser_token_families WHERE family_id = $1`, familyID).Scan(&reason))
	return reason
}

func TestBrowserFamilyRevokeReason_postgresStoresEveryReasonOnTheFamilyAndItsCredentials(t *testing.T) {
	ctx := context.Background()
	fixture := newRefreshFixture(t, ctx, newCredentialStoreDatabase(t, ctx))
	for i, reason := range storage.BrowserFamilyRevokeReasonVocabulary() {
		familyID, token := fixture.startFamily(t, ctx, "r"+string(rune('a'+i)))

		out, err := fixture.refresh.RevokeBrowserFamilyByToken(ctx, auth.HashRefreshToken(token), auth.DevBrowserClientID, "oauth_refresh_grant", reason, *fixture.clock)

		require.NoError(t, err, string(reason))
		require.Equal(t, storage.BrowserFamilyRevokedNow, out.Decision, string(reason))
		require.Equal(t, string(reason), familyReasonRow(t, ctx, fixture.db, familyID).String)
		var carried int
		require.NoError(t, fixture.db.QueryRowContext(ctx, `
SELECT count(*) FROM acr.audit_events
WHERE action = 'credential_revoked' AND metadata->>'family_id' = $1 AND metadata->>'family_revoke_reason' = $2`, familyID, string(reason)).Scan(&carried))
		require.Equal(t, 1, carried, string(reason))
	}
}

func TestBrowserFamilyRevokeReason_postgresRefusesAReasonOutsideTheClosedSetAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	fixture := newRefreshFixture(t, ctx, newCredentialStoreDatabase(t, ctx))
	familyID, token := fixture.startFamily(t, ctx, "bad")
	for _, reason := range []storage.BrowserFamilyRevokeReason{"", "other", "SIGN_OUT", "sign_out ", "browser family sign_out"} {
		_, err := fixture.refresh.RevokeBrowserFamilyByToken(ctx, auth.HashRefreshToken(token), auth.DevBrowserClientID, "oauth_refresh_grant", reason, *fixture.clock)
		require.ErrorIs(t, err, storage.ErrInvalidBrowserRefresh, string(reason))
	}
	require.False(t, familyReasonRow(t, ctx, fixture.db, familyID).Valid)
	live, revoked := familyCredentials(t, ctx, fixture.db, familyID)
	require.Equal(t, 1, live)
	require.Equal(t, 0, revoked)
}

func TestBrowserFamilyRevokeReason_postgresCheckDomain(t *testing.T) {
	ctx := context.Background()
	fixture := newRefreshFixture(t, ctx, newCredentialStoreDatabase(t, ctx))
	familyID, _ := fixture.startFamily(t, ctx, "chk")
	for _, bad := range []string{"other", "", "ORG_DEPROVISIONED", "org_deprovisioned "} {
		_, err := fixture.db.ExecContext(ctx, `UPDATE acr.browser_token_families SET revoked_at = now(), revoked_reason = $2 WHERE family_id = $1`, familyID, bad)
		require.ErrorContains(t, err, "ck_acr_browser_token_families_shape", bad)
	}
	_, err := fixture.db.ExecContext(ctx, `UPDATE acr.browser_token_families SET revoked_reason = 'org_deprovisioned' WHERE family_id = $1`, familyID)
	require.ErrorContains(t, err, "ck_acr_browser_token_families_shape", "a reason without revoked_at")
	_, err = fixture.db.ExecContext(ctx, `UPDATE acr.browser_token_families SET revoked_at = now(), revoked_reason = 'org_deprovisioned' WHERE family_id = $1`, familyID)
	require.NoError(t, err)
}

func TestBrowserFamilyRevokeReason_migration0073AppliesTwiceAndKeepsRows(t *testing.T) {
	ctx := context.Background()
	fixture := newRefreshFixture(t, ctx, newCredentialStoreDatabase(t, ctx))
	familyID, token := fixture.startFamily(t, ctx, "mig")
	_, err := fixture.refresh.RevokeBrowserFamilyByToken(ctx, auth.HashRefreshToken(token), auth.DevBrowserClientID, "oauth_refresh_grant", storage.BrowserFamilyRevokeSignOut, *fixture.clock)
	require.NoError(t, err)
	raw, err := os.ReadFile("../../../migrations/postgres/0073_browser_token_family_org_deprovisioned.sql")
	require.NoError(t, err)

	for run := 0; run < 2; run++ {
		tx, err := fixture.db.BeginTx(ctx, nil)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, string(raw))
		require.NoError(t, err, "run %d", run)
		require.NoError(t, tx.Commit())
	}

	require.Equal(t, "sign_out", familyReasonRow(t, ctx, fixture.db, familyID).String)
}
