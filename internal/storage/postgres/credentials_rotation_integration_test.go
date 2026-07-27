package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestCredentialStoreRotateRejectsRevokedCrossOrgAndUnknownSourcesWithoutSideEffects(t *testing.T) {
	tests := []struct {
		name         string
		rotationOrg  string
		credentialID func(string) string
		wantError    error
		prepare      func(context.Context, *auth.Service, string) error
		wantAudits   int
	}{
		{
			name:         "revoked source",
			rotationOrg:  credentialTestOrgID,
			credentialID: func(sourceID string) string { return sourceID },
			wantError:    auth.ErrInvalidCredential,
			prepare: func(ctx context.Context, service *auth.Service, sourceID string) error {
				_, err := service.Revoke(ctx, credentialTestOrgID, sourceID, credentialTestActorID)
				return err
			},
			wantAudits: 2,
		},
		{
			name:         "cross organization source",
			rotationOrg:  "33333333-3333-3333-3333-333333333333",
			credentialID: func(sourceID string) string { return sourceID },
			wantError:    storage.ErrNotFound,
			prepare:      func(context.Context, *auth.Service, string) error { return nil },
			wantAudits:   1,
		},
		{
			name:         "unknown source",
			rotationOrg:  credentialTestOrgID,
			credentialID: func(string) string { return "cred_unknown" },
			wantError:    storage.ErrNotFound,
			prepare:      func(context.Context, *auth.Service, string) error { return nil },
			wantAudits:   1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
			require.NoError(t, test.prepare(ctx, service, source.Credential.CredentialID))

			// When
			issued, err := service.Rotate(ctx, auth.RotateCredentialRequest{
				OrgID: test.rotationOrg, CredentialID: test.credentialID(source.Credential.CredentialID),
				CreatedBy: credentialTestActorID, Overlap: time.Minute,
			})

			// Then
			require.ErrorIs(t, err, test.wantError)
			require.Empty(t, issued.Token)
			require.Empty(t, issued.Credential.CredentialID)
			var credentials, rotatedAudits int
			require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.client_credentials WHERE org_id = $1", credentialTestOrgID).Scan(&credentials))
			require.Equal(t, 1, credentials)
			require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.audit_events WHERE org_id = $1", credentialTestOrgID).Scan(&rotatedAudits))
			require.Equal(t, test.wantAudits, rotatedAudits)
			var successfulRotations int
			require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.audit_events WHERE org_id = $1 AND action = 'credential_rotated'", credentialTestOrgID).Scan(&successfulRotations))
			require.Zero(t, successfulRotations)
		})
	}
}

