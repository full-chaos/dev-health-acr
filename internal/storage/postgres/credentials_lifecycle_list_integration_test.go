package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Real-database cells of the read-only credential listing (CHAOS-11200): the
// keyset walk over the lifecycle outbox, the status of the latest event, the
// organization boundary, the plan on a table with many organizations, and the
// reconcile workload role. Lead-run.

const listOtherOrgID = "22222222-2222-2222-2222-222222222222"

func walkLifecycle(t *testing.T, f outboxFixture, org string, limit int) []contractsv1.CredentialLifecycleItem {
	t.Helper()
	audit, err := NewAuditStore(f.db)
	require.NoError(t, err)
	var all []contractsv1.CredentialLifecycleItem
	after := ""
	for range 1000 {
		page, err := audit.ListCredentialLifecycle(f.ctx, org, after, limit)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page), limit)
		if len(page) == 0 {
			return all
		}
		all = append(all, page...)
		after = page[len(page)-1].CredentialID
	}
	t.Fatal("walk does not end")
	return nil
}

func TestCredentialLifecycleList_StatusFromTheLatestEventRevisionIsTheMaximum(t *testing.T) {
	f := newTickFixture(t)
	issued, err := f.service.Create(f.ctx, credentialCreateRequest("list-live"))
	require.NoError(t, err)
	live := issued.Credential.CredentialID

	revoked, err := f.service.Create(f.ctx, credentialCreateRequest("list-revoked"))
	require.NoError(t, err)
	_, err = f.service.Revoke(f.ctx, credentialTestOrgID, revoked.Credential.CredentialID, credentialTestActorID)
	require.NoError(t, err)

	rotatedParent, err := f.service.Create(f.ctx, credentialCreateRequest("list-parent"))
	require.NoError(t, err)
	child, err := f.service.Rotate(f.ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: rotatedParent.Credential.CredentialID, CreatedBy: credentialTestActorID})
	require.NoError(t, err)

	insertTickCredential(t, f.db, "cred_list_expired", past, "NULL")
	require.Equal(t, 1, f.run(t, 100).Expired)
	insertTickCredential(t, f.db, "cred_list_legacy", future, "NULL")

	got := map[string]contractsv1.CredentialLifecycleItem{}
	for _, item := range walkLifecycle(t, f.outboxFixture, credentialTestOrgID, 2) {
		got[item.CredentialID] = item
	}
	want := map[string]contractsv1.CredentialLifecycleItem{
		live:                                  {CredentialID: live, Revision: 1, Status: "active"},
		revoked.Credential.CredentialID:       {CredentialID: revoked.Credential.CredentialID, Revision: 2, Status: "revoked"},
		rotatedParent.Credential.CredentialID: {CredentialID: rotatedParent.Credential.CredentialID, Revision: 2, Status: "revoked"},
		child.Credential.CredentialID:         {CredentialID: child.Credential.CredentialID, Revision: 1, Status: "active"},
		"cred_list_expired":                   {CredentialID: "cred_list_expired", Revision: 2, Status: "expired"},
	}
	require.Equal(t, want, got, "a credential with no outbox row (cred_list_legacy) is absent until the tick writes its first row")

	require.Equal(t, 1, f.run(t, 100).FirstTouch)
	again := map[string]contractsv1.CredentialLifecycleItem{}
	for _, item := range walkLifecycle(t, f.outboxFixture, credentialTestOrgID, 100) {
		again[item.CredentialID] = item
	}
	require.Equal(t, contractsv1.CredentialLifecycleItem{CredentialID: "cred_list_legacy", Revision: 1, Status: "active"}, again["cred_list_legacy"])
}

