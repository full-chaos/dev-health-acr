package memory

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// TestReproSelfRotationKeepsTheApprovingUser reproduces the gap the
// credential provenance change closes. A device credential is approved by
// user_1; after a self-rotation the stored successor must still name user_1
// as the user who approved it, because owner keys and per-user policy need a
// durable approving user. approvingUserOf reads the one stored field that can
// carry it.
func TestReproSelfRotationKeepsTheApprovingUser(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	backend, credentials, err := newCredentialStore(NewAuditStore(), clock)
	require.NoError(t, err)
	devices, err := NewDeviceAuthorizationStore(DeviceAuthorizationStoreOptions{Credentials: credentials, Now: clock})
	require.NoError(t, err)
	service, err := auth.NewService(credentials, auth.ServiceOptions{Now: clock, GenerateToken: auth.GenerateToken, GenerateCredentialID: auth.GenerateCredentialID})
	require.NoError(t, err)
	pending, err := devices.Create(context.Background(), storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("repro-device-code"), UserCodeHash: storage.HashUserCode("REPRO123"),
	})
	require.NoError(t, err)
	grant := storage.DeviceAuthorizationGrant{
		OrgID: "11111111-1111-1111-1111-111111111111", RepositoryScopes: []string{"full-chaos/dev-health-acr"},
		Scopes: []string{auth.ScopeContextRead}, ApprovingSubject: "user_1",
		ApprovingAuthenticationMethod: storage.AuthenticationMethodWebAssertion,
	}
	_, err = devices.Approve(context.Background(), pending.UserCodeHash, grant)
	require.NoError(t, err)
	expires := now.Add(30 * 24 * time.Hour)
	prepared, err := service.PrepareCreate(auth.CreateCredentialRequest{
		OrgID: grant.OrgID, Name: "device login", RepositoryScopes: grant.RepositoryScopes,
		Scopes: grant.Scopes, CreatedBy: grant.ApprovingSubject, ExpiresAt: &expires,
	})
	require.NoError(t, err)
	source, err := devices.Redeem(context.Background(), pending.DeviceCodeHash, prepared.StorageInput())
	require.NoError(t, err)
	require.Equal(t, "user_1", approvingUserOf(backend.byID[source.CredentialID]), "the redeemed credential names its approver")

	rotation, err := service.RotateSelf(context.Background(), storage.Principal{
		AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: source.CredentialID,
		OrgID: source.OrgID, CredentialID: source.CredentialID,
		RepositoryScopes: source.RepositoryScopes, Permissions: source.Scopes,
	})
	require.NoError(t, err)

	require.Equal(t, "user_1", approvingUserOf(backend.byID[rotation.Issued.Credential.CredentialID]),
		"the self-rotated successor must still name the user who approved the credential")
}

func approvingUserOf(record storage.CredentialRecord) string { return record.CreatedBy }
