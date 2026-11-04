package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// CHAOS-12825: a presented-token revoke racing a rotation of the same
// credential on real PostgreSQL row locks ends with the source revoked, one
// credential_revoked audit row for it, and the revoke answered ok.
func TestCredentialStore_presentedTokenRevokeRacesRotation(t *testing.T) {
	ctx := context.Background()
	f := newProvenanceFixture(t, ctx)
	const client = "acrc_0123456789abcdef0123456789abcdef"
	// The database judges expiry by its own clock, so this test issues and
	// rotates with the real clock rather than the fixture's frozen one.
	service, err := auth.NewService(f.creds, auth.ServiceOptions{})
	require.NoError(t, err)

	created, err := f.devices.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("presented-revoke-device"),
		UserCodeHash:   storage.HashUserCode("PRSNT1"),
	})
	require.NoError(t, err)
	grant := postgresDeviceAuthorizationGrant()
	_, err = f.devices.Approve(ctx, created.UserCodeHash, grant)
	require.NoError(t, err)
	prepared, err := service.PrepareCreate(auth.CreateCredentialRequest{
		OrgID: grant.OrgID, Name: "device login", RepositoryScopes: grant.RepositoryScopes,
		Scopes: grant.Scopes, CreatedBy: grant.ApprovingSubject,
		ExpiresAt: postgresPointerTime(time.Now().Add(30 * 24 * time.Hour)),
		Resource:  "https://mcp.example.test/mcp", ClientID: client,
	})
	require.NoError(t, err)
	redeemed, err := f.devices.Redeem(ctx, created.DeviceCodeHash, prepared.StorageInput())
	require.NoError(t, err)
	issued, err := prepared.Complete(redeemed)
	require.NoError(t, err)

	var wg sync.WaitGroup
	var revoked auth.PresentedRevocation
	var revokeErr, rotateErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		revoked, revokeErr = service.RevokePresentedAccessToken(ctx, issued.Token, client)
	}()
	go func() {
		defer wg.Done()
		_, rotateErr = service.Rotate(ctx, auth.RotateCredentialRequest{
			OrgID: redeemed.OrgID, CredentialID: redeemed.CredentialID, CreatedBy: redeemed.CredentialID, Overlap: time.Minute,
		})
	}()
	wg.Wait()

	require.NoError(t, revokeErr)
	require.Equal(t, oauthvocab.OutcomeOK, revoked.Outcome)
	_ = rotateErr // the rotation may lose the race and find the source revoked; either order is valid
	source, err := f.creds.GetByID(ctx, redeemed.OrgID, redeemed.CredentialID)
	require.NoError(t, err)
	require.NotNil(t, source.RevokedAt, "the source credential must end revoked whichever side won")
	var audit int
	require.NoError(t, f.db.QueryRowContext(ctx, `
SELECT count(*) FROM acr.audit_events WHERE action = 'credential_revoked' AND resource_id = $1`, redeemed.CredentialID).Scan(&audit))
	require.Equal(t, 1, audit)
}
