package orgpurge_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/orgpurge"
	"github.com/full-chaos/dev-health-acr/internal/storage/orgfence"
)

// Real-database cells for the org purge's credential lifecycle events (PR-1c).
// The purge takes the credential rows with one DELETE ... RETURNING and writes
// a revoked row for each returned row that was live, in the same transaction.

type purgeRow struct {
	Aggregate string
	Revision  int
	EventType string
	ActorType string
	ActorRef  string
}

func insertPurgeCredential(t *testing.T, db *sql.DB, org, id, extra string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
INSERT INTO acr.client_credentials (credential_id, org_id, name, token_prefix, token_hash, scopes, repository_scopes, created_at, `+columnsOf(extra)+`)
VALUES ($1, $2::uuid, 'purge cell', 'fcacr_purgecell', 'hash-' || $1, '["context:read"]'::jsonb, '["acme/widgets"]'::jsonb, now() - interval '2 days', `+valuesOf(extra)+`)`, id, org)
	require.NoError(t, err, id)
}

// extra is one of: "live", "revoked", "expired", "browser".
func columnsOf(extra string) string {
	switch extra {
	case "revoked":
		return "client_class, revoked_at"
	case "expired":
		return "client_class, expires_at"
	default:
		return "client_class, expires_at"
	}
}

func valuesOf(extra string) string {
	switch extra {
	case "revoked":
		return "'org_bearer', now() - interval '1 day'"
	case "expired":
		return "'org_bearer', now() - interval '1 hour'"
	default:
		return "'org_bearer', now() + interval '30 days'"
	}
}

func purgeRows(t *testing.T, db *sql.DB, aggregate string) []purgeRow {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
SELECT aggregate_id, revision, event_type, coalesce(payload->'payload'->>'actor_type', ''), coalesce(payload->'payload'->>'actor_ref', '')
FROM acr.credential_lifecycle_outbox WHERE aggregate_id = $1 ORDER BY revision`, aggregate)
	require.NoError(t, err)
	defer rows.Close()
	var out []purgeRow
	for rows.Next() {
		var row purgeRow
		require.NoError(t, rows.Scan(&row.Aggregate, &row.Revision, &row.EventType, &row.ActorType, &row.ActorRef))
		out = append(out, row)
	}
	require.NoError(t, rows.Err())
	return out
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(), query, args...).Scan(&n))
	return n
}

func TestPurgeLifecycle_EveryLiveCredentialGetsOneRevokedRowWithFirstTouchFirst(t *testing.T) {
	r := newRig(t)
	insertPurgeCredential(t, r.db, orgGone, "cred_pl_live1", "live")
	insertPurgeCredential(t, r.db, orgGone, "cred_pl_live2", "live")
	insertPurgeCredential(t, r.db, orgKept, "cred_pl_kept", "live")
	purgeOrg(t, r, orgGone)
	for _, id := range []string{"cred_pl_live1", "cred_pl_live2"} {
		require.Equal(t, []purgeRow{
			{id, 1, "acr_credential.issued", "system", ""},
			{id, 2, "acr_credential.revoked", "system", ""},
		}, purgeRows(t, r.db, id), id)
	}
	require.Empty(t, purgeRows(t, r.db, "cred_pl_kept"), "another organization is untouched")
	require.Zero(t, count(t, r.db, `SELECT count(*) FROM acr.client_credentials WHERE org_id = $1::uuid`, orgGone))
}

func TestPurgeLifecycle_TerminalAndExpiredCredentialsGetNoRow(t *testing.T) {
	r := newRig(t)
	insertPurgeCredential(t, r.db, orgGone, "cred_pl_revoked", "revoked")
	insertPurgeCredential(t, r.db, orgGone, "cred_pl_expired", "expired")
	purgeOrg(t, r, orgGone)
	require.Empty(t, purgeRows(t, r.db, "cred_pl_revoked"), "a terminal credential gets no event")
	require.Empty(t, purgeRows(t, r.db, "cred_pl_expired"), "an unrevoked expired credential gets no event: it stays non-live and non-terminal at the consumer")
}

func TestPurgeLifecycle_ALegacyLiveCredentialGetsRevisionOneThenTheEvent(t *testing.T) {
	r := newRig(t)
	insertPurgeCredential(t, r.db, orgGone, "cred_pl_legacy", "live")
	_, err := r.db.ExecContext(context.Background(), `DELETE FROM acr.credential_lifecycle_outbox`)
	require.NoError(t, err)
	purgeOrg(t, r, orgGone)
	rows := purgeRows(t, r.db, "cred_pl_legacy")
	require.Len(t, rows, 2)
	require.Equal(t, 1, rows[0].Revision)
	require.Equal(t, "acr_credential.issued", rows[0].EventType)
	require.Equal(t, 2, rows[1].Revision)
}

