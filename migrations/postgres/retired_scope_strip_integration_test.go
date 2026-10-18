package postgres

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestRunner_upgradeTo54StripsTheRetiredContextAdminScope(t *testing.T) {
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	pre := fstest.MapFS{}
	entries, err := fs.ReadDir(Files, ".")
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") || name >= "0054_" {
			continue
		}
		body, readErr := fs.ReadFile(Files, name)
		require.NoError(t, readErr)
		pre[name] = &fstest.MapFile{Data: body}
	}
	require.Len(t, pre, 53, "every migration before 0054")
	released, err := NewRunner(pre)
	require.NoError(t, err)
	require.NoError(t, released.Up(ctx, db))

	const orgID = "70d529e0-0000-4000-8000-000000000001"
	const deviceHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const requestDeviceHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const userHash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const requestUserHash = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	_, err = db.ExecContext(ctx, `INSERT INTO acr.client_credentials (credential_id, org_id, name, token_prefix, token_hash, scopes, created_at)
		VALUES ('cred-with-admin', $1, 'legacy', 'fcacr_aaaa', 'hash-1', '["context:read","context:admin","evidence:read"]', now()),
		       ('cred-without-admin', $1, 'current', 'fcacr_bbbb', 'hash-2', '["context:read"]', now()),
		       ('cred-only-admin', $1, 'admin only', 'fcacr_cccc', 'hash-3', '["context:admin"]', now())`, orgID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO acr.device_authorizations (device_code_hash, user_code_hash, state, authorized_scopes, created_at, expires_at, poll_interval_seconds, issuance_provenance)
		VALUES ($1, $2, 'pending', '["context:admin","context:read"]', timestamptz '2026-10-18 12:00:00+00', timestamptz '2026-10-18 12:10:00+00', 5, 'device_authorization'),
		       ($3, $4, 'pending', '["context:read"]', timestamptz '2026-10-18 12:00:00+00', timestamptz '2026-10-18 12:10:00+00', 5, 'device_authorization')`,
		deviceHash, userHash, requestDeviceHash, requestUserHash)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO acr.oauth_device_grants (device_code_hash, client_id, client_kind, resource, scope, created_at, expires_at)
		VALUES ($1, 'acrc_00000000000000000000000000000000', 'dynamic', 'https://example.com/mcp', 'context:read context:admin evidence:read', now(), now() + interval '1 hour')`, deviceHash)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO acr.oauth_authorization_requests (handle_hash, device_code_hash, client_id, client_kind, redirect_uri, code_challenge, resource, scope, created_at, expires_at)
		VALUES ($1, $2, 'acrc_00000000000000000000000000000000', 'dynamic', 'https://example.com/cb', $3, 'https://example.com/mcp', 'context:admin', now(), now() + interval '1 hour')`,
		strings.Repeat("e", 64), requestDeviceHash, strings.Repeat("A", 43))
	require.NoError(t, err)

	latest, err := Embedded()
	require.NoError(t, err)
	require.NoError(t, latest.Up(ctx, db))
	require.Equal(t, expectedMigrationVersions, migrationVersions(t, ctx, latest, db))

	scalar := func(query string, args ...any) string {
		var value string
		require.NoError(t, db.QueryRowContext(ctx, query, args...).Scan(&value))
		return value
	}
	require.JSONEq(t, `["context:read","evidence:read"]`, scalar(`SELECT scopes::text FROM acr.client_credentials WHERE credential_id = 'cred-with-admin'`))
	require.JSONEq(t, `["context:read"]`, scalar(`SELECT scopes::text FROM acr.client_credentials WHERE credential_id = 'cred-without-admin'`))
	require.JSONEq(t, `[]`, scalar(`SELECT scopes::text FROM acr.client_credentials WHERE credential_id = 'cred-only-admin'`), "a credential whose only scope was the retired one is left with an empty scope set; self-rotate and self-revoke refuse it with the invalid-credential error")
	require.JSONEq(t, `["context:read"]`, scalar(`SELECT authorized_scopes::text FROM acr.device_authorizations WHERE device_code_hash = $1`, deviceHash))
	require.Equal(t, "context:read evidence:read", scalar(`SELECT scope FROM acr.oauth_device_grants WHERE device_code_hash = $1`, deviceHash))
	require.Equal(t, "", scalar(`SELECT scope FROM acr.oauth_authorization_requests WHERE device_code_hash = $1`, requestDeviceHash))

	for _, table := range []struct{ name, column string }{
		{"client_credentials", "scopes::text"},
		{"device_authorizations", "authorized_scopes::text"},
		{"oauth_device_grants", "scope"},
		{"oauth_authorization_requests", "scope"},
	} {
		require.Equal(t, "0", scalar(`SELECT count(*)::text FROM acr.`+table.name+` WHERE `+table.column+` LIKE '%context:admin%'`), table.name)
	}
}