func TestCredentialLifecycleList_KeysetWalkHasNoDuplicateAndNoSkipAndAnInsertMidWalkSortsByID(t *testing.T) {
	f := newOutboxFixture(t)
	for i := range 7 {
		_, err := f.db.ExecContext(f.ctx, `
INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at)
SELECT gen_random_uuid(), $1::uuid, $2, r, CASE WHEN r = 1 THEN 'acr_credential.issued' ELSE 'acr_credential.revoked' END, '{}'::jsonb, now()
FROM generate_series(1, $3::int) AS r`, credentialTestOrgID, fmt.Sprintf("cred_w%02d", i*2), 1+i%2)
		require.NoError(t, err)
	}
	audit, err := NewAuditStore(f.db)
	require.NoError(t, err)
	first, err := audit.ListCredentialLifecycle(f.ctx, credentialTestOrgID, "", 3)
	require.NoError(t, err)
	require.Len(t, first, 3)
	// A credential sorting after the cursor appears; one sorting before it does not.
	for _, id := range []string{"cred_w01", "cred_w99"} {
		_, err = f.db.ExecContext(f.ctx, `INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at)
VALUES (gen_random_uuid(), $1::uuid, $2, 1, 'acr_credential.issued', '{}'::jsonb, now())`, credentialTestOrgID, id)
		require.NoError(t, err)
	}
	rest := []contractsv1.CredentialLifecycleItem{}
	after := first[2].CredentialID
	for {
		page, err := audit.ListCredentialLifecycle(f.ctx, credentialTestOrgID, after, 3)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		rest = append(rest, page...)
		after = page[len(page)-1].CredentialID
	}
	ids := []string{}
	for _, item := range append(first, rest...) {
		ids = append(ids, item.CredentialID)
	}
	require.Equal(t, []string{"cred_w00", "cred_w02", "cred_w04", "cred_w06", "cred_w08", "cred_w10", "cred_w12", "cred_w99"}, ids,
		"cred_w99 sorts after the cursor and appears; cred_w01 sorts before it and is not listed in this walk")
}

func TestCredentialLifecycleList_OnlyTheCallersOrganization(t *testing.T) {
	f := newOutboxFixture(t)
	mine, err := f.service.Create(f.ctx, credentialCreateRequest("list-mine"))
	require.NoError(t, err)
	other := credentialCreateRequest("list-other")
	other.OrgID = listOtherOrgID
	theirs, err := f.service.Create(f.ctx, other)
	require.NoError(t, err)
	got := walkLifecycle(t, f, credentialTestOrgID, 10)
	require.Len(t, got, 1)
	require.Equal(t, mine.Credential.CredentialID, got[0].CredentialID)
	for _, item := range walkLifecycle(t, f, listOtherOrgID, 10) {
		require.Equal(t, theirs.Credential.CredentialID, item.CredentialID)
	}
	audit, err := NewAuditStore(f.db)
	require.NoError(t, err)
	_, err = audit.ListCredentialLifecycle(f.ctx, "not-a-uuid", "", 10)
	require.Error(t, err, "an org id that is not a UUID is an error, never an empty or a wide read")
}

