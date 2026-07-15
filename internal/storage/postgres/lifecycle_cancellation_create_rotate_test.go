package postgres

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/stretchr/testify/require"
)

func TestCredentialStore_CreateRollsBackWhenCancellationFollowsBeginTx(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{})
	require.NoError(t, err)
	entered, release := blockAuditIDGeneration(audit)
	canceledCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() {
		_, createErr := service.Create(canceledCtx, credentialCreateRequest("cancel-create"))
		result <- createErr
	}()
	<-entered

	// When
	cancel()
	close(release)
	err = <-result

	// Then
	require.ErrorIs(t, err, context.Canceled)
	assertCredentialAndAuditCounts(t, ctx, db, 0, 0)
}

func TestCredentialStore_RotateRollsBackWhenCancellationFollowsBeginTx(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{})
	require.NoError(t, err)
	source, err := service.Create(ctx, credentialCreateRequest("cancel-rotate"))
	require.NoError(t, err)
	entered, release := blockAuditIDGeneration(audit)
	canceledCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() {
		_, rotateErr := service.Rotate(canceledCtx, auth.RotateCredentialRequest{
			OrgID: credentialTestOrgID, CredentialID: source.Credential.CredentialID, CreatedBy: credentialTestActorID,
		})
		result <- rotateErr
	}()
	<-entered

	// When
	cancel()
	close(release)
	err = <-result

	// Then
	require.ErrorIs(t, err, context.Canceled)
	stored, err := store.GetByID(ctx, credentialTestOrgID, source.Credential.CredentialID)
	require.NoError(t, err)
	require.Nil(t, stored.RevokedAt)
	require.Nil(t, stored.ExpiresAt)
	var rotatedAt sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx, "SELECT rotated_at FROM acr.client_credentials WHERE credential_id = $1", source.Credential.CredentialID).Scan(&rotatedAt))
	require.False(t, rotatedAt.Valid)
	assertCredentialAndAuditCounts(t, ctx, db, 1, 1)
}

func blockAuditIDGeneration(audit *AuditStore) (<-chan struct{}, chan<- struct{}) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	audit.GenerateID = func() (string, error) {
		once.Do(func() { close(entered) })
		<-release
		return generateUUID()
	}
	return entered, release
}
