package postgres

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// Real-database cells of the credential lifecycle tick (the expired producer
// and the first-touch tick) and of the retention gate. Lead-run.

var tickNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

type tickFixture struct {
	outboxFixture
	tick *LifecycleTick
}

func newTickFixture(t *testing.T) tickFixture {
	t.Helper()
	f := newOutboxFixture(t)
	return tickFixture{outboxFixture: f, tick: newTickOn(t, f.db)}
}

func newTickOn(t *testing.T, db *sql.DB) *LifecycleTick {
	t.Helper()
	audit, err := NewAuditStore(db)
	require.NoError(t, err)
	tick, err := NewCredentialLifecycleTick(db, audit)
	require.NoError(t, err)
	return tick
}

// insertTickCredential writes a credential by SQL (no outbox row: a legacy one).
// expires is a SQL expression ("NULL", or a timestamp); revoked likewise.
func insertTickCredential(t *testing.T, db *sql.DB, id, expires, revoked string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
INSERT INTO acr.client_credentials (credential_id, org_id, name, token_prefix, token_hash, scopes, repository_scopes, created_at, client_class, expires_at, revoked_at)
VALUES ($1, $2::uuid, 'tick cell', 'fcacr_tickcell', 'hash-' || $1, '["context:read"]'::jsonb, '["acme/widgets"]'::jsonb, $3::timestamptz - interval '3 days', 'org_bearer', `+expires+`, `+revoked+`)`,
		id, credentialTestOrgID, tickNow)
	require.NoError(t, err, id)
}

func (f tickFixture) run(t *testing.T, limit int) LifecycleTickResult {
	t.Helper()
	result, err := f.tick.Tick(f.ctx, tickNow, limit)
	require.NoError(t, err)
	return result
}

const (
	past     = "'2026-11-02T11:00:00Z'::timestamptz"
	exactNow = "'2026-11-02T12:00:00Z'::timestamptz"
	future   = "'2026-11-03T12:00:00Z'::timestamptz"
)

func TestLifecycleTick_AnExpiredUnrevokedCredentialGetsItsFirstTouchThenOneExpiredRow(t *testing.T) {
	f := newTickFixture(t)
	insertTickCredential(t, f.db, "cred_tk_expired", past, "NULL")
	require.Equal(t, 1, f.run(t, 100).Expired)
	require.Equal(t, []outboxRow{
		{"cred_tk_expired", 1, "acr_credential.issued", "system", ""},
		{"cred_tk_expired", 2, "acr_credential.expired", "system", ""},
	}, f.rows(t, "cred_tk_expired"))
	var expiresAt, createdAt string
	require.NoError(t, f.db.QueryRowContext(f.ctx, `SELECT payload->'payload'->'change'->>'expires_at', payload->>'created_at' FROM acr.credential_lifecycle_outbox WHERE aggregate_id = 'cred_tk_expired' AND revision = 2`).Scan(&expiresAt, &createdAt))
	require.Equal(t, "2026-11-02T11:00:00Z", expiresAt, "change.expires_at is the credential's expiry")
	require.Equal(t, "2026-11-02T12:00:00Z", createdAt, "created_at is the tick's one now")
}

func TestLifecycleTick_AnEventedExpiredCredentialGetsOnlyTheExpiredRow(t *testing.T) {
	f := newTickFixture(t)
	issued, err := f.service.Create(f.ctx, credentialCreateRequest("tick-evented"))
	require.NoError(t, err)
	_, err = f.db.ExecContext(f.ctx, `UPDATE acr.client_credentials SET expires_at = `+past+` WHERE credential_id = $1`, issued.Credential.CredentialID)
	require.NoError(t, err)
	require.Equal(t, 1, f.run(t, 100).Expired)
	rows := f.rows(t, issued.Credential.CredentialID)
	require.Len(t, rows, 2)
	require.Equal(t, "acr_credential.expired", rows[1].EventType)
}

func TestLifecycleTick_RevokedNullFutureAndTheBoundaryAreTreatedByTheirExpiry(t *testing.T) {
	f := newTickFixture(t)
	insertTickCredential(t, f.db, "cred_tk_revoked", past, "'2026-11-01T00:00:00Z'")
	insertTickCredential(t, f.db, "cred_tk_null", "NULL", "NULL")
	insertTickCredential(t, f.db, "cred_tk_future", future, "NULL")
	insertTickCredential(t, f.db, "cred_tk_boundary", exactNow, "NULL")
	result := f.run(t, 100)
	require.Equal(t, 1, result.Expired, "only the boundary credential (expires_at == now) expires")
	require.Empty(t, f.rows(t, "cred_tk_revoked"), "a revoked credential is terminal: no event")
	require.Equal(t, "acr_credential.expired", f.rows(t, "cred_tk_boundary")[1].EventType)
	require.Equal(t, []outboxRow{{"cred_tk_null", 1, "acr_credential.issued", "system", ""}}, f.rows(t, "cred_tk_null"), "live and untouched: first touch only")
	require.Equal(t, []outboxRow{{"cred_tk_future", 1, "acr_credential.issued", "system", ""}}, f.rows(t, "cred_tk_future"))
	require.Equal(t, 2, result.FirstTouch)
}

func TestLifecycleTick_ASecondTickWritesNothing(t *testing.T) {
	f := newTickFixture(t)
	insertTickCredential(t, f.db, "cred_tk_again", past, "NULL")
	insertTickCredential(t, f.db, "cred_tk_again_live", future, "NULL")
	first := f.run(t, 100)
	require.Equal(t, 1, first.Expired)
	require.Equal(t, 1, first.FirstTouch)
	before := f.count(t, `SELECT count(*) FROM acr.credential_lifecycle_outbox`)
	second := f.run(t, 100)
	require.Zero(t, second.Expired+second.FirstTouch)
	require.Equal(t, before, f.count(t, `SELECT count(*) FROM acr.credential_lifecycle_outbox`))
}

func TestLifecycleTick_ARotatedParentPastItsOverlapCutExpiresOnce(t *testing.T) {
	f := newTickFixture(t)
	parent, err := f.service.Create(f.ctx, credentialCreateRequest("tick-overlap"))
	require.NoError(t, err)
	_, err = f.service.Rotate(f.ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: parent.Credential.CredentialID, CreatedBy: credentialTestActorID, Overlap: time.Minute})
	require.NoError(t, err)
	later := time.Now().UTC().Add(5 * time.Minute)
	for i := 0; i < 2; i++ {
		_, err = f.tick.Tick(f.ctx, later, 100)
		require.NoError(t, err)
	}
	rows := f.rows(t, parent.Credential.CredentialID)
	require.Len(t, rows, 2)
	require.Equal(t, "acr_credential.expired", rows[1].EventType)
}

func TestLifecycleTick_TwoConcurrentTicksWriteOneExpiredRow(t *testing.T) {
	f := newTickFixture(t)
	insertTickCredential(t, f.db, "cred_tk_race", past, "NULL")
	other := newTickOn(t, f.db)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, tick := range []*LifecycleTick{f.tick, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = tick.Tick(f.ctx, tickNow, 100)
		}()
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE aggregate_id = 'cred_tk_race' AND event_type = 'acr_credential.expired'`))
}

