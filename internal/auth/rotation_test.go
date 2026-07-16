package auth

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	"github.com/stretchr/testify/require"
)

func TestCredentialServiceRotateRejectsRevokedSourceWithoutSideEffects(t *testing.T) {
	// Given
	clock := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, clock, audit)
	service := newTestService(t, store, audit, clock)
	source, err := service.Create(context.Background(), CreateCredentialRequest{
		OrgID: "org_1", Name: "revoked", RepositoryScopes: []string{"owner/repo"}, CreatedBy: "actor_1",
	})
	require.NoError(t, err)
	_, err = service.Revoke(context.Background(), "org_1", source.Credential.CredentialID, "actor_1")
	require.NoError(t, err)
	beforeAuditCount := len(audit.Events())
	before, err := store.GetByID(context.Background(), "org_1", source.Credential.CredentialID)
	require.NoError(t, err)

	// When
	issued, err := service.Rotate(context.Background(), RotateCredentialRequest{
		OrgID: "org_1", CredentialID: source.Credential.CredentialID, CreatedBy: "actor_1", Overlap: time.Minute,
	})

	// Then
	require.ErrorIs(t, err, ErrInvalidCredential)
	require.Empty(t, issued.Token)
	require.Empty(t, issued.Credential.CredentialID)
	credentials, err := store.List(context.Background(), "org_1")
	require.NoError(t, err)
	require.Len(t, credentials, 1)
	after, err := store.GetByID(context.Background(), "org_1", source.Credential.CredentialID)
	require.NoError(t, err)
	require.Equal(t, before.RevokedAt, after.RevokedAt)
	require.Equal(t, before.ExpiresAt, after.ExpiresAt)
	require.Len(t, audit.Events(), beforeAuditCount)
}

func TestCredentialServiceRotatePreservesNotFoundForCrossOrgAndUnknownSources(t *testing.T) {
	tests := []struct {
		name         string
		rotationOrg  string
		credentialID string
	}{
		{name: "cross organization", rotationOrg: "org_2"},
		{name: "unknown source", rotationOrg: "org_1", credentialID: "cred_test_unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			clock := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
			audit := memory.NewAuditStore()
			store := newMemoryCredentialStoreAt(t, clock, audit)
			service := newTestService(t, store, audit, clock)
			source, err := service.Create(context.Background(), CreateCredentialRequest{
				OrgID: "org_1", Name: "source", RepositoryScopes: []string{"owner/repo"}, CreatedBy: "actor_1",
			})
			require.NoError(t, err)
			credentialID := test.credentialID
			if credentialID == "" {
				credentialID = source.Credential.CredentialID
			}
			beforeAuditCount := len(audit.Events())

			// When
			issued, err := service.Rotate(context.Background(), RotateCredentialRequest{
				OrgID: test.rotationOrg, CredentialID: credentialID, CreatedBy: "actor_1", Overlap: time.Minute,
			})

			// Then
			require.ErrorIs(t, err, storage.ErrNotFound)
			require.Empty(t, issued.Token)
			require.Empty(t, issued.Credential.CredentialID)
			credentials, listErr := store.List(context.Background(), "org_1")
			require.NoError(t, listErr)
			require.Len(t, credentials, 1)
			stored, getErr := store.GetByID(context.Background(), "org_1", source.Credential.CredentialID)
			require.NoError(t, getErr)
			require.Nil(t, stored.RevokedAt)
			require.Nil(t, stored.ExpiresAt)
			require.Len(t, audit.Events(), beforeAuditCount)
		})
	}
}

func TestCredentialServiceRotateAllowsExpiredUnrevokedSourceWithBoundedOverlap(t *testing.T) {
	// Given
	startedAt := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	now := startedAt
	audit := memory.NewAuditStore()
	store, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{
		Audit: audit,
		Now:   func() time.Time { return now },
	})
	require.NoError(t, err)
	issuingService := newTestService(t, store, audit, startedAt)
	expiresAt := startedAt.Add(time.Minute)
	source, err := issuingService.Create(context.Background(), CreateCredentialRequest{
		OrgID: "org_1", Name: "expired", RepositoryScopes: []string{"owner/repo"}, CreatedBy: "actor_1", ExpiresAt: &expiresAt,
	})
	require.NoError(t, err)
	now = startedAt.Add(2 * time.Minute)
	rotatingService := newTestServiceFrom(t, store, audit, now, 10)

	// When
	replacement, err := rotatingService.Rotate(context.Background(), RotateCredentialRequest{
		OrgID: "org_1", CredentialID: source.Credential.CredentialID, CreatedBy: "actor_1", Overlap: time.Minute,
	})

	// Then
	require.NoError(t, err)
	require.NotEmpty(t, replacement.Token)
	credentials, err := store.List(context.Background(), "org_1")
	require.NoError(t, err)
	require.Len(t, credentials, 2)
	stored, err := store.GetByID(context.Background(), "org_1", source.Credential.CredentialID)
	require.NoError(t, err)
	require.Nil(t, stored.RevokedAt)
	require.Equal(t, expiresAt, *stored.ExpiresAt)
}
