package auth

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	"github.com/stretchr/testify/require"
)

func TestService_RollbackSelfRotationRejectsShortenedReceiptBeforeSourceOverlapExpires(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: audit, Now: func() time.Time { return now }})
	require.NoError(t, err)
	service, err := NewService(store, ServiceOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	source := createSelfCredential(t, service, []string{"owner/repo"})
	rotation, err := service.RotateSelf(context.Background(), selfPrincipal(source.Credential))
	require.NoError(t, err)
	rotation.Receipt.RollbackUntil = now.Add(time.Second)
	now = now.Add(2 * time.Second)

	// When
	_, err = service.RollbackSelfRotation(context.Background(), selfPrincipal(rotation.Issued.Credential), rotation.Receipt)

	// Then
	require.ErrorIs(t, err, ErrInvalidCredential)
	successor, getErr := store.GetByID(context.Background(), source.Credential.OrgID, rotation.Issued.Credential.CredentialID)
	require.NoError(t, getErr)
	require.Nil(t, successor.RevokedAt)
	require.Len(t, rollbackAudits(audit.Events()), 0)
}

func TestService_RollbackSelfRotationPreservesAuthenticatedGrantAndAuditsActor(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, now, audit)
	service := newTestService(t, store, audit, now)
	source := createSelfCredential(t, service, []string{"owner/repo"})
	rotation, err := service.RotateSelf(context.Background(), selfPrincipal(source.Credential))
	require.NoError(t, err)
	principal := selfPrincipal(rotation.Issued.Credential)

	// When
	revoked, err := service.RollbackSelfRotation(context.Background(), principal, rotation.Receipt)

	// Then
	require.NoError(t, err)
	require.Equal(t, rotation.Receipt.SuccessorCredentialID, revoked.CredentialID)
	entries := rollbackAudits(audit.Events())
	require.Len(t, entries, 1)
	require.Equal(t, principal.OrgID, entries[0].OrgID)
	require.Equal(t, principal.Subject, entries[0].ActorID)
	require.Equal(t, rotation.Receipt.SuccessorCredentialID, entries[0].ResourceID)
}

func TestService_RollbackSelfRotationRejectsAlteredPrincipalWithoutStateOrAuditMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*storage.Principal)
	}{
		{name: "wrong organization", mutate: func(principal *storage.Principal) { principal.OrgID = "org_other" }},
		{name: "wrong subject", mutate: func(principal *storage.Principal) { principal.Subject = "cred_other" }},
		{name: "repository scope", mutate: func(principal *storage.Principal) { principal.RepositoryScopes = []string{"owner/other"} }},
		{name: "permission", mutate: func(principal *storage.Principal) { principal.Permissions = []string{"evidence:read"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
			audit := memory.NewAuditStore()
			store := newMemoryCredentialStoreAt(t, now, audit)
			service := newTestService(t, store, audit, now)
			source := createSelfCredential(t, service, []string{"owner/repo"})
			rotation, err := service.RotateSelf(context.Background(), selfPrincipal(source.Credential))
			require.NoError(t, err)
			principal := selfPrincipal(rotation.Issued.Credential)
			test.mutate(&principal)
			before := len(rollbackAudits(audit.Events()))

			// When
			_, err = service.RollbackSelfRotation(context.Background(), principal, rotation.Receipt)

			// Then
			require.Error(t, err)
			successor, getErr := store.GetByID(context.Background(), source.Credential.OrgID, rotation.Issued.Credential.CredentialID)
			require.NoError(t, getErr)
			require.Nil(t, successor.RevokedAt)
			require.Len(t, rollbackAudits(audit.Events()), before)
		})
	}
}

func rollbackAudits(events []storage.AuditEvent) []storage.AuditEvent {
	entries := make([]storage.AuditEvent, 0, 1)
	for _, event := range events {
		if event.Action == storage.AuditActionCredentialRevoked && event.ResourceType == "acr_credential" {
			entries = append(entries, event)
		}
	}
	return entries
}

func TestService_RollbackSelfRotationRejectsAlreadyRevokedSuccessorWithoutAudit(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, now, audit)
	service := newTestService(t, store, audit, now)
	source := createSelfCredential(t, service, []string{"owner/repo"})
	rotation, err := service.RotateSelf(context.Background(), selfPrincipal(source.Credential))
	require.NoError(t, err)
	principal := selfPrincipal(rotation.Issued.Credential)
	_, err = service.RollbackSelfRotation(context.Background(), principal, rotation.Receipt)
	require.NoError(t, err)
	before := len(rollbackAudits(audit.Events()))

	// When
	_, err = service.RollbackSelfRotation(context.Background(), principal, rotation.Receipt)

	// Then
	require.ErrorIs(t, err, storage.ErrConflict)
	require.Len(t, rollbackAudits(audit.Events()), before)
}

func TestService_RollbackSelfRotationDoesNotRevokeSourceAfterSuccess(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, now, audit)
	service := newTestService(t, store, audit, now)
	source := createSelfCredential(t, service, []string{"owner/repo"})
	rotation, err := service.RotateSelf(context.Background(), selfPrincipal(source.Credential))
	require.NoError(t, err)

	// When
	_, err = service.RollbackSelfRotation(context.Background(), selfPrincipal(rotation.Issued.Credential), rotation.Receipt)

	// Then
	require.NoError(t, err)
	storedSource, getErr := store.GetByID(context.Background(), source.Credential.OrgID, source.Credential.CredentialID)
	require.NoError(t, getErr)
	require.Nil(t, storedSource.RevokedAt)
	require.NotNil(t, storedSource.ExpiresAt)
	require.True(t, storedSource.ExpiresAt.After(now))
}
