package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// CHAOS-12124 in Postgres: the refresh grant's rotation, grace, reuse
// revoke and family revoke, and two replicas refreshing one token at once.

type refreshFixture struct {
	provenanceFixture
	clock   *time.Time
	refresh *BrowserRefreshStore
	service *auth.BrowserRefreshService
}

func newRefreshFixture(t *testing.T, ctx context.Context, db *sql.DB) refreshFixture {
	t.Helper()
	base := newProvenanceFixtureOn(t, ctx, db)
	clock := base.now
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewBrowserRefreshStore(db, audit)
	require.NoError(t, err)
	fixture := refreshFixture{provenanceFixture: base, clock: &clock, refresh: store}
	fixture.service, err = auth.NewBrowserRefreshService(store, func() time.Time { return *fixture.clock }, nil)
	require.NoError(t, err)
	return fixture
}

// startFamily issues a browser credential (approved with a session) and
// starts its family; it returns the credential's family id and the first
// refresh token.
func (f refreshFixture) startFamily(t *testing.T, ctx context.Context, suffix string) (string, string) {
	t.Helper()
	approved, grant := f.approveWithSession(t, ctx, suffix, "sid_"+suffix)
	input := f.browserInput(t, grant, "")
	input.FamilyID = "fam_" + suffix
	credential, err := f.devices.Redeem(ctx, approved.DeviceCodeHash, input)
	require.NoError(t, err)
	refreshToken, err := f.service.Start(ctx, auth.IssuedCredential{Credential: credential})
	require.NoError(t, err)
	return credential.Provenance.FamilyID, refreshToken
}

