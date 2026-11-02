package postgres

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Real-database cells of the lifecycle outbox reaper. Lead-run.

type reapFixture struct {
	outboxFixture
	reaper *OutboxReaper
	now    time.Time
}

func newReapFixture(t *testing.T) reapFixture {
	t.Helper()
	f := newOutboxFixture(t)
	reaper, err := NewOutboxReaper(f.db)
	require.NoError(t, err)
	return reapFixture{outboxFixture: f, reaper: reaper, now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
}

// add inserts one outbox row; publishedAgo < 0 leaves it unpublished.
func (f reapFixture) add(t *testing.T, aggregate string, revision int, eventType string, publishedAgo time.Duration) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"aggregate_id": aggregate, "payload": map[string]any{"revision": revision}})
	require.NoError(t, err)
	var published any
	if publishedAgo >= 0 {
		published = f.now.Add(-publishedAgo)
	}
	_, err = f.db.ExecContext(f.ctx, `INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at, published_at)
VALUES (gen_random_uuid(), $1::uuid, $2, $3, $4, $5::jsonb, $6, $7)`, credentialTestOrgID, aggregate, revision, eventType, string(payload), f.now.Add(-60*24*time.Hour), published)
	require.NoError(t, err)
}

func (f reapFixture) remaining(t *testing.T, aggregate string) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE aggregate_id = $1`, aggregate)
}

func (f reapFixture) reap(t *testing.T, limit int) ReapResult {
	t.Helper()
	result, err := f.reaper.ReapPublished(f.ctx, f.now.Add(-7*24*time.Hour), limit)
	require.NoError(t, err)
	return result
}

const (
	issued  = "acr_credential.issued"
	revoked = "acr_credential.revoked"
	expired = "acr_credential.expired"
	day     = 24 * time.Hour
)

func TestOutboxReap_ReapsAPublishedAgedAggregateWhoseCredentialIsGone(t *testing.T) {
	f := newReapFixture(t)
	f.add(t, "cred_rp_gone", 1, issued, 9*day)
	f.add(t, "cred_rp_gone", 2, revoked, 8*day)
	result := f.reap(t, 100)
	require.Equal(t, ReapResult{Aggregates: 1, Rows: 2}, result, "a published aged aggregate with no credential row is reaped")
	require.Zero(t, f.remaining(t, "cred_rp_gone"), "every revision of the reaped aggregate goes")
}

func TestOutboxReap_KeepsTheTerminalRowsWhileTheCredentialRowExists(t *testing.T) {
	f := newReapFixture(t)
	insertTickCredential(t, f.db, "cred_rp_alive", "NULL", "NULL")
	f.add(t, "cred_rp_alive", 1, issued, 40*day)
	f.add(t, "cred_rp_alive", 2, expired, 30*day)
	result := f.reap(t, 100)
	require.Zero(t, result.Rows, "a terminal aggregate stays while its credential row exists")
	require.Equal(t, 2, f.remaining(t, "cred_rp_alive"), "the retention gate reads the expired row of a credential that still exists")
}

func TestOutboxReap_ReapsANonTerminalTailOnceTheCredentialIsGone(t *testing.T) {
	f := newReapFixture(t)
	f.add(t, "cred_rp_tail", 1, issued, 9*day)
	result := f.reap(t, 100)
	require.Equal(t, 1, result.Aggregates, "a non-terminal aggregate is reaped once published, aged and its credential is gone")
	require.Zero(t, f.remaining(t, "cred_rp_tail"))
}

func TestOutboxReap_NeverDeletesAnAggregateWithAnUnpublishedRow(t *testing.T) {
	f := newReapFixture(t)
	f.add(t, "cred_rp_unpub", 1, issued, 30*day)
	f.add(t, "cred_rp_unpub", 2, revoked, -1)
	result := f.reap(t, 100)
	require.Zero(t, result.Rows, "an aggregate with an unpublished row is never reaped")
	require.Equal(t, 2, f.remaining(t, "cred_rp_unpub"))
}

func TestOutboxReap_NeverDeletesPartOfAnAggregate(t *testing.T) {
	f := newReapFixture(t)
	f.add(t, "cred_rp_part", 1, issued, 30*day)
	f.add(t, "cred_rp_part", 2, revoked, 1*day)
	result := f.reap(t, 100)
	require.Zero(t, result.Rows, "one row newer than the cutoff keeps the whole aggregate")
	require.Equal(t, 2, f.remaining(t, "cred_rp_part"), "an old revision never goes alone: MaxRevision and the terminal rule read the outbox")
}

func TestOutboxReap_ARowPublishedExactlyAtTheCutoffIsKept(t *testing.T) {
	f := newReapFixture(t)
	f.add(t, "cred_rp_edge", 1, issued, 7*day)
	require.Zero(t, f.reap(t, 100).Rows, "a row published exactly at the cutoff is kept")
	_, err := f.db.ExecContext(f.ctx, `UPDATE acr.credential_lifecycle_outbox SET published_at = published_at - interval '1 second' WHERE aggregate_id = 'cred_rp_edge'`)
	require.NoError(t, err)
	require.Equal(t, 1, f.reap(t, 100).Rows, "one second older than the cutoff is reaped")
}

func TestOutboxReap_TheBatchIsBoundedOldestFirstAndASecondRunFindsTheRest(t *testing.T) {
	f := newReapFixture(t)
	for i, age := range []time.Duration{20 * day, 10 * day, 30 * day, 9 * day, 8 * day} {
		f.add(t, fmt.Sprintf("cred_rp_b%d", i), 1, issued, age)
	}
	first := f.reap(t, 2)
	require.Equal(t, 2, first.Aggregates, "the batch is bounded by its limit")
	require.Zero(t, f.remaining(t, "cred_rp_b2"), "the oldest published aggregate goes first")
	require.Zero(t, f.remaining(t, "cred_rp_b0"), "the second oldest goes second")
	require.Equal(t, 1, f.remaining(t, "cred_rp_b1"), "a newer aggregate waits for the next run")
	require.Equal(t, 2, f.reap(t, 2).Aggregates, "the next run takes the next two")
	require.Equal(t, 1, f.reap(t, 2).Aggregates, "then the last one")
	require.Zero(t, f.reap(t, 2).Aggregates, "a run with nothing left reaps nothing")
}

func TestOutboxReap_APurgeRevokedRowBlocksTheReapUntilItIsPublished(t *testing.T) {
	f := newReapFixture(t)
	f.add(t, "cred_rp_purge", 1, issued, 30*day)
	f.add(t, "cred_rp_purge", 2, revoked, -1)
	require.Zero(t, f.reap(t, 100).Rows, "the unpublished revoked row of a purge is kept")
	_, err := f.db.ExecContext(f.ctx, `UPDATE acr.credential_lifecycle_outbox SET published_at = $1 WHERE aggregate_id = 'cred_rp_purge' AND revision = 2`, f.now.Add(-8*day))
	require.NoError(t, err)
	require.Equal(t, 2, f.reap(t, 100).Rows, "once published and aged the whole aggregate goes")
}

func TestOutboxReap_OtherAggregatesAreUntouched(t *testing.T) {
	f := newReapFixture(t)
	insertTickCredential(t, f.db, "cred_rp_keep", "NULL", "NULL")
	f.add(t, "cred_rp_keep", 1, issued, 30*day)
	f.add(t, "cred_rp_drop", 1, issued, 30*day)
	f.add(t, "cred_rp_new", 1, issued, 1*day)
	result := f.reap(t, 100)
	require.Equal(t, ReapResult{Aggregates: 1, Rows: 1}, result, "only the aged aggregate with no credential row goes")
	require.Equal(t, 1, f.remaining(t, "cred_rp_keep"), "an aggregate whose credential exists is untouched")
	require.Equal(t, 1, f.remaining(t, "cred_rp_new"), "an aggregate inside retention is untouched")
}

// planNodes walks an EXPLAIN (FORMAT JSON) document.
func planNodes(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var doc []map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &doc))
	var nodes []map[string]any
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		nodes = append(nodes, node)
		if children, ok := node["Plans"].([]any); ok {
			for _, child := range children {
				walk(child.(map[string]any))
			}
		}
	}
	plan, ok := doc[0]["Plan"].(map[string]any)
	require.True(t, ok, "an EXPLAIN plan")
	walk(plan)
	return nodes
}

func (f reapFixture) explain(t *testing.T, statement string, args ...any) (string, []map[string]any) {
	t.Helper()
	var raw string
	require.NoError(t, f.db.QueryRowContext(f.ctx, `EXPLAIN (FORMAT JSON) `+statement, args...).Scan(&raw))
	return raw, planNodes(t, raw)
}

// TestOutboxReap_ExplainPlans seeds a backlog the size of a busy deployment, analyzes the tables and reads the plans of
// the claim and the reaper: the claim must stay on the partial index of unpublished rows, and the reaper's credential
// check must be an anti join, never a per-aggregate sequential scan of client_credentials.
func TestOutboxReap_ExplainPlans(t *testing.T) {
	f := newReapFixture(t)
	_, err := f.db.ExecContext(f.ctx, `
INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at, published_at)
SELECT gen_random_uuid(), $1::uuid, 'cred_ex_' || g, r, CASE WHEN r = 1 THEN 'acr_credential.issued' ELSE 'acr_credential.revoked' END, '{}'::jsonb,
       $2::timestamptz - (g || ' seconds')::interval - interval '60 days',
       CASE WHEN g % 100 = 0 AND r = 2 THEN NULL ELSE $2::timestamptz - interval '30 days' END
FROM generate_series(1, 20000) g, generate_series(1, 2) r`, credentialTestOrgID, f.now)
	require.NoError(t, err)
	_, err = f.db.ExecContext(f.ctx, `
INSERT INTO acr.client_credentials (credential_id, org_id, name, token_prefix, token_hash, scopes, repository_scopes, created_at, client_class)
SELECT 'cred_ex_' || g, $1::uuid, 'explain cell', 'fcacr_explaincell', 'hash-ex-' || g, '["context:read"]'::jsonb, '["acme/widgets"]'::jsonb, $2::timestamptz, 'org_bearer'
FROM generate_series(1, 20000, 7) g`, credentialTestOrgID, f.now)
	require.NoError(t, err)
	_, err = f.db.ExecContext(f.ctx, `ANALYZE acr.credential_lifecycle_outbox; ANALYZE acr.client_credentials`)
	require.NoError(t, err)

	claimRaw, claim := f.explain(t, LifecycleOutboxClaimSQL, []string{})
	t.Logf("claim plan: %s", claimRaw)
	var usesPartialIndex, seqScansOutbox bool
	for _, node := range claim {
		if node["Index Name"] == "ix_acr_credential_lifecycle_outbox_unpublished" {
			usesPartialIndex = true
		}
		if node["Node Type"] == "Seq Scan" && node["Relation Name"] == "credential_lifecycle_outbox" {
			seqScansOutbox = true
		}
	}
	require.True(t, usesPartialIndex, "the claim reads the partial index of unpublished rows")
	require.False(t, seqScansOutbox, "the claim never scans the whole outbox")

	reapRaw, reap := f.explain(t, LifecycleOutboxReapSQL, f.now.Add(-7*24*time.Hour), 100)
	t.Logf("reap plan: %s", reapRaw)
	var antiJoin, perGroupSeqScan bool
	for _, node := range reap {
		if nodeType, _ := node["Node Type"].(string); strings.Contains(nodeType, "Join") && node["Join Type"] == "Anti" {
			antiJoin = true
		}
		if node["Node Type"] == "Seq Scan" && node["Relation Name"] == "client_credentials" && node["Parent Relationship"] == "SubPlan" {
			perGroupSeqScan = true
		}
	}
	require.True(t, antiJoin, "the reaper checks the credential row with an anti join")
	require.False(t, perGroupSeqScan, "no per-aggregate sequential scan of client_credentials")
	tx, err := f.db.BeginTx(f.ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var execution string
	rows, err := tx.QueryContext(f.ctx, `EXPLAIN (ANALYZE, FORMAT TEXT, TIMING OFF) `+LifecycleOutboxReapSQL, f.now.Add(-7*24*time.Hour), 100)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		if strings.Contains(line, "Execution Time") {
			execution = line
		}
	}
	t.Logf("reap executed over %d outbox rows in a rolled-back transaction: %s (read it: a full-outbox HAVING read that is too slow needs an index, which is a migration)", 40000, execution)
}
