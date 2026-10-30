package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// CHAOS-12140 in Postgres: the purge of ended browser token families. Every
// family starts at fixture.now and ends 12 h later (auth.BrowserFamilyLifetime);
// the purge takes its clock and grace as arguments, so one database walks the
// timeline: two purges at now+2h, then two at the family end + grace + 1m.

type purgeFamily struct {
	id        string
	first     string // the code-exchange credential: a device authorization names it
	members   []string
	refreshes []string // refresh token hashes
	refresh   string   // the family's current (unused) refresh token
}

func (f refreshFixture) purgeFamily(t *testing.T, ctx context.Context, suffix string) purgeFamily {
	t.Helper()
	*f.clock = f.now
	familyID, refresh := f.startFamily(t, ctx, suffix)
	family := purgeFamily{id: familyID, refreshes: []string{auth.HashRefreshToken(refresh)}}
	require.NoError(t, f.db.QueryRowContext(ctx, `
SELECT d.redeemed_credential_id FROM acr.device_authorizations d
JOIN acr.client_credentials cc ON cc.credential_id = d.redeemed_credential_id
WHERE cc.family_id = $1`, familyID).Scan(&family.first))
	*f.clock = f.now.Add(5 * time.Minute)
	rotated, err := f.service.Refresh(ctx, refresh, auth.DevBrowserClientID)
	require.NoError(t, err)
	require.Equal(t, storage.BrowserRefreshRotated, rotated.Decision)
	family.members = append(family.members, rotated.Issued.Credential.CredentialID)
	family.refreshes = append(family.refreshes, auth.HashRefreshToken(rotated.RefreshToken))
	family.refresh = rotated.RefreshToken
	return family
}

func (f refreshFixture) signOut(t *testing.T, ctx context.Context, family purgeFamily, at time.Time) {
	t.Helper()
	*f.clock = at
	decision, err := f.service.RevokeFamily(ctx, family.refresh, auth.DevBrowserClientID)
	require.NoError(t, err)
	require.Equal(t, storage.BrowserFamilyRevokedNow, decision)
}