func familyCredentials(t *testing.T, ctx context.Context, db *sql.DB, familyID string) (live, revoked int) {
	t.Helper()
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT count(*) FILTER (WHERE revoked_at IS NULL), count(*) FILTER (WHERE revoked_at IS NOT NULL)
FROM acr.client_credentials WHERE family_id = $1`, familyID).Scan(&live, &revoked))
	return live, revoked
}

func TestBrowserRefresh_postgresRotatesGraceReplaysAndRevokesOnReuse(t *testing.T) {
	ctx := context.Background()
	fixture := newRefreshFixture(t, ctx, newCredentialStoreDatabase(t, ctx))
	familyID, first := fixture.startFamily(t, ctx, "r1")

	rotated, err := fixture.service.Refresh(ctx, first, auth.DevBrowserClientID)
	require.NoError(t, err)
	require.Equal(t, storage.BrowserRefreshRotated, rotated.Decision)
	require.Equal(t, familyID, rotated.Issued.Credential.Provenance.FamilyID)
	require.Equal(t, "sid_r1", rotated.Issued.Credential.Provenance.SessionID)
	var storedHash string
	require.NoError(t, fixture.db.QueryRowContext(ctx, `SELECT token_hash FROM acr.client_credentials WHERE credential_id = $1`, rotated.Issued.Credential.CredentialID).Scan(&storedHash))
	require.Equal(t, auth.HashToken(rotated.Issued.Token), storedHash)

	*fixture.clock = fixture.clock.Add(auth.BrowserRefreshGrace)
	replay, err := fixture.service.Refresh(ctx, first, auth.DevBrowserClientID)
	require.NoError(t, err)
	require.Equal(t, storage.BrowserRefreshGraceReplay, replay.Decision)
	require.Equal(t, rotated.Issued.Token, replay.Issued.Token)
	require.Equal(t, rotated.RefreshToken, replay.RefreshToken)
	live, _ := familyCredentials(t, ctx, fixture.db, familyID)
	require.Equal(t, 2, live)

	*fixture.clock = fixture.clock.Add(time.Second)
	reuse, err := fixture.service.Refresh(ctx, first, auth.DevBrowserClientID)
	require.NoError(t, err)
	require.Equal(t, storage.BrowserRefreshReuseRevoked, reuse.Decision)
	live, revoked := familyCredentials(t, ctx, fixture.db, familyID)
	require.Equal(t, 0, live)
	require.Equal(t, 2, revoked)
	var reason sql.NullString
	require.NoError(t, fixture.db.QueryRowContext(ctx, `SELECT revoked_reason FROM acr.browser_token_families WHERE family_id = $1`, familyID).Scan(&reason))
	require.Equal(t, "reuse", reason.String)
	after, err := fixture.service.Refresh(ctx, rotated.RefreshToken, auth.DevBrowserClientID)
	require.NoError(t, err)
	require.Equal(t, storage.BrowserRefreshFamilyRevoked, after.Decision)
	// No plaintext token is stored: only hashes and the rotation seed.
	var tokens int
	require.NoError(t, fixture.db.QueryRowContext(ctx, `
SELECT count(*) FROM acr.browser_refresh_tokens WHERE token_hash IN ($1, $2)`, auth.HashRefreshToken(first), auth.HashRefreshToken(rotated.RefreshToken)).Scan(&tokens))
	require.Equal(t, 2, tokens)
}

func TestBrowserRefresh_postgresFamilyEndAndRevokes(t *testing.T) {
	ctx := context.Background()
	fixture := newRefreshFixture(t, ctx, newCredentialStoreDatabase(t, ctx))

	// The family end bounds the successor and then stops the family.
	familyID, first := fixture.startFamily(t, ctx, "e1")
	familyEnd := fixture.now.Add(auth.BrowserFamilyLifetime)
	*fixture.clock = familyEnd.Add(-3 * time.Minute)
	late, err := fixture.service.Refresh(ctx, first, auth.DevBrowserClientID)
	require.NoError(t, err)
	require.True(t, late.Issued.Credential.ExpiresAt.Equal(familyEnd), "successor expires %v, want the family end %v", late.Issued.Credential.ExpiresAt, familyEnd)
	*fixture.clock = familyEnd
	expired, err := fixture.service.Refresh(ctx, late.RefreshToken, auth.DevBrowserClientID)
	require.NoError(t, err)
	require.Equal(t, storage.BrowserRefreshFamilyExpired, expired.Decision)
	_ = familyID

	// Sign-out by refresh token, every access token long expired.
	*fixture.clock = fixture.now
	signedOut, token := fixture.startFamily(t, ctx, "s1")
	*fixture.clock = fixture.now.Add(11 * time.Minute)
	decision, err := fixture.service.RevokeFamily(ctx, token, auth.DevBrowserClientID)
	require.NoError(t, err)
	require.Equal(t, storage.BrowserFamilyRevokedNow, decision)
	live, revoked := familyCredentials(t, ctx, fixture.db, signedOut)
	require.Equal(t, 0, live)
	require.Equal(t, 1, revoked)
	other, err := fixture.service.RevokeFamily(ctx, token, "https://other.example.test/acr/client.json")
	require.NoError(t, err)
	require.Equal(t, storage.BrowserRefreshClientMismatch, other)

	// A self or operator revoke of a member revokes the family.
	*fixture.clock = fixture.now
	memberFamily, memberToken := fixture.startFamily(t, ctx, "m1")
	successor, err := fixture.service.Refresh(ctx, memberToken, auth.DevBrowserClientID)
	require.NoError(t, err)
	_, err = fixture.creds.RevokeCredential(ctx, storage.CredentialRevocationInput{
		OrgID: successor.Issued.Credential.OrgID, CredentialID: successor.Issued.Credential.CredentialID, ActorID: credentialTestActorID,
	})
	require.NoError(t, err)
	live, revoked = familyCredentials(t, ctx, fixture.db, memberFamily)
	require.Equal(t, 0, live)
	require.Equal(t, 2, revoked)
	var reason sql.NullString
	require.NoError(t, fixture.db.QueryRowContext(ctx, `SELECT revoked_reason FROM acr.browser_token_families WHERE family_id = $1`, memberFamily).Scan(&reason))
	require.Equal(t, "member_revoked", reason.String)
	refused, err := fixture.service.Refresh(ctx, successor.RefreshToken, auth.DevBrowserClientID)
	require.NoError(t, err)
	require.Equal(t, storage.BrowserRefreshFamilyRevoked, refused.Decision)
}

// Two replicas (two pools) refresh one token at the same moment: one
// rotation happens, and both answers carry the same successor.
func TestBrowserRefresh_postgresTwoPoolsRotateOnce(t *testing.T) {
	ctx := context.Background()
	server := sharedPostgresFixture(t)
	name := server.createDatabase(t, ctx)
	open := func() *sql.DB {
		db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: server.dsnFor(name)})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		return db
	}
	one := newRefreshFixture(t, ctx, open())
	two := newRefreshFixture(t, ctx, open())
	for round := range 8 {
		familyID, token := one.startFamily(t, ctx, fmt.Sprintf("c%d", round))
		var wait sync.WaitGroup
		results := make([]auth.BrowserRefresh, 2)
		errs := make([]error, 2)
		start := make(chan struct{})
		for i, fixture := range []refreshFixture{one, two} {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				results[i], errs[i] = fixture.service.Refresh(ctx, token, auth.DevBrowserClientID)
			}()
		}
		close(start)
		wait.Wait()
		require.NoError(t, errs[0])
		require.NoError(t, errs[1])
		decisions := map[storage.BrowserRefreshDecision]int{results[0].Decision: 1}
		decisions[results[1].Decision]++
		require.Equal(t, map[storage.BrowserRefreshDecision]int{storage.BrowserRefreshRotated: 1, storage.BrowserRefreshGraceReplay: 1}, decisions, "round %d", round)
		require.Equal(t, results[0].Issued.Token, results[1].Issued.Token)
		require.Equal(t, results[0].RefreshToken, results[1].RefreshToken)
		live, _ := familyCredentials(t, ctx, one.db, familyID)
		require.Equal(t, 2, live, "round %d: credentials of the family", round)
	}
}

func TestBrowserRefresh_postgresTablesRefuseBadRows(t *testing.T) {
	ctx := context.Background()
	fixture := newRefreshFixture(t, ctx, newCredentialStoreDatabase(t, ctx))
	familyID, token := fixture.startFamily(t, ctx, "k1")
	_, err := fixture.db.ExecContext(ctx, `UPDATE acr.browser_refresh_tokens SET consumed_at = now() WHERE token_hash = $1`, auth.HashRefreshToken(token))
	require.ErrorContains(t, err, "ck_acr_browser_refresh_tokens_shape")
	_, err = fixture.db.ExecContext(ctx, `UPDATE acr.browser_refresh_tokens SET consumed_at = now(), successor_hash = repeat('a', 64) WHERE token_hash = $1`, auth.HashRefreshToken(token))
	require.ErrorContains(t, err, "ck_acr_browser_refresh_tokens_shape")
	_, err = fixture.db.ExecContext(ctx, `UPDATE acr.browser_token_families SET revoked_at = now() WHERE family_id = $1`, familyID)
	require.ErrorContains(t, err, "ck_acr_browser_token_families_shape")
	_, err = fixture.db.ExecContext(ctx, `UPDATE acr.browser_token_families SET revoked_at = now(), revoked_reason = 'other' WHERE family_id = $1`, familyID)
	require.ErrorContains(t, err, "ck_acr_browser_token_families_shape")
}
