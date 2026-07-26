package auth

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	"github.com/stretchr/testify/require"
)

func TestService_RotateSelf_preservesStoredGrantAndUsesFixedLifecycle(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, now, audit)
	service := newTestService(t, store, audit, now)
	source := createSelfCredential(t, service, []string{"owner/repo"})
	principal := selfPrincipal(source.Credential)

	// When
	rotation, err := service.RotateSelf(context.Background(), principal)

	// Then
	require.NoError(t, err)
	require.True(t, IsTokenShapeValid(rotation.Issued.Token))
	require.Equal(t, source.Credential.RepositoryScopes, rotation.Issued.Credential.RepositoryScopes)
	require.Equal(t, source.Credential.Scopes, rotation.Issued.Credential.Scopes)
	require.NotNil(t, rotation.Issued.Credential.ExpiresAt)
	require.Equal(t, now.Add(DeviceCredentialLifetime), *rotation.Issued.Credential.ExpiresAt)
	require.Equal(t, source.Credential.CredentialID, rotation.Receipt.SourceCredentialID)
	require.Equal(t, rotation.Issued.Credential.CredentialID, rotation.Receipt.SuccessorCredentialID)
	require.Equal(t, now.Add(storage.MaximumCredentialOverlap), rotation.Receipt.RollbackUntil)
	previous, err := store.GetByID(context.Background(), principal.OrgID, principal.CredentialID)
	require.NoError(t, err)
	require.Nil(t, previous.RevokedAt)
	require.NotNil(t, previous.ExpiresAt)
	require.Equal(t, now.Add(storage.MaximumCredentialOverlap), *previous.ExpiresAt)
}

func TestService_RotateSelf_clampsRollbackUntilToSourceExpiry(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, now, audit)
	service := newTestService(t, store, audit, now)
	expiresAt := now.Add(5 * time.Minute)
	source, err := service.Create(context.Background(), CreateCredentialRequest{
		OrgID: deviceFlowTestOrgID, Name: "short-lived self credential", RepositoryScopes: []string{"owner/repo"}, CreatedBy: "actor_1", ExpiresAt: &expiresAt,
	})
	require.NoError(t, err)

	// When
	rotation, err := service.RotateSelf(context.Background(), selfPrincipal(source.Credential))

	// Then
	require.NoError(t, err)
	require.Equal(t, expiresAt, rotation.Receipt.RollbackUntil)
}

func TestService_RotateSelf_rejectsUntrustedPrincipalWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*storage.Principal)
	}{
		{name: "web assertion", mutate: func(principal *storage.Principal) {
			principal.AuthenticationMethod = storage.AuthenticationMethodWebAssertion
			principal.CredentialID = ""
		}},
		{name: "mismatched subject", mutate: func(principal *storage.Principal) {
			principal.Subject = "cred_other"
		}},
		{name: "missing organization", mutate: func(principal *storage.Principal) {
			principal.OrgID = ""
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
			audit := memory.NewAuditStore()
			store := newMemoryCredentialStoreAt(t, now, audit)
			service := newTestService(t, store, audit, now)
			source := createSelfCredential(t, service, []string{"owner/repo"})
			principal := selfPrincipal(source.Credential)
			test.mutate(&principal)
			beforeEvents := len(audit.Events())

			// When
			rotation, err := service.RotateSelf(context.Background(), principal)

			// Then
			require.ErrorIs(t, err, ErrInvalidCredential)
			require.Empty(t, rotation.Issued.Token)
			credentials, listErr := store.List(context.Background(), source.Credential.OrgID)
			require.NoError(t, listErr)
			require.Len(t, credentials, 1)
			require.Len(t, audit.Events(), beforeEvents)
		})
	}
}

func TestService_RevokeSelf_revokesOnlyAuthenticatedCredential(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, now, audit)
	service := newTestService(t, store, audit, now)
	current := createSelfCredential(t, service, []string{"owner/current"})
	other := createSelfCredential(t, service, []string{"owner/other"})

	// When
	revoked, err := service.RevokeSelf(context.Background(), selfPrincipal(current.Credential))

	// Then
	require.NoError(t, err)
	require.Equal(t, current.Credential.CredentialID, revoked.CredentialID)
	storedCurrent, err := store.GetByID(context.Background(), current.Credential.OrgID, current.Credential.CredentialID)
	require.NoError(t, err)
	require.NotNil(t, storedCurrent.RevokedAt)
	untouched, err := store.GetByID(context.Background(), other.Credential.OrgID, other.Credential.CredentialID)
	require.NoError(t, err)
	require.Nil(t, untouched.RevokedAt)
}