func credentialExists(t *testing.T, ctx context.Context, db *sql.DB, credentialID string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM acr.client_credentials WHERE credential_id = $1)`, credentialID).Scan(&exists))
	return exists
}

// familyRows counts the family row and its refresh rows.
func familyRows(t *testing.T, ctx context.Context, db *sql.DB, family purgeFamily) (families, refreshes int) {
	t.Helper()
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.browser_token_families WHERE family_id = $1`, family.id).Scan(&families))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.browser_refresh_tokens WHERE token_hash = ANY($1::text[])`, family.refreshes).Scan(&refreshes))
	return families, refreshes
}

func requireFamilyKept(t *testing.T, ctx context.Context, db *sql.DB, family purgeFamily, why string) {
	t.Helper()
	families, refreshes := familyRows(t, ctx, db, family)
	require.Equal(t, 1, families, "%s: family row", why)
	require.Equal(t, len(family.refreshes), refreshes, "%s: refresh rows", why)
	for _, member := range family.members {
		require.True(t, credentialExists(t, ctx, db, member), "%s: member credential %s", why, member)
	}
	require.True(t, credentialExists(t, ctx, db, family.first), "%s: first credential", why)
}

// requireFamilyPurged: the family row, every refresh row (by cascade) and every
// member credential are gone; the first credential stays while a device
// authorization names it.
func requireFamilyPurged(t *testing.T, ctx context.Context, db *sql.DB, family purgeFamily, why string) {
	t.Helper()
	families, refreshes := familyRows(t, ctx, db, family)
	require.Zero(t, families, "%s: family row", why)
	require.Zero(t, refreshes, "%s: refresh rows go with the family by cascade", why)
	for _, member := range family.members {
		require.False(t, credentialExists(t, ctx, db, member), "%s: member credential %s", why, member)
	}
	require.True(t, credentialExists(t, ctx, db, family.first), "%s: the first credential stays while its device authorization names it", why)
}

func TestBrowserFamilyPurge_postgresDeletesEndedFamiliesAndKeepsTheRest(t *testing.T) {
	ctx := context.Background()
	fixture := newRefreshFixture(t, ctx, newCredentialStoreDatabase(t, ctx))
	now := fixture.now
	const grace = time.Hour

	// Not a browser credential: an ordinary OAuth exchange. Never purged.
	other := fixture.redeemDevice(t, ctx, "p0", "https://example.com/mcp", "https://client.example.test/acr/client.json")

	signedOut := fixture.purgeFamily(t, ctx, "p1")
	fixture.signOut(t, ctx, signedOut, now.Add(20*time.Minute))

	insideGrace := fixture.purgeFamily(t, ctx, "p2")
	fixture.signOut(t, ctx, insideGrace, now.Add(90*time.Minute))

	// A live family whose member was revoked on its own (an older release does
	// this; the next refresh ends the family). A refresh reads that member, so
	// it must stay while the family has not ended.
	live := fixture.purgeFamily(t, ctx, "p3")
	_, err := fixture.db.ExecContext(ctx, `UPDATE acr.client_credentials SET revoked_at = $2 WHERE credential_id = $1`, live.members[0], now.Add(30*time.Minute))
	require.NoError(t, err)

	// A revoked family with one member planted live again: a live credential is
	// never deleted, and the family stays while that member does.
	planted := fixture.purgeFamily(t, ctx, "p4")
	fixture.signOut(t, ctx, planted, now.Add(20*time.Minute))
	_, err = fixture.db.ExecContext(ctx, `UPDATE acr.client_credentials SET revoked_at = NULL, expires_at = $2 WHERE credential_id = $1`, planted.members[0], now.Add(5*time.Hour))
	require.NoError(t, err)

	untouched := fixture.purgeFamily(t, ctx, "p5")

	// At now+2h with a 1 h grace, only the family signed out at now+20m ended
	// before the cutoff (now+1h) and has nothing live.
	early := now.Add(2 * time.Hour)
	result, err := fixture.refresh.PurgeEndedFamilies(ctx, early, grace, 100)
	require.NoError(t, err)
	require.Equal(t, BrowserFamilyPurgeResult{Credentials: 1, Families: 1}, result)
	remaining, err := fixture.refresh.CountEndedFamiliesRemaining(ctx, early, grace, 100)
	require.NoError(t, err)
	require.Equal(t, BrowserFamilyPurgeRemaining{}, remaining)
	requireFamilyPurged(t, ctx, fixture.db, signedOut, "signed out before the cutoff")
	requireFamilyKept(t, ctx, fixture.db, insideGrace, "signed out inside the grace")
	requireFamilyKept(t, ctx, fixture.db, live, "live family with a revoked member")
	requireFamilyKept(t, ctx, fixture.db, planted, "revoked family with a live member")
	requireFamilyKept(t, ctx, fixture.db, untouched, "live family")

	// A second purge at the same moment finds nothing.
	result, err = fixture.refresh.PurgeEndedFamilies(ctx, early, grace, 100)
	require.NoError(t, err)
	require.Equal(t, BrowserFamilyPurgeResult{}, result)

	// One minute past every family end + grace, every family has ended and
	// nothing in it is live: the members and the families go, in batches of
	// one, with the backlog in the remaining counts.
	late := now.Add(auth.BrowserFamilyLifetime + grace + time.Minute)
	remaining, err = fixture.refresh.CountEndedFamiliesRemaining(ctx, late, grace, 2)
	require.NoError(t, err)
	require.Equal(t, BrowserFamilyPurgeRemaining{Credentials: 3, Families: 0}, remaining, "4 members are eligible, capped at limit+1; no family is eligible while its members remain")
	total := BrowserFamilyPurgeResult{}
	for range 10 {
		result, err = fixture.refresh.PurgeEndedFamilies(ctx, late, grace, 1)
		require.NoError(t, err)
		require.LessOrEqual(t, result.Credentials, 1)
		require.LessOrEqual(t, result.Families, 1)
		total.Credentials += result.Credentials
		total.Families += result.Families
	}
	require.Equal(t, BrowserFamilyPurgeResult{Credentials: 4, Families: 4}, total)
	for name, family := range map[string]purgeFamily{"inside grace": insideGrace, "live": live, "planted": planted, "untouched": untouched} {
		requireFamilyPurged(t, ctx, fixture.db, family, name+" after its end")
	}

	// The OAuth purge deletes the device authorizations later (after
	// ACR_OAUTH_REQUEST_PURGE_GRACE); the first credentials then go by the
	// family end they carry, with no family row left.
	_, err = fixture.db.ExecContext(ctx, `DELETE FROM acr.device_authorizations`)
	require.NoError(t, err)
	result, err = fixture.refresh.PurgeEndedFamilies(ctx, late, grace, 100)
	require.NoError(t, err)
	require.Equal(t, BrowserFamilyPurgeResult{Credentials: 5, Families: 0}, result)
	for _, family := range []purgeFamily{signedOut, insideGrace, live, planted, untouched} {
		require.False(t, credentialExists(t, ctx, fixture.db, family.first), "first credential of %s", family.id)
	}
	require.True(t, credentialExists(t, ctx, fixture.db, other.CredentialID), "a credential of another class is never purged")
	remaining, err = fixture.refresh.CountEndedFamiliesRemaining(ctx, late, grace, 100)
	require.NoError(t, err)
	require.Equal(t, BrowserFamilyPurgeRemaining{}, remaining)
}

func TestBrowserFamilyPurge_postgresRefusesABadGraceAndAnEmptyBatch(t *testing.T) {
	ctx := context.Background()
	fixture := newRefreshFixture(t, ctx, newCredentialStoreDatabase(t, ctx))
	_, err := fixture.refresh.PurgeEndedFamilies(ctx, fixture.now, 0, 10)
	require.ErrorIs(t, err, storage.ErrInvalidBrowserRefresh)
	_, err = fixture.refresh.CountEndedFamiliesRemaining(ctx, fixture.now, -time.Hour, 10)
	require.ErrorIs(t, err, storage.ErrInvalidBrowserRefresh)
	result, err := fixture.refresh.PurgeEndedFamilies(ctx, fixture.now, time.Hour, 0)
	require.NoError(t, err)
	require.Equal(t, BrowserFamilyPurgeResult{}, result)
}
