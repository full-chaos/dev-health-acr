package postgres

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The purge statements and the remaining-eligible probe run every tick on
// tables that unauthenticated registration and /authorize grow. A LIMIT caps
// the rows a statement returns, not the rows it reads, so their cost stays flat
// only if each reads its table through an index in the order it needs. This
// test seeds 100,000 rows that are NOT eligible (registered / requested inside
// the windows) and asserts, from EXPLAIN (ANALYZE, BUFFERS), that no statement
// scans either table and that each reads a small, fixed number of buffers.

type explainNode struct {
	NodeType     string        `json:"Node Type"`
	RelationName string        `json:"Relation Name"`
	ActualLoops  float64       `json:"Actual Loops"`
	SharedHit    float64       `json:"Shared Hit Blocks"`
	SharedRead   float64       `json:"Shared Read Blocks"`
	Plans        []explainNode `json:"Plans"`
}

func (n explainNode) walk(visit func(explainNode)) {
	visit(n)
	for _, child := range n.Plans {
		child.walk(visit)
	}
}

// explainStatement runs EXPLAIN (ANALYZE, BUFFERS) of query inside a
// transaction it rolls back (the statements lock and delete), and returns the
// plan tree and the most buffers any node of it touched. A node's counters
// include its children's, and a CTE hangs off the root as a child too, so the
// largest is the whole statement's.
func (f *oauthPurgeFixture) explainStatement(query string, args ...any) (explainNode, int) {
	f.t.Helper()
	tx, err := f.db.BeginTx(f.ctx, nil)
	require.NoError(f.t, err)
	defer func() { _ = tx.Rollback() }()
	var raw []byte
	require.NoError(f.t, tx.QueryRowContext(f.ctx, `EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF, FORMAT JSON) `+query, args...).Scan(&raw))
	var documents []struct {
		Plan explainNode `json:"Plan"`
	}
	require.NoError(f.t, json.Unmarshal(raw, &documents))
	require.Len(f.t, documents, 1)
	buffers := 0
	documents[0].Plan.walk(func(node explainNode) {
		buffers = max(buffers, int(node.SharedHit+node.SharedRead))
	})
	return documents[0].Plan, buffers
}

func TestOAuthPurgeStatements_readABoundedNumberOfBuffers(t *testing.T) {
	// Given: 100,000 young clients, and 100,000 young requests (each on its own
	// device authorization), none of them eligible at purgeAt
	f := newOAuthPurgeFixture(t)
	purgeAt := f.t0.Add(100 * 24 * time.Hour)
	young := purgeAt.Add(-24 * time.Hour)
	const seeded = 100000
	_, err := f.db.ExecContext(f.ctx, `
INSERT INTO acr.oauth_clients (client_id, client_name, redirect_uris, created_at)
SELECT 'acrc_' || lpad(to_hex(i), 32, '0'), 'plan', '[]'::jsonb, $1::timestamptz FROM generate_series(1, $2::int) i`, young, seeded)
	require.NoError(t, err)
	_, err = f.db.ExecContext(f.ctx, `
INSERT INTO acr.device_authorizations (device_code_hash, user_code_hash, state, created_at, expires_at, poll_interval_seconds, issuance_provenance)
SELECT lpad(to_hex(i), 64, '0'), lpad(to_hex(i + 1000000), 64, '0'), 'pending', $1::timestamptz, $1::timestamptz + interval '10 minutes', 5, 'device_authorization'
FROM generate_series(1, $2::int) i`, young, seeded)
	require.NoError(t, err)
	_, err = f.db.ExecContext(f.ctx, `
INSERT INTO acr.oauth_authorization_requests (handle_hash, device_code_hash, client_id, client_kind, redirect_uri, code_challenge, resource, state, created_at, expires_at)
SELECT lpad(to_hex(i + 2000000), 64, '0'), lpad(to_hex(i), 64, '0'), 'acrc_' || lpad(to_hex(i), 32, '0'), 'dynamic', 'https://example.com/cb', repeat('A', 43), 'https://example.com/r', 's',
       $1::timestamptz, $1::timestamptz + interval '10 minutes'
FROM generate_series(1, $2::int) i`, young, seeded)
	require.NoError(t, err)
	_, err = f.db.ExecContext(f.ctx, `ANALYZE acr.oauth_clients; ANALYZE acr.oauth_authorization_requests; ANALYZE acr.device_authorizations`)
	require.NoError(t, err)
	require.Equal(t, seeded, f.count("acr.oauth_clients"))
	require.Equal(t, seeded, f.count("acr.oauth_authorization_requests"))
	require.Equal(t, seeded, f.count("acr.device_authorizations"))

	clientCutoff := purgeAt.Add(-oauthPurgeIdle)
	requestCutoff := purgeAt.Add(-oauthPurgeGrace)
	ids := make([]string, 500)
	for i := range ids {
		ids[i] = "acrc_00000000000000000000000000000000"
	}
	deviceIDs := make([]string, 500)
	for i := range deviceIDs {
		deviceIDs[i] = "0000000000000000000000000000000000000000000000000000000000000000"
	}
	statements := []struct {
		name  string
		query string
		args  []any
	}{
		{"idle client candidates (purge select)", selectIdleOAuthClientsSQL, []any{clientCutoff, 500, purgeAt}},
		{"idle client recheck (purge delete)", deleteIdleOAuthClientsSQL, []any{clientCutoff, ids, purgeAt}},
		{"idle client remaining probe", countIdleOAuthClientsSQL, []any{clientCutoff, 501, purgeAt}},
		{"expired request purge", purgeExpiredOAuthRequestsSQL, []any{requestCutoff, 500, purgeAt}},
		{"expired request remaining probe", countExpiredOAuthRequestsSQL, []any{requestCutoff, 501, purgeAt}},
		{"expired device authorization candidates (purge select)", selectExpiredDeviceAuthorizationsSQL, []any{requestCutoff, 500, purgeAt}},
		{"expired device authorization recheck (purge delete)", deleteExpiredDeviceAuthorizationsSQL, []any{requestCutoff, deviceIDs, purgeAt}},
		{"expired device authorization remaining probe", countExpiredDeviceAuthorizationsSQL, []any{requestCutoff, 501, purgeAt}},
	}

	// Then: none scans a seeded table, and each reads a bounded number of buffers
	// (a scan of either table reads on the order of a thousand)
	const bufferBound = 250
	for _, statement := range statements {
		plan, buffers := f.explainStatement(statement.query, statement.args...)
		plan.walk(func(node explainNode) {
			// A scan the plan never executed (the inner side of an anti join
			// that no outer row reached) reads nothing.
			if node.NodeType == "Seq Scan" && node.ActualLoops > 0 {
				require.NotContainsf(t, []string{"oauth_clients", "oauth_authorization_requests", "device_authorizations"}, node.RelationName,
					"%s: scans %s instead of reading it through an index", statement.name, node.RelationName)
			}
		})
		require.LessOrEqualf(t, buffers, bufferBound, "%s: read %d buffers over %d seeded rows", statement.name, buffers, seeded)
	}
}