func TestPurgeLifecycle_OutboxRowsSurviveThePurgeAndASecondRunWritesNothing(t *testing.T) {
	r := newRig(t)
	insertPurgeCredential(t, r.db, orgGone, "cred_pl_survive", "live")
	// An outbox row of a credential long gone, and the live credential's own
	// revision 1 (written by hand: a credential inserted by SQL has none, and
	// the purge would otherwise add that first touch too).
	_, err := r.db.ExecContext(context.Background(), `
INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at)
VALUES (gen_random_uuid(), $1::uuid, 'cred_pl_old', 1, 'acr_credential.issued', '{}'::jsonb, now()),
       (gen_random_uuid(), $1::uuid, 'cred_pl_survive', 1, 'acr_credential.issued', '{}'::jsonb, now())`, orgGone)
	require.NoError(t, err)
	before := count(t, r.db, `SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE org_id = $1::uuid`, orgGone)
	require.Equal(t, 2, before)
	purgeOrg(t, r, orgGone)
	after := count(t, r.db, `SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE org_id = $1::uuid`, orgGone)
	require.Equal(t, before+1, after, "no outbox row is deleted; only the live credential's revoked row is added")
	require.Equal(t, 1, count(t, r.db, `SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE aggregate_id = 'cred_pl_old' AND revision = 1`), "the old row survives")
	require.Equal(t, []purgeRow{
		{"cred_pl_survive", 1, "acr_credential.issued", "", ""},
		{"cred_pl_survive", 2, "acr_credential.revoked", "system", ""},
	}, purgeRows(t, r.db, "cred_pl_survive"), "the hand-written revision 1 stays; the purge adds revision 2 only")
	purgeOrg(t, r, orgGone)
	require.Equal(t, after, count(t, r.db, `SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE org_id = $1::uuid`, orgGone), "a second run writes nothing")
}

func TestPurgeLifecycle_ADryRunCountsWithTheSameRuleAndWritesNothing(t *testing.T) {
	r := newRig(t)
	insertPurgeCredential(t, r.db, orgGone, "cred_pl_dry1", "live")
	insertPurgeCredential(t, r.db, orgGone, "cred_pl_dry2", "live")
	insertPurgeCredential(t, r.db, orgGone, "cred_pl_dry3", "revoked")
	insertPurgeCredential(t, r.db, orgGone, "cred_pl_dry4", "expired")
	before := count(t, r.db, `SELECT count(*) FROM acr.credential_lifecycle_outbox`)
	dry, err := r.run(t, orgGone, orgpurge.Options{DryRun: true})
	require.NoError(t, err)
	require.EqualValues(t, 2, dry.LifecycleEvents)
	require.Equal(t, before, count(t, r.db, `SELECT count(*) FROM acr.credential_lifecycle_outbox`), "a dry run writes no row")
	real, err := r.run(t, orgGone, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(orgGone)})
	require.NoError(t, err)
	require.Equal(t, dry.LifecycleEvents, real.LifecycleEvents, "the dry run and the real run count the same credentials")
}

func TestPurgeLifecycle_AFailedOutboxWriteRollsTheWholePurgeBack(t *testing.T) {
	r := newRig(t)
	insertPurgeCredential(t, r.db, orgGone, "cred_pl_rollback", "live")
	_, err := r.db.ExecContext(context.Background(), `
CREATE FUNCTION acr.refuse_outbox_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'outbox refused'; END $$;
CREATE TRIGGER trg_refuse_outbox_insert BEFORE INSERT ON acr.credential_lifecycle_outbox FOR EACH ROW EXECUTE FUNCTION acr.refuse_outbox_insert();`)
	require.NoError(t, err)
	_, err = r.run(t, orgGone, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(orgGone)})
	require.Error(t, err)
	require.Equal(t, 1, count(t, r.db, `SELECT count(*) FROM acr.client_credentials WHERE credential_id = 'cred_pl_rollback'`), "the DELETE rolled back with the failed outbox write")
	require.Zero(t, markerCountFor(t, r.db, orgGone), "no purge marker")
	require.Zero(t, count(t, r.db, `SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE aggregate_id = 'cred_pl_rollback'`))
}

func markerCountFor(t *testing.T, db *sql.DB, org string) int {
	return count(t, db, `SELECT count(*) FROM acr.context_fabric_org_purge_markers WHERE org_id = $1`, org)
}