func TestCredentialStoreRotateCredentialRejectsRevokedSourceWithoutSideEffects(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	lifecycle, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	source, err := lifecycle.CreateCredential(ctx, storage.CredentialCreateInput{
		CredentialID: "cred_source", OrgID: credentialTestOrgID, Name: "source", TokenPrefix: "fcacr_source",
		TokenHash: strings.Repeat("a", 64), RepositoryScopes: []string{"acme/widgets"},
		Scopes: []string{auth.ScopeContextRead}, ActorID: credentialTestActorID,
	})
	require.NoError(t, err)
	_, err = lifecycle.RevokeCredential(ctx, storage.CredentialRevocationInput{
		OrgID: credentialTestOrgID, CredentialID: source.CredentialID, ActorID: credentialTestActorID,
	})
	require.NoError(t, err)
	revokedSource, err := lifecycle.GetByID(ctx, credentialTestOrgID, source.CredentialID)
	require.NoError(t, err)
	require.NotNil(t, revokedSource.RevokedAt)
	var revokedAtBefore time.Time
	var rotatedAtBefore sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT revoked_at, rotated_at
		FROM acr.client_credentials
		WHERE credential_id = $1`, source.CredentialID).Scan(&revokedAtBefore, &rotatedAtBefore))
	require.False(t, rotatedAtBefore.Valid)

	// When
	replacement, err := lifecycle.RotateCredential(ctx, storage.CredentialRotationInput{
		OrgID: credentialTestOrgID, SourceCredentialID: source.CredentialID, ActorID: credentialTestActorID,
		Replacement: storage.CredentialRotationReplacement{
			CredentialID: "cred_replacement", Name: "replacement", TokenPrefix: "fcacr_replacement",
			TokenHash: strings.Repeat("b", 64), RepositoryScopes: []string{"acme/widgets"},
			Scopes: []string{auth.ScopeContextRead}, Immediate: true,
		},
	})

	// Then
	require.ErrorIs(t, err, storage.ErrNotFound)
	require.Empty(t, replacement.CredentialID)
	storedSource, err := lifecycle.GetByID(ctx, credentialTestOrgID, source.CredentialID)
	require.NoError(t, err)
	require.Equal(t, revokedSource, storedSource)
	var revokedAtAfter time.Time
	var rotatedAtAfter sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT revoked_at, rotated_at
		FROM acr.client_credentials
		WHERE credential_id = $1`, source.CredentialID).Scan(&revokedAtAfter, &rotatedAtAfter))
	require.True(t, revokedAtAfter.Equal(revokedAtBefore))
	require.False(t, rotatedAtAfter.Valid)
	_, err = lifecycle.GetByID(ctx, credentialTestOrgID, "cred_replacement")
	require.ErrorIs(t, err, storage.ErrNotFound)
	assertCredentialAndAuditCounts(t, ctx, db, 1, 2)
	var successfulRotations int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.audit_events WHERE org_id = $1 AND action = 'credential_rotated'", credentialTestOrgID).Scan(&successfulRotations))
	require.Zero(t, successfulRotations)
}

func TestCredentialStoreRotateAllowsExpiredUnrevokedSourceWithBoundedOverlap(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	startedAt := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	now := startedAt
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	_, store, err := newCredentialStore(db, audit, func() time.Time { return now })
	require.NoError(t, err)
	service, err := auth.NewService(store, auth.ServiceOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	expiresAt := startedAt.Add(time.Minute)
	source, err := service.Create(ctx, auth.CreateCredentialRequest{
		OrgID: credentialTestOrgID, Name: "expired", RepositoryScopes: []string{"acme/widgets"},
		Scopes: []string{auth.ScopeContextRead}, CreatedBy: credentialTestActorID, ExpiresAt: &expiresAt,
	})
	require.NoError(t, err)
	now = startedAt.Add(2 * time.Minute)

	// When
	replacement, err := service.Rotate(ctx, auth.RotateCredentialRequest{
		OrgID: credentialTestOrgID, CredentialID: source.Credential.CredentialID,
		CreatedBy: credentialTestActorID, Overlap: time.Minute,
	})

	// Then
	require.NoError(t, err)
	require.NotEmpty(t, replacement.Token)
	var credentials, rotations int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.client_credentials WHERE org_id = $1", credentialTestOrgID).Scan(&credentials))
	require.Equal(t, 2, credentials)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.audit_events WHERE org_id = $1 AND action = 'credential_rotated'", credentialTestOrgID).Scan(&rotations))
	require.Equal(t, 1, rotations)
	stored, err := store.GetByID(ctx, credentialTestOrgID, source.Credential.CredentialID)
	require.NoError(t, err)
	require.Nil(t, stored.RevokedAt)
	require.True(t, expiresAt.Equal(*stored.ExpiresAt))
}

func TestCredentialStoreRollbackRejectsSuccessorFromAnotherRotationWithoutPartialMutation(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	lifecycle, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(lifecycle, auth.ServiceOptions{})
	require.NoError(t, err)
	first, err := service.Create(ctx, credentialCreateRequest("first"))
	require.NoError(t, err)
	second, err := service.Create(ctx, credentialCreateRequest("second"))
	require.NoError(t, err)
	firstReplacement, err := service.Rotate(ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: first.Credential.CredentialID, CreatedBy: credentialTestActorID, Overlap: time.Minute})
	require.NoError(t, err)
	secondReplacement, err := service.Rotate(ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: second.Credential.CredentialID, CreatedBy: credentialTestActorID, Overlap: time.Minute})
	require.NoError(t, err)

	_, err = lifecycle.RollbackCredentialRotation(ctx, storage.CredentialRotationRollbackInput{
		OrgID: credentialTestOrgID, SourceCredentialID: first.Credential.CredentialID,
		SuccessorCredentialID: secondReplacement.Credential.CredentialID, ActorID: credentialTestActorID,
		RollbackUntil: time.Now().UTC().Add(time.Minute),
	})

	require.ErrorIs(t, err, storage.ErrConflict)
	stored, err := lifecycle.GetByID(ctx, credentialTestOrgID, secondReplacement.Credential.CredentialID)
	require.NoError(t, err)
	require.Nil(t, stored.RevokedAt)
	firstStored, err := lifecycle.GetByID(ctx, credentialTestOrgID, firstReplacement.Credential.CredentialID)
	require.NoError(t, err)
	require.Nil(t, firstStored.RevokedAt)
}