func TestLifecycleTick_ARowLockedByARevokeIsSkippedAndNeverExpiredAfterIt(t *testing.T) {
	f := newTickFixture(t)
	insertTickCredential(t, f.db, "cred_tk_locked", past, "NULL")
	revoker, err := f.db.BeginTx(f.ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = revoker.Rollback() })
	_, err = revoker.ExecContext(f.ctx, `UPDATE acr.client_credentials SET revoked_at = now() WHERE credential_id = 'cred_tk_locked'`)
	require.NoError(t, err)
	require.Zero(t, f.run(t, 100).Expired, "the locked row is skipped, not waited for")
	require.NoError(t, revoker.Commit())
	require.Zero(t, f.run(t, 100).Expired, "once revoked it is terminal")
	require.Empty(t, f.rows(t, "cred_tk_locked"))
}

func TestLifecycleTick_TheBatchIsBoundedOrderedAndReportsFull(t *testing.T) {
	f := newTickFixture(t)
	for i, id := range []string{"cred_tk_b1", "cred_tk_b2", "cred_tk_b3"} {
		insertTickCredential(t, f.db, id, "'2026-11-02T0"+string(rune('1'+i))+":00:00Z'::timestamptz", "NULL")
	}
	first := f.run(t, 2)
	require.Equal(t, 2, first.Expired)
	require.True(t, first.Full, "a batch that reached the limit says so")
	require.NotEmpty(t, f.rows(t, "cred_tk_b1"), "oldest expiry first")
	require.NotEmpty(t, f.rows(t, "cred_tk_b2"))
	require.Empty(t, f.rows(t, "cred_tk_b3"))
	second := f.run(t, 2)
	require.Equal(t, 1, second.Expired)
	require.False(t, second.Full)
	require.NotEmpty(t, f.rows(t, "cred_tk_b3"))
}

