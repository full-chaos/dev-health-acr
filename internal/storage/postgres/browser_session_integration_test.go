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

// CHAOS-12123 in Postgres: the approval stores the web session (migration
// 0061), and a browser-class redemption writes it with the family from the
// locked row, refusing a forged or missing session.

func (f provenanceFixture) approveWithSession(t *testing.T, ctx context.Context, suffix, sessionID string) (storage.DeviceAuthorization, storage.DeviceAuthorizationGrant) {
	t.Helper()
	created, err := f.devices.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("browser-device-" + suffix),
		UserCodeHash:   storage.HashUserCode("BRWS" + suffix),
	})
	require.NoError(t, err)
	grant := postgresDeviceAuthorizationGrant()
	grant.ApprovingSessionID = sessionID
	approved, err := f.devices.Approve(ctx, created.UserCodeHash, grant)
	require.NoError(t, err)
	return approved, grant
}

func (f provenanceFixture) browserInput(t *testing.T, grant storage.DeviceAuthorizationGrant, sessionID string) storage.CredentialCreateInput {
	t.Helper()
	family := f.now.Add(auth.BrowserFamilyLifetime)
	prepared, err := f.service.PrepareCreate(auth.CreateCredentialRequest{
		OrgID: grant.OrgID, Name: "browser login", RepositoryScopes: grant.RepositoryScopes,
		Scopes: grant.Scopes, CreatedBy: grant.ApprovingSubject,
		ExpiresAt: postgresPointerTime(f.now.Add(auth.BrowserCredentialLifetime)),
		Resource:  "https://acr.example.test", ClientID: auth.DevBrowserClientID,
		ClientClass: storage.CredentialClientClassBrowser, FamilyID: "fam_pg_1", FamilyExpiresAt: &family,
	})
	require.NoError(t, err)
	input := prepared.StorageInput()
	input.SessionID = sessionID
	return input
}

func TestBrowserSession_postgresWritesTheSessionAndFamilyFromTheRecord(t *testing.T) {
	ctx := context.Background()
	fixture := newProvenanceFixture(t, ctx)
	approved, grant := fixture.approveWithSession(t, ctx, "1", "sid_pg_1")
	require.Equal(t, "sid_pg_1", approved.ApprovingSessionID)
	var stored sql.NullString
	require.NoError(t, fixture.db.QueryRowContext(ctx, `SELECT approving_session_id FROM acr.device_authorizations WHERE device_code_hash = $1`, approved.DeviceCodeHash.String()).Scan(&stored))
	require.Equal(t, "sid_pg_1", stored.String)

	credential, err := fixture.devices.Redeem(ctx, approved.DeviceCodeHash, fixture.browserInput(t, grant, ""))

	require.NoError(t, err)
	require.Equal(t, "browser", credential.Provenance.ClientClass)
	require.Equal(t, "sid_pg_1", credential.Provenance.SessionID)
	var class, session, family, client sql.NullString
	var familyExpires sql.NullTime
	require.NoError(t, fixture.db.QueryRowContext(ctx, `
SELECT client_class, session_id, family_id, family_expires_at, client_id FROM acr.client_credentials WHERE credential_id = $1`, credential.CredentialID).
		Scan(&class, &session, &family, &familyExpires, &client))
	require.Equal(t, "browser", class.String)
	require.Equal(t, "sid_pg_1", session.String)
	require.Equal(t, "fam_pg_1", family.String)
	require.Equal(t, auth.DevBrowserClientID, client.String)
	require.True(t, familyExpires.Time.Equal(fixture.now.Add(12*time.Hour)), "family expires at %v", familyExpires.Time)
	read, err := fixture.creds.GetByID(ctx, credential.OrgID, credential.CredentialID)
	require.NoError(t, err)
	require.Equal(t, credential.Provenance.FamilyID, read.Provenance.FamilyID)
	require.Equal(t, "sid_pg_1", read.Provenance.SessionID)
}

func TestBrowserSession_postgresRefusesAForgedOrMissingSession(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct{ suffix, recorded, presented string }{
		"a caller session that differs": {"2", "sid_pg_1", "sid_pg_2"},
		"an approval with no session":   {"3", "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newProvenanceFixture(t, ctx)
			approved, grant := fixture.approveWithSession(t, ctx, tc.suffix, tc.recorded)

			_, err := fixture.devices.Redeem(ctx, approved.DeviceCodeHash, fixture.browserInput(t, grant, tc.presented))

			require.ErrorIs(t, err, storage.ErrInvalidDeviceAuthorization)
			record, getErr := fixture.devices.GetByDeviceCodeHash(ctx, approved.DeviceCodeHash)
			require.NoError(t, getErr)
			require.Equal(t, storage.DeviceAuthorizationStateApproved, record.State)
		})
	}
}

func TestBrowserSession_postgresConstraintsHold(t *testing.T) {
	ctx := context.Background()
	fixture := newProvenanceFixture(t, ctx)
	approved, grant := fixture.approveWithSession(t, ctx, "4", "sid_pg_4")
	credential, err := fixture.devices.Redeem(ctx, approved.DeviceCodeHash, fixture.browserInput(t, grant, ""))
	require.NoError(t, err)

	// The session is immutable like the rest of the provenance (0058).
	_, err = fixture.db.ExecContext(ctx, `UPDATE acr.client_credentials SET session_id = 'other' WHERE credential_id = $1`, credential.CredentialID)
	require.ErrorContains(t, err, "credential provenance is immutable")
	// A browser row names its client (0061).
	_, err = fixture.db.ExecContext(ctx, `ALTER TABLE acr.client_credentials DISABLE TRIGGER trg_client_credentials_provenance_immutable`)
	require.NoError(t, err)
	_, err = fixture.db.ExecContext(ctx, `UPDATE acr.client_credentials SET client_id = NULL WHERE credential_id = $1`, credential.CredentialID)
	require.ErrorContains(t, err, "ck_acr_client_credentials_browser_client")
	// An approving session is 1..256 characters.
	_, err = fixture.db.ExecContext(ctx, `UPDATE acr.device_authorizations SET approving_session_id = '' WHERE device_code_hash = $1`, approved.DeviceCodeHash.String())
	require.ErrorContains(t, err, "ck_acr_device_authorizations_approving_session")
}