func TestCredentialStoreRollbackKeepsSuccessorActiveWhenAuditWriteFails(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	lifecycle, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(lifecycle, auth.ServiceOptions{})
	require.NoError(t, err)
	source, err := service.Create(ctx, credentialCreateRequest("source"))
	require.NoError(t, err)
	successor, err := service.Rotate(ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: source.Credential.CredentialID, CreatedBy: credentialTestActorID, Overlap: time.Minute})
	require.NoError(t, err)
	audit.GenerateID = func() (string, error) { return "", errors.New("audit unavailable") }

	_, err = lifecycle.RollbackCredentialRotation(ctx, storage.CredentialRotationRollbackInput{
		OrgID: credentialTestOrgID, SourceCredentialID: source.Credential.CredentialID,
		SuccessorCredentialID: successor.Credential.CredentialID, ActorID: credentialTestActorID,
		RollbackUntil: time.Now().UTC().Add(time.Minute),
	})

	require.ErrorIs(t, err, storage.ErrUnavailable)
	stored, getErr := lifecycle.GetByID(ctx, credentialTestOrgID, successor.Credential.CredentialID)
	require.NoError(t, getErr)
	require.Nil(t, stored.RevokedAt)
}

func TestCredentialStoreRollbackRejectsSuccessorThatWasRotatedAgainWithoutAuditMutation(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	lifecycle, err := NewCredentialStore(db, audit)
	require.NoError(t, err)
	service, err := auth.NewService(lifecycle, auth.ServiceOptions{})
	require.NoError(t, err)
	source, err := service.Create(ctx, credentialCreateRequest("source"))
	require.NoError(t, err)
	first, err := service.Rotate(ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: source.Credential.CredentialID, CreatedBy: credentialTestActorID, Overlap: time.Minute})
	require.NoError(t, err)
	_, err = service.Rotate(ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: first.Credential.CredentialID, CreatedBy: credentialTestActorID, Overlap: time.Minute})
	require.NoError(t, err)
	var auditsBefore int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.audit_events WHERE org_id = $1", credentialTestOrgID).Scan(&auditsBefore))

	_, err = lifecycle.RollbackCredentialRotation(ctx, storage.CredentialRotationRollbackInput{
		OrgID: credentialTestOrgID, SourceCredentialID: source.Credential.CredentialID,
		SuccessorCredentialID: first.Credential.CredentialID, ActorID: credentialTestActorID,
		RollbackUntil: time.Now().UTC().Add(time.Minute),
	})

	require.ErrorIs(t, err, storage.ErrConflict)
	stored, getErr := lifecycle.GetByID(ctx, credentialTestOrgID, first.Credential.CredentialID)
	require.NoError(t, getErr)
	require.Nil(t, stored.RevokedAt)
	var auditsAfter int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM acr.audit_events WHERE org_id = $1", credentialTestOrgID).Scan(&auditsAfter))
	require.Equal(t, auditsBefore, auditsAfter)
}
