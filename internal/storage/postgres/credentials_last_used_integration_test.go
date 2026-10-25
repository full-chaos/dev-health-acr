package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestCredentialStore_TouchLastUsed_keepsNewestMetadata(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	_, lifecycle, err := newCredentialStore(db, audit, time.Now)
	require.NoError(t, err)
	service, err := auth.NewService(lifecycle, auth.ServiceOptions{})
	require.NoError(t, err)
	created, err := service.Create(ctx, credentialCreateRequest("last-used"))
	require.NoError(t, err)
	newer := time.Date(2026, time.July, 15, 20, 0, 0, 0, time.UTC)
	older := newer.Add(-time.Minute)

	// When
	require.NoError(t, lifecycle.TouchLastUsed(ctx, created.Credential.OrgID, created.Credential.CredentialID, "203.0.113.10", "newer-agent", newer))
	require.NoError(t, lifecycle.TouchLastUsed(ctx, created.Credential.OrgID, created.Credential.CredentialID, "203.0.113.11", "older-agent", older))

	// Then
	var (
		usedAt    time.Time
		usedIP    sql.NullString
		userAgent sql.NullString
	)
	err = db.QueryRowContext(ctx, `
	SELECT last_used_at, host(last_used_ip), last_used_user_agent
	FROM acr.client_credentials
	WHERE credential_id = $1`, created.Credential.CredentialID).Scan(&usedAt, &usedIP, &userAgent)
	require.NoError(t, err)
	require.True(t, usedAt.Equal(newer))
	require.Equal(t, "203.0.113.10", usedIP.String)
	require.Equal(t, "newer-agent", userAgent.String)
}

func TestCredentialStore_TouchLastUsed_returnsNotFound_whenCredentialDoesNotExist(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	_, lifecycle, err := newCredentialStore(db, audit, time.Now)
	require.NoError(t, err)

	// When
	err = lifecycle.TouchLastUsed(ctx, "00000000-0000-4000-8000-0000000000aa", "credential-does-not-exist", "203.0.113.10", "agent", time.Now())

	// Then
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestCredentialStore_TouchLastUsed_redactsDatabaseCastErrors(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	store, lifecycle, err := newCredentialStore(db, audit, time.Now)
	require.NoError(t, err)
	service, err := auth.NewService(lifecycle, auth.ServiceOptions{})
	require.NoError(t, err)
	created, err := service.Create(ctx, credentialCreateRequest("cast-error"))
	require.NoError(t, err)
	invalidIP := "not-an-ip"

	// When
	err = store.TouchLastUsed(ctx, created.Credential.OrgID, created.Credential.CredentialID, invalidIP, "agent", time.Now())

	// Then
	require.ErrorIs(t, err, storage.ErrUnavailable)
	require.False(t, errors.Is(err, storage.ErrInvalidCredentialInput))
	require.NotContains(t, err.Error(), invalidIP)
	require.NotContains(t, err.Error(), "invalid input syntax")
}

func TestCredentialStore_TouchLastUsed_leavesCredentialOfAnotherOrganizationUnchanged(t *testing.T) {
	// Given
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	_, lifecycle, err := newCredentialStore(db, audit, time.Now)
	require.NoError(t, err)
	service, err := auth.NewService(lifecycle, auth.ServiceOptions{})
	require.NoError(t, err)
	created, err := service.Create(ctx, credentialCreateRequest("other-org"))
	require.NoError(t, err)

	// When
	err = lifecycle.TouchLastUsed(ctx, "22222222-2222-2222-2222-222222222222", created.Credential.CredentialID, "203.0.113.10", "agent", time.Now())

	// Then
	require.ErrorIs(t, err, storage.ErrNotFound)
	var usedAt sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx, `SELECT last_used_at FROM acr.client_credentials WHERE credential_id = $1`, created.Credential.CredentialID).Scan(&usedAt))
	require.False(t, usedAt.Valid)
}