func TestLifecycleTick_AFailedOutboxWriteRollsTheWholeTickBack(t *testing.T) {
	f := newTickFixture(t)
	insertTickCredential(t, f.db, "cred_tk_rollback", past, "NULL")
	insertTickCredential(t, f.db, "cred_tk_rollback_live", future, "NULL")
	_, err := f.db.ExecContext(f.ctx, `
CREATE FUNCTION acr.refuse_outbox_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'outbox refused'; END $$;
CREATE TRIGGER trg_refuse_outbox_insert BEFORE INSERT ON acr.credential_lifecycle_outbox FOR EACH ROW EXECUTE FUNCTION acr.refuse_outbox_insert();`)
	require.NoError(t, err)
	_, err = f.tick.Tick(f.ctx, tickNow, 100)
	require.Error(t, err)
	require.Zero(t, f.count(t, `SELECT count(*) FROM acr.credential_lifecycle_outbox`))
}

// Revoking or rotating an already expired credential still succeeds; the
// outbox takes no second terminal row for it.
func TestLifecycleTick_ARevokeAfterTheExpiredEventWritesNoSecondTerminalRow(t *testing.T) {
	f := newTickFixture(t)
	issued, err := f.service.Create(f.ctx, credentialCreateRequest("tick-revoke-after"))
	require.NoError(t, err)
	id := issued.Credential.CredentialID
	_, err = f.db.ExecContext(f.ctx, `UPDATE acr.client_credentials SET expires_at = `+past+` WHERE credential_id = $1`, id)
	require.NoError(t, err)
	f.run(t, 100)
	_, err = f.service.Revoke(f.ctx, credentialTestOrgID, id, credentialTestActorID)
	require.NoError(t, err)
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE aggregate_id = $1 AND event_type IN ('acr_credential.expired','acr_credential.revoked')`, id))
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM acr.audit_events WHERE resource_id = $1 AND action = 'credential_revoked'`, id), "the audit row is still written")
}

func TestLifecycleTick_ARotationOfAnExpiredParentWritesTheChildAndNoSecondTerminalForTheParent(t *testing.T) {
	f := newTickFixture(t)
	parent, err := f.service.Create(f.ctx, credentialCreateRequest("tick-rotate-after"))
	require.NoError(t, err)
	pid := parent.Credential.CredentialID
	_, err = f.db.ExecContext(f.ctx, `UPDATE acr.client_credentials SET expires_at = `+past+` WHERE credential_id = $1`, pid)
	require.NoError(t, err)
	f.run(t, 100)
	child, err := f.service.Rotate(f.ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: pid, CreatedBy: credentialTestActorID})
	require.NoError(t, err)
	require.Len(t, f.rows(t, child.Credential.CredentialID), 1, "the child's rows are written")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE aggregate_id = $1 AND event_type IN ('acr_credential.expired','acr_credential.revoked')`, pid))
}

func TestLifecycleTick_TheWorkloadPurgeWaitsForTheExpiredEventAndNeverForARevokedRow(t *testing.T) {
	f := newTickFixture(t)
	_, err := f.db.ExecContext(f.ctx, `
