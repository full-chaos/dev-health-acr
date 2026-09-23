package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// A real unique violation on the OAuth create/issue paths must stay
// storage.ErrConflict AND keep its DependencyErrorClass, so the API's
// dependency-failure log can name SQLSTATE 23505 instead of a bare
// "oauth_dependency". RED before: the store replaced the classified
// error with bare storage.ErrConflict.
func TestOAuthStore_uniqueViolationsKeepTheirErrorClass(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	db := newCredentialStoreDatabase(t, ctx)
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	deviceStore, err := NewDeviceAuthorizationStoreWithOptions(db, audit, DeviceAuthorizationStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)
	store, err := NewOAuthStoreWithOptions(db, OAuthStoreOptions{Now: func() time.Time { return now }})
	require.NoError(t, err)

	requireClass := func(t *testing.T, err error) {
		t.Helper()
		require.ErrorIs(t, err, storage.ErrConflict)
		var class *storage.DependencyErrorClass
		require.True(t, errors.As(err, &class), "conflict lost its DependencyErrorClass: %v", err)
		require.Equal(t, "23505", class.SQLState)
		require.Equal(t, "unique_violation", class.Class)
	}

	deviceA, err := deviceStore.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("conflict-class-a"), UserCodeHash: storage.HashUserCode("CONFLCTA"),
	})
	require.NoError(t, err)
	deviceB, err := deviceStore.Create(ctx, storage.DeviceAuthorizationCreateInput{
		DeviceCodeHash: storage.HashDeviceCode("conflict-class-b"), UserCodeHash: storage.HashUserCode("CONFLCTB"),
	})
	require.NoError(t, err)

	t.Run("CreateDeviceGrant", func(t *testing.T) {
		grant := validOAuthDeviceGrantPG(now, deviceA.DeviceCodeHash)
		_, err := store.CreateDeviceGrant(ctx, grant)
		require.NoError(t, err)
		_, err = store.CreateDeviceGrant(ctx, grant)
		requireClass(t, err)
	})
	t.Run("CreateAuthorizationRequest", func(t *testing.T) {
		request := validOAuthAuthorizationRequestPG(now, deviceA.DeviceCodeHash, "conflict-class-handle")
		_, err := store.CreateAuthorizationRequest(ctx, request)
		require.NoError(t, err)
		_, err = store.CreateAuthorizationRequest(ctx, request)
		requireClass(t, err)
	})
	t.Run("IssueAuthorizationCode", func(t *testing.T) {
		deviceC, err := deviceStore.Create(ctx, storage.DeviceAuthorizationCreateInput{
			DeviceCodeHash: storage.HashDeviceCode("conflict-class-c"), UserCodeHash: storage.HashUserCode("CONFLCTC"),
		})
		require.NoError(t, err)
		first, err := store.CreateAuthorizationRequest(ctx, validOAuthAuthorizationRequestPG(now, deviceB.DeviceCodeHash, "conflict-issue-1"))
		require.NoError(t, err)
		second, err := store.CreateAuthorizationRequest(ctx, validOAuthAuthorizationRequestPG(now, deviceC.DeviceCodeHash, "conflict-issue-2"))
		require.NoError(t, err)
		code := storage.HashOAuthSecret("conflict-shared-code")
		_, err = store.IssueAuthorizationCode(ctx, first.HandleHash, code, now.Add(storage.OAuthAuthorizationCodeTTL))
		require.NoError(t, err)
		_, err = store.IssueAuthorizationCode(ctx, second.HandleHash, code, now.Add(storage.OAuthAuthorizationCodeTTL))
		requireClass(t, err)
	})
}