func TestCredentialLifecycleList_TheKeysetPageUsesTheOrgAggregateIndexOnATableOfManyOrganizations(t *testing.T) {
	f := newOutboxFixture(t)
	_, err := f.db.ExecContext(f.ctx, `
INSERT INTO acr.credential_lifecycle_outbox (outbox_id, org_id, aggregate_id, revision, event_type, payload, created_at)
SELECT gen_random_uuid(), ('00000000-0000-0000-0000-' || lpad(o::text, 12, '0'))::uuid, 'cred_' || lpad(o::text, 3, '0') || '_' || lpad(a::text, 5, '0'), 1,
       'acr_credential.issued', '{}'::jsonb, now()
FROM generate_series(1, 60) AS o, generate_series(1, 400) AS a`)
	require.NoError(t, err)
	_, err = f.db.ExecContext(f.ctx, `ANALYZE acr.credential_lifecycle_outbox`)
	require.NoError(t, err)
	org := "00000000-0000-0000-0000-000000000042"
	plan := explainListing(t, f.db, f.ctx, org, "cred_042_00100", 100)
	t.Logf("plan:\n%s", plan)
	require.Contains(t, plan, "ix_acr_credential_lifecycle_outbox_org_aggregate", "the keyset walk is served by the org-leading index")
	require.NotContains(t, plan, "Seq Scan on credential_lifecycle_outbox", "no sequential scan of the outbox")
	_, err = f.db.ExecContext(f.ctx, `DROP INDEX acr.ix_acr_credential_lifecycle_outbox_org_aggregate`)
	require.NoError(t, err)
	t.Logf("plan without the index (for the record, not asserted):\n%s", explainListing(t, f.db, f.ctx, org, "cred_042_00100", 100))
	_, err = f.db.ExecContext(f.ctx, `CREATE INDEX ix_acr_credential_lifecycle_outbox_org_aggregate ON acr.credential_lifecycle_outbox (org_id, aggregate_id COLLATE "C")`)
	require.NoError(t, err)
	audit, err := NewAuditStore(f.db)
	require.NoError(t, err)
	page, err := audit.ListCredentialLifecycle(f.ctx, org, "cred_042_00100", 100)
	require.NoError(t, err)
	require.Len(t, page, 100)
	require.Equal(t, "cred_042_00101", page[0].CredentialID)
	require.Equal(t, "cred_042_00200", page[99].CredentialID)
}

func explainListing(t *testing.T, db *sql.DB, ctx context.Context, org, after string, limit int) string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "EXPLAIN (COSTS OFF) "+CredentialLifecycleListSQL, org, after, limit)
	require.NoError(t, err)
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		lines = append(lines, line)
	}
	require.NoError(t, rows.Err())
	return strings.Join(lines, "\n")
}

func TestWorkloadBindings_TheReconcileAndRevokeRolesAreAdmittedAndAnUnknownRoleIsNot(t *testing.T) {
	ctx := context.Background()
	db := newCredentialStoreDatabase(t, ctx)
	insert := func(id, role string) error {
		_, err := db.ExecContext(ctx, `
INSERT INTO acr.workload_bindings (binding_id, org_id, trust_domain, namespace, service_account_name, service_account_uid, role, repository_scopes, created_at, created_by)
VALUES ($1, $2, 'cluster.local', 'ns', 'sa-' || $1, 'uid-' || $1, $3, '["*"]'::jsonb, $4, 'test_actor')`, id, credentialTestOrgID, role, time.Now())
		return err
	}
	for _, role := range []string{"read", "ops", "reconcile", "revoke"} {
		require.NoError(t, insert("wlb_role_"+role, role), role)
	}
	require.Error(t, insert("wlb_role_admin", "admin"))
	store, err := NewWorkloadBindingStore(db)
	require.NoError(t, err)
	bindings, err := store.Lookup(ctx, storage.WorkloadBindingKey{TrustDomain: "cluster.local", Namespace: "ns", ServiceAccountName: "sa-wlb_role_reconcile", ServiceAccountUID: "uid-wlb_role_reconcile"})
	require.NoError(t, err)
	require.Len(t, bindings, 1)
	binding := bindings[0]
	require.Equal(t, "reconcile", binding.Role)
	require.Equal(t, []string{auth.ScopeCredentialLifecycleRead}, auth.RoleScopes(binding.Role))
	bindings, err = store.Lookup(ctx, storage.WorkloadBindingKey{TrustDomain: "cluster.local", Namespace: "ns", ServiceAccountName: "sa-wlb_role_revoke", ServiceAccountUID: "uid-wlb_role_revoke"})
	require.NoError(t, err)
	require.Len(t, bindings, 1)
	require.Equal(t, "revoke", bindings[0].Role)
	require.Equal(t, []string{auth.ScopeCredentialLifecycleRevoke}, auth.RoleScopes(bindings[0].Role))
}
