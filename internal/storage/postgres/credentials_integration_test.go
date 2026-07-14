package postgres

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const credentialTestOrgID = "11111111-1111-1111-1111-111111111111"
const credentialTestActorID = "22222222-2222-2222-2222-222222222222"

func TestCredentialStore_rotatesWithOverlapAndScopesRevokeToOrganization(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{})
	require.NoError(t, err)
	created, err := service.Create(ctx, credentialCreateRequest("first"))
	require.NoError(t, err)

	// When
	rotated, err := service.Rotate(ctx, auth.RotateCredentialRequest{
		OrgID: credentialTestOrgID, CredentialID: created.Credential.CredentialID,
		Name: "replacement", RepositoryScopes: []string{"acme/widgets"}, Scopes: []string{auth.ScopeContextRead},
		CreatedBy: credentialTestActorID, Overlap: 5 * time.Minute,
	})

	// Then
	require.NoError(t, err)
	previous, err := store.GetByID(ctx, credentialTestOrgID, created.Credential.CredentialID)
	require.NoError(t, err)
	require.Nil(t, previous.RevokedAt)
	require.NotNil(t, previous.ExpiresAt)
	require.NotEqual(t, created.Token, rotated.Token)
	require.NotEmpty(t, rotated.Credential.TokenPrefix)

	// When
	_, err = service.Revoke(ctx, "33333333-3333-3333-3333-333333333333", created.Credential.CredentialID, credentialTestActorID)

	// Then
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestCredentialStore_immediateRotationAndRevocationDenyAuthentication(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{})
	require.NoError(t, err)
	created, err := service.Create(ctx, credentialCreateRequest("first"))
	require.NoError(t, err)
	_, err = service.Rotate(ctx, auth.RotateCredentialRequest{
		OrgID: credentialTestOrgID, CredentialID: created.Credential.CredentialID,
		Name: "replacement", RepositoryScopes: []string{"acme/widgets"}, Scopes: []string{auth.ScopeContextRead},
		CreatedBy: credentialTestActorID,
	})
	require.NoError(t, err)
	authenticator, err := auth.NewAuthenticator(store, audit, auth.AuthenticatorOptions{})
	require.NoError(t, err)
	handler := authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("revoked token authorized") }))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+created.Token)
	recorder := httptest.NewRecorder()

	// When
	handler.ServeHTTP(recorder, request)

	// Then
	require.Equal(t, http.StatusUnauthorized, recorder.Code)
	credential, err := store.GetByID(ctx, credentialTestOrgID, created.Credential.CredentialID)
	require.NoError(t, err)
	require.NotNil(t, credential.RevokedAt)
}

func TestCredentialStore_persistsOnlyHashAndLifecycleAudits(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{})
	require.NoError(t, err)

	// When
	created, err := service.Create(ctx, credentialCreateRequest("first"))

	// Then
	require.NoError(t, err)
	var storedHash string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT token_hash FROM acr.client_credentials WHERE credential_id = $1", created.Credential.CredentialID).Scan(&storedHash))
	require.Equal(t, auth.HashToken(created.Token), storedHash)
	require.NotEqual(t, created.Token, storedHash)
	var auditRows int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.audit_events WHERE org_id = $1 AND action = 'credential_created'", credentialTestOrgID).Scan(&auditRows))
	require.Equal(t, 1, auditRows)
}

func TestCredentialStore_recordsRotationAndRevocationAudits(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{})
	require.NoError(t, err)
	created, err := service.Create(ctx, credentialCreateRequest("first"))
	require.NoError(t, err)

	// When
	rotated, err := service.Rotate(ctx, auth.RotateCredentialRequest{
		OrgID: credentialTestOrgID, CredentialID: created.Credential.CredentialID,
		Name: "replacement", RepositoryScopes: []string{"acme/widgets"}, Scopes: []string{auth.ScopeContextRead},
		CreatedBy: credentialTestActorID,
	})
	require.NoError(t, err)
	_, err = service.Revoke(ctx, credentialTestOrgID, rotated.Credential.CredentialID, credentialTestActorID)

	// Then
	require.NoError(t, err)
	for _, action := range []string{"credential_created", "credential_rotated", "credential_revoked"} {
		var rows int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.audit_events WHERE org_id = $1 AND action = $2", credentialTestOrgID, action).Scan(&rows))
		require.Equal(t, 1, rows, action)
	}
}

func TestCredentialStore_persistsRotationActorOnReplacement(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{})
	require.NoError(t, err)
	created, err := service.Create(ctx, credentialCreateRequest("first"))
	require.NoError(t, err)

	// When
	rotated, err := service.Rotate(ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: created.Credential.CredentialID, CreatedBy: credentialTestActorID})

	// Then
	require.NoError(t, err)
	var createdBy string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT created_by FROM acr.client_credentials WHERE credential_id = $1", rotated.Credential.CredentialID).Scan(&createdBy))
	require.Equal(t, credentialTestActorID, createdBy)
}

