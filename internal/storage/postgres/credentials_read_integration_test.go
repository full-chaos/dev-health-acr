package postgres

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestCredentialStore_ListAndGetByIDRoundTripJSONScopes(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{})
	require.NoError(t, err)
	request := credentialCreateRequest("round-trip")
	request.RepositoryScopes = []string{"acme/catalog", "acme/widgets"}
	request.Scopes = []string{auth.ScopeEvidenceRead, auth.ScopeContextRead}
	issued, err := service.Create(ctx, request)
	require.NoError(t, err)

	// When
	listed, err := store.List(ctx, credentialTestOrgID)

	// Then
	require.NoError(t, err)
	require.Len(t, listed, 1)
	requireCredentialRoundTrip(t, issued.Credential, listed[0])
	fetched, err := store.GetByID(ctx, credentialTestOrgID, issued.Credential.CredentialID)
	require.NoError(t, err)
	requireCredentialRoundTrip(t, issued.Credential, fetched)
}

func TestCredentialStore_DoesNotEnumerateCredentialsAcrossOrganizations(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{})
	require.NoError(t, err)
	issued, err := service.Create(ctx, credentialCreateRequest("private"))
	require.NoError(t, err)
	foreignOrgID := "33333333-3333-3333-3333-333333333333"

	// When
	listed, err := store.List(ctx, foreignOrgID)

	// Then
	require.NoError(t, err)
	require.Empty(t, listed)
	_, err = store.GetByID(ctx, foreignOrgID, issued.Credential.CredentialID)
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func requireCredentialRoundTrip(t *testing.T, want, actual contractsv1.ClientCredential) {
	t.Helper()
	require.Equal(t, want.SchemaVersion, actual.SchemaVersion)
	require.Equal(t, want.CredentialID, actual.CredentialID)
	require.Equal(t, want.Name, actual.Name)
	require.Equal(t, want.TokenPrefix, actual.TokenPrefix)
	require.Equal(t, want.OrgID, actual.OrgID)
	require.Equal(t, want.RepositoryScopes, actual.RepositoryScopes)
	require.Equal(t, want.Scopes, actual.Scopes)
	require.True(t, want.CreatedAt.Equal(actual.CreatedAt))
	require.Equal(t, want.ExpiresAt, actual.ExpiresAt)
	require.Equal(t, want.RevokedAt, actual.RevokedAt)
	require.Equal(t, want.LastUsedAt, actual.LastUsedAt)
}