func TestService_RollbackSelfRotation_requiresSuccessorAndRetainsSourceOverlap(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, now, audit)
	service := newTestService(t, store, audit, now)
	source := createSelfCredential(t, service, []string{"owner/repo"})
	sourcePrincipal := selfPrincipal(source.Credential)
	rotation, err := service.RotateSelf(context.Background(), sourcePrincipal)
	require.NoError(t, err)

	// When
	_, sourceRollbackErr := service.RollbackSelfRotation(context.Background(), sourcePrincipal, rotation.Receipt)
	revoked, successorRollbackErr := service.RollbackSelfRotation(
		context.Background(), selfPrincipal(rotation.Issued.Credential), rotation.Receipt,
	)

	// Then
	require.ErrorIs(t, sourceRollbackErr, ErrInvalidCredential)
	require.NoError(t, successorRollbackErr)
	require.Equal(t, rotation.Receipt.SuccessorCredentialID, revoked.CredentialID)
	storedSuccessor, err := store.GetByID(context.Background(), source.Credential.OrgID, rotation.Receipt.SuccessorCredentialID)
	require.NoError(t, err)
	require.NotNil(t, storedSuccessor.RevokedAt)
	retained, err := store.GetByID(context.Background(), source.Credential.OrgID, source.Credential.CredentialID)
	require.NoError(t, err)
	require.Nil(t, retained.RevokedAt)
	require.NotNil(t, retained.ExpiresAt)
	require.Equal(t, now.Add(storage.MaximumCredentialOverlap), *retained.ExpiresAt)
}

func TestService_SelfLifecycle_staleOperationsFailWithoutSecondSecret(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, now, audit)
	service := newTestService(t, store, audit, now)
	source := createSelfCredential(t, service, []string{"owner/repo"})
	principal := selfPrincipal(source.Credential)
	first, err := service.RotateSelf(context.Background(), principal)
	require.NoError(t, err)

	// When
	second, staleRotateErr := service.RotateSelf(context.Background(), principal)
	_, firstRevokeErr := service.RevokeSelf(context.Background(), selfPrincipal(first.Issued.Credential))
	_, staleRevokeErr := service.RevokeSelf(context.Background(), selfPrincipal(first.Issued.Credential))

	// Then
	require.Error(t, staleRotateErr)
	require.Empty(t, second.Issued.Token)
	require.NotContains(t, staleRotateErr.Error(), first.Issued.Token)
	require.NoError(t, firstRevokeErr)
	require.Error(t, staleRevokeErr)
}

func TestService_RollbackSelfRotation_rejectsExpiredSourceOverlap(t *testing.T) {
	for _, elapsed := range []time.Duration{storage.MaximumCredentialOverlap, storage.MaximumCredentialOverlap + time.Second} {
		t.Run(elapsed.String(), func(t *testing.T) {
			// Given
			now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
			audit := memory.NewAuditStore()
			store, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{
				Audit: audit,
				Now:   func() time.Time { return now },
			})
			require.NoError(t, err)
			service, err := NewService(store, ServiceOptions{Now: func() time.Time { return now }})
			require.NoError(t, err)
			source := createSelfCredential(t, service, []string{"owner/repo"})
			rotation, err := service.RotateSelf(context.Background(), selfPrincipal(source.Credential))
			require.NoError(t, err)
			now = now.Add(elapsed)

			// When
			revoked, rollbackErr := service.RollbackSelfRotation(
				context.Background(), selfPrincipal(rotation.Issued.Credential), rotation.Receipt,
			)

			// Then
			require.ErrorIs(t, rollbackErr, ErrInvalidCredential)
			require.Empty(t, revoked.CredentialID)
			successor, getErr := store.GetByID(context.Background(), rotation.Issued.Credential.OrgID, rotation.Issued.Credential.CredentialID)
			require.NoError(t, getErr)
			require.Nil(t, successor.RevokedAt)
		})
	}
}

func createSelfCredential(t *testing.T, service *Service, repositories []string) IssuedCredential {
	t.Helper()
	issued, err := service.Create(context.Background(), CreateCredentialRequest{
		OrgID: deviceFlowTestOrgID, Name: "self credential", RepositoryScopes: repositories, CreatedBy: "actor_1",
	})
	require.NoError(t, err)
	return issued
}

func selfPrincipal(credential contractsv1.ClientCredential) storage.Principal {
	return storage.Principal{
		AuthenticationMethod: storage.AuthenticationMethodCredential,
		Subject:              credential.CredentialID, OrgID: credential.OrgID, CredentialID: credential.CredentialID,
		RepositoryScopes: append([]string(nil), credential.RepositoryScopes...),
		Permissions:      append([]string(nil), credential.Scopes...),
	}
}