func TestCredentialStore_rollsBackLifecycleWhenAuditRecordingFails(t *testing.T) {
	ctx := context.Background()

	t.Run("create", func(t *testing.T) {
		db := newCredentialStoreDatabase(t, ctx)
		audit, err := NewAuditStore(db)
		require.NoError(t, err)
		store, err := NewCredentialStore(db, audit)
		require.NoError(t, err)
		audit.GenerateID = func() (string, error) { return "", errAuditUnavailable }
		service, err := auth.NewService(store, auth.ServiceOptions{})
		require.NoError(t, err)

		issued, err := service.Create(ctx, credentialCreateRequest("first"))

		require.ErrorIs(t, err, errAuditUnavailable)
		require.Empty(t, issued.Token)
		assertCredentialAndAuditCounts(t, ctx, db, 0, 0)
	})

	t.Run("rotate", func(t *testing.T) {
		db := newCredentialStoreDatabase(t, ctx)
		audit, err := NewAuditStore(db)
		require.NoError(t, err)
		store, err := NewCredentialStore(db, audit)
		require.NoError(t, err)
		service, err := auth.NewService(store, auth.ServiceOptions{})
		require.NoError(t, err)
		original, err := service.Create(ctx, credentialCreateRequest("first"))
		require.NoError(t, err)
		audit.GenerateID = func() (string, error) { return "", errAuditUnavailable }

		issued, err := service.Rotate(ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: original.Credential.CredentialID, CreatedBy: credentialTestActorID})

		require.ErrorIs(t, err, errAuditUnavailable)
		require.Empty(t, issued.Token)
		stored, getErr := store.GetByID(ctx, credentialTestOrgID, original.Credential.CredentialID)
		require.NoError(t, getErr)
		require.Nil(t, stored.RevokedAt)
		require.Nil(t, stored.ExpiresAt)
		assertCredentialAndAuditCounts(t, ctx, db, 1, 1)
	})

	t.Run("revoke", func(t *testing.T) {
		db := newCredentialStoreDatabase(t, ctx)
		audit, err := NewAuditStore(db)
		require.NoError(t, err)
		store, err := NewCredentialStore(db, audit)
		require.NoError(t, err)
		service, err := auth.NewService(store, auth.ServiceOptions{})
		require.NoError(t, err)
		original, err := service.Create(ctx, credentialCreateRequest("first"))
		require.NoError(t, err)
		audit.GenerateID = func() (string, error) { return "", errAuditUnavailable }

		revoked, err := service.Revoke(ctx, credentialTestOrgID, original.Credential.CredentialID, credentialTestActorID)

		require.ErrorIs(t, err, errAuditUnavailable)
		require.Empty(t, revoked.CredentialID)
		stored, getErr := store.GetByID(ctx, credentialTestOrgID, original.Credential.CredentialID)
		require.NoError(t, getErr)
		require.Nil(t, stored.RevokedAt)
		assertCredentialAndAuditCounts(t, ctx, db, 1, 1)
	})
}

func TestCredentialStore_rejectsAuditStoreFromDifferentDatabase(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	foreignDB := newCredentialStoreDatabase(t, ctx)
	foreignAudit, err := NewAuditStore(foreignDB)
	require.NoError(t, err)
	store, err := NewCredentialStore(db, foreignAudit)
	require.Error(t, err)
	require.Nil(t, store)
}

var errAuditUnavailable = errors.New("audit persistence unavailable")

func assertCredentialAndAuditCounts(t *testing.T, ctx context.Context, db *sql.DB, credentials, audits int) {
	t.Helper()
	var actualCredentials, actualAudits int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.client_credentials WHERE org_id = $1", credentialTestOrgID).Scan(&actualCredentials))
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.audit_events WHERE org_id = $1", credentialTestOrgID).Scan(&actualAudits))
	require.Equal(t, credentials, actualCredentials)
	require.Equal(t, audits, actualAudits)
}

func credentialCreateRequest(name string) auth.CreateCredentialRequest {
	return auth.CreateCredentialRequest{
		OrgID: credentialTestOrgID, Name: name, RepositoryScopes: []string{"acme/widgets"},
		Scopes: []string{auth.ScopeContextRead}, CreatedBy: credentialTestActorID,
	}
}

func newCredentialStoreDatabase(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runner, err := migrations.Embedded()
	require.NoError(t, err)
	_, err = runner.Apply(ctx, db)
	require.NoError(t, err)
	return db
}
