package postgres

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestCredentialStore_allows_only_one_concurrent_overlap_rotation(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{})
	require.NoError(t, err)
	source, err := service.Create(ctx, credentialCreateRequest("source"))
	require.NoError(t, err)
	results := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(1)
	for _, name := range []string{"replacement-a", "replacement-b"} {
		go func(name string) {
			start.Wait()
			_, rotateErr := service.Rotate(ctx, auth.RotateCredentialRequest{
				OrgID: credentialTestOrgID, CredentialID: source.Credential.CredentialID,
				Name: name, RepositoryScopes: []string{"acme/widgets"}, Scopes: []string{auth.ScopeContextRead},
				CreatedBy: credentialTestActorID, Overlap: time.Minute,
			})
			results <- rotateErr
		}(name)
	}

	// When
	start.Done()
	first, second := <-results, <-results

	// Then
	require.True(t, (first == nil) != (second == nil), "results = %v, %v", first, second)
	require.True(t, errors.Is(first, storage.ErrConflict) || errors.Is(second, storage.ErrConflict), "results = %v, %v", first, second)
	assertCredentialAndAuditCounts(t, ctx, db, 2, 2)
}

func TestCredentialStore_maps_unique_token_hash_conflicts_to_storage_conflict(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	var id uint
	service, err := auth.NewService(store, auth.ServiceOptions{
		GenerateToken: func() (string, error) {
			return auth.TokenPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32)), nil
		},
		GenerateCredentialID: func() (string, error) {
			id++
			return "cred_conflict_" + string(rune('a'+id)), nil
		},
	})
	require.NoError(t, err)
	_, err = service.Create(ctx, credentialCreateRequest("first"))
	require.NoError(t, err)

	// When
	_, err = service.Create(ctx, credentialCreateRequest("second"))

	// Then
	require.ErrorIs(t, err, storage.ErrConflict)
}

func TestCredentialStore_preservesContextCancellationClassification(t *testing.T) {
	for _, expected := range []error{context.Canceled, context.DeadlineExceeded} {
		// When
		actual := sanitizeDatabaseError(expected)

		// Then
		require.ErrorIs(t, actual, expected)
		require.NotErrorIs(t, actual, storage.ErrUnavailable)
	}
}

func TestAuditStore_rejectsReservedCredentialLifecycleActions(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)

	// When
	err = audit.Record(ctx, storage.AuditEvent{
		OrgID: credentialTestOrgID, ActorType: "user", ActorID: credentialTestActorID,
		Action: "credential_revoked", ResourceType: "acr_credential", ResourceID: "cred_fabricated",
		Status: "success", CreatedAt: time.Now().UTC(),
	})

	// Then
	require.Error(t, err)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.audit_events WHERE action = 'credential_revoked'").Scan(&count))
	require.Zero(t, count)
}

func TestCredentialStore_rejectsRepeatedRevokeWithExactlyOneAudit(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{})
	require.NoError(t, err)
	issued, err := service.Create(ctx, credentialCreateRequest("revoke-once"))
	require.NoError(t, err)
	_, err = service.Revoke(ctx, credentialTestOrgID, issued.Credential.CredentialID, credentialTestActorID)
	require.NoError(t, err)

	// When
	_, err = service.Revoke(ctx, credentialTestOrgID, issued.Credential.CredentialID, credentialTestActorID)

	// Then
	require.ErrorIs(t, err, storage.ErrConflict)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.audit_events WHERE action = 'credential_revoked' AND resource_id = $1", issued.Credential.CredentialID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestCredentialStore_rejectsTokenLookupAfterRevokeCommit(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{})
	require.NoError(t, err)
	issued, err := service.Create(ctx, credentialCreateRequest("lookup"))
	require.NoError(t, err)
	_, err = service.Revoke(ctx, credentialTestOrgID, issued.Credential.CredentialID, credentialTestActorID)
	require.NoError(t, err)

	// When
	_, err = store.FindByTokenHash(ctx, auth.HashToken(issued.Token))

	// Then
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestCredentialStore_zeroValueReturnsErrorInsteadOfPanicking(t *testing.T) {
	// Given
	var store storage.CredentialLifecycle

	// When
	require.NotPanics(t, func() {
		_, err := store.CreateCredential(context.Background(), storage.CredentialCreateInput{})
		require.Error(t, err)
	})
}

func TestCredentialStore_sanitizesUnknownDatabaseErrors(t *testing.T) {
	// Given
	secret := "driver host=private password=secret"

	// When
	err := sanitizeDatabaseError(errors.New(secret))

	// Then
	require.ErrorIs(t, err, storage.ErrUnavailable)
	require.NotContains(t, err.Error(), secret)
	require.False(t, strings.Contains(err.Error(), "password"))
}
