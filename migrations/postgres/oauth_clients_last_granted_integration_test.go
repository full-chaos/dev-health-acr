package postgres

import (
	"context"
	"database/sql"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// CHAOS-12320: 0067 adds acr.oauth_clients.last_granted_at and backfills it
// from every grant that left a row, so no client granted before the roll is
// purged by the new rule.

const oauthClientsLastGrantedMigration = "0067_oauth_clients_last_granted.sql"

func TestMigration0067BoundsItsLockWaitFirst(t *testing.T) {
	t.Parallel()
	body, err := fs.ReadFile(Files, oauthClientsLastGrantedMigration)
	require.NoError(t, err)
	var statements []string
	for _, line := range strings.Split(string(body), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			statements = append(statements, trimmed)
		}
	}
	require.NotEmpty(t, statements)
	require.Equal(t, "SET LOCAL lock_timeout = '5s';", statements[0], "the lock bound must come before the ALTER that takes the lock")
	require.Equal(t, 1, strings.Count(string(body), "ALTER TABLE"), "one ALTER, so the lock is taken once")
}

func TestRunner_upgradeTo67BackfillsGrantedClients(t *testing.T) {
	ctx := context.Background()
	db := newTestDatabase(t, ctx)
	require.NoError(t, chainBefore(t, oauthClientsLastGrantedMigration).Up(ctx, db))

	const org = "70d529e0-0000-4000-8000-000000012320"
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	byCredential := "acrc_" + strings.Repeat("a1", 16)
	byRequest := "acrc_" + strings.Repeat("b2", 16)
	byGrant := "acrc_" + strings.Repeat("c3", 16)
	neverGranted := "acrc_" + strings.Repeat("d4", 16)
	for _, id := range []string{byCredential, byRequest, byGrant, neverGranted} {
		_, err := db.ExecContext(ctx, `INSERT INTO acr.oauth_clients (client_id, redirect_uris, created_at) VALUES ($1, '["http://127.0.0.1:1/cb"]', $2)`, id, t0)
		require.NoError(t, err)
	}
	credential := func(id, clientID string, createdAt time.Time) {
		t.Helper()
		var class, approver, resource, client any
		if clientID != "" {
			class, approver, resource, client = "oauth_user", "user-12320", "https://mcp.example.test/mcp", clientID
		}
		_, err := db.ExecContext(ctx, `INSERT INTO acr.client_credentials (credential_id, org_id, name, token_prefix, token_hash, scopes, created_at, client_class, approving_user_id, resource, client_id)
			VALUES ($1, $2, 'backfill', 'fcacr_bf67', 'hash-'||$1::text, '["context:read"]', $3, $4, $5, $6, $7)`, id, org, createdAt, class, approver, resource, client)
		require.NoError(t, err)
	}
	redeemedDevice := func(seed, credentialID string, redeemedAt time.Time) string {
		t.Helper()
		device := strings.Repeat(seed, 64)
		created := redeemedAt.Add(-time.Minute)
		_, err := db.ExecContext(ctx, `INSERT INTO acr.device_authorizations
			(device_code_hash, user_code_hash, state, created_at, expires_at, poll_interval_seconds, issuance_provenance, approved_at, redeemed_at, redeemed_credential_id)
			VALUES ($1, $2, 'redeemed', $3, $3 + INTERVAL '10 minutes', 5, 'device_authorization', $3, $4, $5)`,
			device, strings.Repeat(seed, 63)+"0", created, redeemedAt, credentialID)
		require.NoError(t, err)
		return device
	}
	request := func(handleSeed, device, clientID string, createdAt time.Time) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO acr.oauth_authorization_requests
			(handle_hash, device_code_hash, client_id, client_kind, redirect_uri, code_challenge, resource, created_at, expires_at)
			VALUES ($1, $2, $3, 'dynamic', 'http://127.0.0.1:1/cb', $4, 'https://mcp.example.test/mcp', $5, $5 + INTERVAL '10 minutes')`,
			strings.Repeat(handleSeed, 64), device, clientID, strings.Repeat("A", 43), createdAt)
		require.NoError(t, err)
	}

	// byCredential: an OAuth credential names it (the later of two counts).
	credential("cred-67-a-old", byCredential, t0.Add(time.Hour))
	credential("cred-67-a-new", byCredential, t0.Add(5*time.Hour))
	// byRequest: a legacy credential without client_id, reached through the
	// redeemed device authorization behind the client's request.
	credential("cred-67-b", "", t0)
	request("1", redeemedDevice("2", "cred-67-b", t0.Add(2*time.Hour)), byRequest, t0.Add(2*time.Hour))
	// byGrant: a device grant's redeemed authorization is later than its
	// credential row's created_at.
	credential("cred-67-c", byGrant, t0.Add(time.Hour))
	device := redeemedDevice("3", "cred-67-c", t0.Add(3*time.Hour))
	_, err := db.ExecContext(ctx, `INSERT INTO acr.oauth_device_grants (device_code_hash, client_id, client_kind, resource, scope, created_at, expires_at)
		VALUES ($1, $2, 'dynamic', 'https://mcp.example.test/mcp', 'context:read', $3, $3 + INTERVAL '10 minutes')`, device, byGrant, t0.Add(3*time.Hour))
	require.NoError(t, err)
	// neverGranted: a request whose authorization was never redeemed.
	pending := strings.Repeat("4", 64)
	_, err = db.ExecContext(ctx, `INSERT INTO acr.device_authorizations
		(device_code_hash, user_code_hash, state, created_at, expires_at, poll_interval_seconds, issuance_provenance)
		VALUES ($1, $2, 'pending', $3, $3 + INTERVAL '10 minutes', 5, 'device_authorization')`, pending, strings.Repeat("5", 64), t0)
	require.NoError(t, err)
	request("6", pending, neverGranted, t0)

	runner, err := Embedded()
	require.NoError(t, err)
	require.NoError(t, runner.Up(ctx, db))

	lastGranted := func(id string) sql.NullTime {
		t.Helper()
		var at sql.NullTime
		require.NoError(t, db.QueryRowContext(ctx, `SELECT last_granted_at FROM acr.oauth_clients WHERE client_id = $1`, id).Scan(&at))
		return at
	}
	for id, want := range map[string]time.Time{byCredential: t0.Add(5 * time.Hour), byRequest: t0.Add(2 * time.Hour), byGrant: t0.Add(3 * time.Hour)} {
		got := lastGranted(id)
		require.Truef(t, got.Valid && got.Time.Equal(want), "%s: want last_granted_at %s, got %+v", id, want, got)
	}
	require.False(t, lastGranted(neverGranted).Valid, "a client that never completed a grant stays NULL and keeps the 30-day purge")
}
