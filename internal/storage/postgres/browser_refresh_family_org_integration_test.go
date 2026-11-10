package postgres

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

type browserRefreshFamilyOrgReader interface {
	BrowserRefreshFamilyOrg(ctx context.Context, presentedHash, clientID string) (string, error)
}

// The refresh grant reads a family's organization before it rotates: a read
// that takes no lock and writes nothing, so the token stays unconsumed.
func TestBrowserRefresh_postgresFamilyOrgIsAReadOnlyLookup(t *testing.T) {
	ctx := context.Background()
	fixture := newRefreshFixture(t, ctx, newCredentialStoreDatabase(t, ctx))
	familyID, token := fixture.startFamily(t, ctx, "o1")
	reader, ok := any(fixture.refresh).(browserRefreshFamilyOrgReader)
	require.True(t, ok, "the postgres refresh store has no BrowserRefreshFamilyOrg")
	var familyOrg string
	require.NoError(t, fixture.db.QueryRowContext(ctx, `SELECT org_id::text FROM acr.browser_token_families WHERE family_id = $1`, familyID).Scan(&familyOrg))

	org, err := reader.BrowserRefreshFamilyOrg(ctx, auth.HashRefreshToken(token), auth.DevBrowserClientID)
	require.NoError(t, err)
	require.Equal(t, familyOrg, org)

	_, err = reader.BrowserRefreshFamilyOrg(ctx, auth.HashRefreshToken(auth.RefreshTokenPrefix+"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), auth.DevBrowserClientID)
	require.ErrorIs(t, err, storage.ErrNotFound)
	_, err = reader.BrowserRefreshFamilyOrg(ctx, auth.HashRefreshToken(token), "https://other.example.test/acr/client.json")
	require.ErrorIs(t, err, storage.ErrNotFound)

	rotated, err := fixture.service.Refresh(ctx, token, auth.DevBrowserClientID)
	require.NoError(t, err)
	require.Equal(t, storage.BrowserRefreshRotated, rotated.Decision, "the read consumed the token")
	org, err = reader.BrowserRefreshFamilyOrg(ctx, auth.HashRefreshToken(rotated.RefreshToken), auth.DevBrowserClientID)
	require.NoError(t, err)
	require.Equal(t, familyOrg, org)
}