// A revoke that commits while the purge's DELETE waits on its row lock is
// returned by the DELETE as the revoke committed it: terminal, so the purge
// writes nothing for it and exactly one revoked row exists, the revoke's.
func TestPurgeLifecycle_ARevokeCommittingWhileTheDeleteWaitsGivesExactlyOneRevokedRow(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	insertPurgeCredential(t, r.db, orgGone, "cred_pl_race", "live")
	revoker, err := r.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = revoker.Rollback() })
	_, err = revoker.ExecContext(ctx, `UPDATE acr.client_credentials SET revoked_at = now() WHERE credential_id = 'cred_pl_race'`)
	require.NoError(t, err)

	var wg sync.WaitGroup
	var purgeErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, purgeErr = r.run(t, orgGone, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(orgGone)})
	}()
	require.Eventually(t, func() bool {
		return count(t, r.db, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE 'DELETE FROM acr.client_credentials%'`) > 0
	}, 30*time.Second, 50*time.Millisecond, "the purge's DELETE must be waiting on the revoke's row lock")
	for _, statement := range []string{
		`INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at) VALUES (gen_random_uuid(), '` + orgGone + `'::uuid, 'cred_pl_race', 1, 'acr_credential.issued', '{}'::jsonb, now())`,
		`INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at) VALUES (gen_random_uuid(), '` + orgGone + `'::uuid, 'cred_pl_race', 2, 'acr_credential.revoked', '{"payload":{"actor_type":"user","actor_ref":"revoker"}}'::jsonb, now())`,
	} {
		_, err = revoker.ExecContext(ctx, statement)
		require.NoError(t, err)
	}
	require.NoError(t, revoker.Commit())
	wg.Wait()
	require.NoError(t, purgeErr)
	rows := purgeRows(t, r.db, "cred_pl_race")
	require.Len(t, rows, 2, "the revoke's two rows only: the purge wrote nothing for the terminal credential")
	require.Equal(t, "user", rows[1].ActorType, "the one revoked row is the revoke's, not a system row from the purge")
	require.Equal(t, "revoker", rows[1].ActorRef)
	require.Equal(t, 1, count(t, r.db, `SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE aggregate_id = 'cred_pl_race' AND event_type = 'acr_credential.revoked'`))
}

func TestPurgeLifecycle_ARevokeAfterThePurgeFindsNoRowAndWritesNoEvent(t *testing.T) {
	r := newRig(t)
	insertPurgeCredential(t, r.db, orgGone, "cred_pl_after", "live")
	purgeOrg(t, r, orgGone)
	before := count(t, r.db, `SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE aggregate_id = 'cred_pl_after'`)
	res, err := r.db.ExecContext(context.Background(), `UPDATE acr.client_credentials SET revoked_at = now() WHERE credential_id = 'cred_pl_after'`)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.Zero(t, n, "the revoke finds no row")
	require.Equal(t, before, count(t, r.db, `SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE aggregate_id = 'cred_pl_after'`))
}

// An issue that starts while the purge holds the write fence is refused (or
// ends after the purge); no live row the purge missed is committed, and every
// deleted live row has its revoked event.
func TestPurgeLifecycle_AnIssueDuringThePurgeIsRefusedAndNoDeletedLiveRowLacksItsEvent(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	insertPurgeCredential(t, r.db, orgGone, "cred_pl_fence_old", "live")
	_, err := r.db.ExecContext(ctx, `
CREATE FUNCTION acr.hold_purge_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(2); RETURN NULL; END $$;
CREATE TRIGGER trg_hold_purge_delete BEFORE DELETE ON acr.client_credentials FOR EACH STATEMENT EXECUTE FUNCTION acr.hold_purge_delete();`)
	require.NoError(t, err)

	var wg sync.WaitGroup
	var purgeErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, purgeErr = r.run(t, orgGone, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(orgGone)})
	}()
	require.Eventually(t, func() bool {
		return count(t, r.db, `SELECT count(*) FROM pg_stat_activity WHERE query LIKE 'DELETE FROM acr.client_credentials%' AND state = 'active'`) > 0
	}, 30*time.Second, 20*time.Millisecond, "the purge must be inside its credential delete (holding the fence)")
	_, issueErr := r.db.ExecContext(ctx, `
INSERT INTO acr.client_credentials (credential_id, org_id, name, token_prefix, token_hash, scopes, repository_scopes, created_at, client_class)
VALUES ('cred_pl_fence_new', $1::uuid, 'late', 'fcacr_late', 'hash-late', '["context:read"]'::jsonb, '["acme/widgets"]'::jsonb, now(), 'org_bearer')`, orgGone)
	wg.Wait()
	require.NoError(t, purgeErr)
	require.Error(t, issueErr, "the issue that started during the purge is refused")
	require.True(t, orgfence.Refused(issueErr), "refused by the write fence: %v", issueErr)
	require.Zero(t, count(t, r.db, `SELECT count(*) FROM acr.client_credentials WHERE org_id = $1::uuid`, orgGone), "no live row survives or is committed past the purge")
	require.Equal(t, 1, count(t, r.db, `SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE aggregate_id = 'cred_pl_fence_old' AND event_type = 'acr_credential.revoked'`), "every deleted live row has its event")
}