INSERT INTO acr.workload_bindings (binding_id, org_id, trust_domain, namespace, service_account_name, service_account_uid, role, repository_scopes, created_at, created_by)
VALUES ('wlb_tick_gate', $1, 'cluster.local', 'ns', 'sa', 'uid', 'read', '["*"]'::jsonb, now(), 'test_actor')`, workloadBindingTestOrgID)
	require.NoError(t, err)
	for _, spec := range []struct{ id, revoked string }{{"cred_tk_wl_open", "NULL"}, {"cred_tk_wl_revoked", "'2026-11-01T00:00:00Z'"}} {
		_, err = f.db.ExecContext(f.ctx, `
INSERT INTO acr.client_credentials (credential_id, org_id, name, token_prefix, token_hash, scopes, repository_scopes, created_at, client_class, expires_at, revoked_at, workload_binding_id)
VALUES ($1, $2::uuid, 'wl', 'fcacr_wl', 'hash-' || $1, '["context:read"]'::jsonb, '["*"]'::jsonb, $3::timestamptz - interval '3 days', 'workload', `+past+`, `+spec.revoked+`, 'wlb_tick_gate')`, spec.id, workloadBindingTestOrgID, tickNow)
		require.NoError(t, err)
	}
	purge := NewWorkloadCredentialPurger(f.db)
	deleted, err := purge(f.ctx, tickNow, 500)
	require.NoError(t, err)
	require.Equal(t, 1, deleted, "the revoked workload credential goes; the unrevoked one has no expired event yet")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM acr.client_credentials WHERE credential_id = 'cred_tk_wl_open'`))
	_, err = f.tick.Tick(f.ctx, tickNow, 100)
	require.NoError(t, err)
	deleted, err = purge(f.ctx, tickNow, 500)
	require.NoError(t, err)
	require.Equal(t, 1, deleted, "after the tick wrote the expired row the purge takes it")
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM acr.credential_lifecycle_outbox WHERE aggregate_id = 'cred_tk_wl_open' AND event_type = 'acr_credential.expired'`), "its expired row is kept")
}

func TestLifecycleTick_TheEndedBrowserPurgeWaitsForTheExpiredEvent(t *testing.T) {
	f := newTickFixture(t)
	_, err := f.db.ExecContext(f.ctx, `
INSERT INTO acr.client_credentials (credential_id, org_id, name, token_prefix, token_hash, scopes, repository_scopes, created_at, client_class, expires_at,
    approving_user_id, client_id, session_id, family_id, family_expires_at, resource)
VALUES ('cred_tk_browser', $1::uuid, 'b', 'fcacr_b', 'hash-b', '["context:read"]'::jsonb, '["acme/widgets"]'::jsonb, $2::timestamptz - interval '3 days', 'browser', `+past+`,
    'web-user-1', 'client-1', 'sess-1', 'fam_tk_gate', `+past+`, 'https://acr.example/mcp')`, credentialTestOrgID, tickNow)
	require.NoError(t, err)
	audit, err := NewAuditStore(f.db)
	require.NoError(t, err)
	store, err := NewBrowserRefreshStore(f.db, audit)
	require.NoError(t, err)
	result, err := store.PurgeEndedFamilies(f.ctx, tickNow, time.Minute, 500)
	require.NoError(t, err)
	require.Zero(t, result.Credentials, "no expired event yet: the credential waits")
	_, err = f.tick.Tick(f.ctx, tickNow, 100)
	require.NoError(t, err)
	result, err = store.PurgeEndedFamilies(f.ctx, tickNow, time.Minute, 500)
	require.NoError(t, err)
	require.Equal(t, 1, result.Credentials)
}
