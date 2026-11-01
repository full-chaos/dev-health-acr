package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/internal/credentiallifecycle"
)

// One cell per call-site class of the lifecycle outbox writer. Each reads the
// rows of the aggregates the flow touched from a real database; the lead's
// mutant trees remove one append call each and name the cell that must fail.

func lifecycleRows(t *testing.T, ctx context.Context, db *sql.DB, aggregate string) []outboxRow {
	t.Helper()
	result, err := db.QueryContext(ctx, `
SELECT aggregate_id, revision, event_type, payload->'payload'->>'actor_type', coalesce(payload->'payload'->>'actor_ref', '')
FROM acr.credential_lifecycle_outbox WHERE aggregate_id = $1 ORDER BY revision`, aggregate)
	require.NoError(t, err)
	defer result.Close()
	var out []outboxRow
	for result.Next() {
		var row outboxRow
		require.NoError(t, result.Scan(&row.Aggregate, &row.Revision, &row.EventType, &row.ActorType, &row.ActorRef))
		out = append(out, row)
	}
	require.NoError(t, result.Err())
	return out
}

func wireRef(t *testing.T, value string) string {
	t.Helper()
	ref, err := credentiallifecycle.WireRef(value)
	require.NoError(t, err)
	return ref
}

func principalOf(id string, repositories, scopes []string) storage.Principal {
	return storage.Principal{
		AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: id,
		OrgID: credentialTestOrgID, CredentialID: id, RepositoryScopes: repositories, Permissions: scopes,
	}
}

func TestCredentialOutboxClass_IssueWritesIssuedRevisionOne(t *testing.T) {
	f := newOutboxFixture(t)
	issued, err := f.service.Create(f.ctx, credentialCreateRequest("class-issue"))
	require.NoError(t, err)
	id := issued.Credential.CredentialID
	require.Equal(t, []outboxRow{{id, 1, "acr_credential.issued", "user", credentialTestActorID}}, lifecycleRows(t, f.ctx, f.db, id))
}

func TestCredentialOutboxClass_WorkloadExchangeWritesIssuedBySystem(t *testing.T) {
	f := newOutboxFixture(t)
	_, err := f.db.ExecContext(f.ctx, `
INSERT INTO acr.workload_bindings (binding_id, org_id, trust_domain, namespace, service_account_name, service_account_uid, role, repository_scopes, created_at, created_by)
VALUES ('wlb_outbox_test', $1, 'cluster.local', 'ns', 'sa', 'uid', 'read', '["*"]'::jsonb, now(), 'test_actor')`, workloadBindingTestOrgID)
	require.NoError(t, err)
	issued, err := f.service.Create(f.ctx, auth.CreateCredentialRequest{
		OrgID: workloadBindingTestOrgID, Name: "workload:wlb_outbox_test", RepositoryScopes: []string{"*"}, Scopes: []string{auth.ScopeContextRead},
		CreatedBy: "wlb_outbox_test", IssuanceProvenance: storage.CredentialIssuanceProvenanceWorkloadExchange, WorkloadBindingID: "wlb_outbox_test",
	})
	require.NoError(t, err)
	id := issued.Credential.CredentialID
	require.Equal(t, []outboxRow{{id, 1, "acr_credential.issued", "system", ""}}, lifecycleRows(t, f.ctx, f.db, id))
}

func TestCredentialOutboxClass_DeviceRedeemWritesDeviceRedeemed(t *testing.T) {
	ctx := context.Background()
	fixture := newProvenanceFixture(t, ctx)
	approved, grant := fixture.approveWithSession(t, ctx, "dv1", "")
	prepared, err := fixture.service.PrepareCreate(auth.CreateCredentialRequest{
		OrgID: grant.OrgID, Name: "device login", RepositoryScopes: grant.RepositoryScopes, Scopes: grant.Scopes, CreatedBy: grant.ApprovingSubject,
		ExpiresAt: postgresPointerTime(fixture.now.Add(24 * time.Hour)),
	})
	require.NoError(t, err)
	credential, err := fixture.devices.Redeem(ctx, approved.DeviceCodeHash, prepared.StorageInput())
	require.NoError(t, err)
	id := credential.CredentialID
	require.Equal(t, []outboxRow{{id, 1, "acr_credential.device_redeemed", "user", wireRef(t, grant.ApprovingSubject)}}, lifecycleRows(t, ctx, fixture.db, id))
}

func TestCredentialOutboxClass_BrowserRedeemWritesIssued(t *testing.T) {
	ctx := context.Background()
	fixture := newProvenanceFixture(t, ctx)
	approved, grant := fixture.approveWithSession(t, ctx, "br1", "sid_br1")
	credential, err := fixture.devices.Redeem(ctx, approved.DeviceCodeHash, fixture.browserInput(t, grant, ""))
	require.NoError(t, err)
	id := credential.CredentialID
	require.Equal(t, []outboxRow{{id, 1, "acr_credential.issued", "user", wireRef(t, grant.ApprovingSubject)}}, lifecycleRows(t, ctx, fixture.db, id))
	var class string
	require.NoError(t, fixture.db.QueryRowContext(ctx, `SELECT payload->'payload'->'change'->>'acr_class' FROM acr.credential_lifecycle_outbox WHERE aggregate_id = $1`, id).Scan(&class))
	require.Equal(t, "browser", class)
}

func TestCredentialOutboxClass_BrowserRefreshWritesTheSuccessorAndLeavesThePredecessorLive(t *testing.T) {
	ctx := context.Background()
	fixture := newRefreshFixture(t, ctx, newCredentialStoreDatabase(t, ctx))
	familyID, first := fixture.startFamily(t, ctx, "rf1")
	var predecessor string
	require.NoError(t, fixture.db.QueryRowContext(ctx, `SELECT credential_id FROM acr.client_credentials WHERE family_id = $1`, familyID).Scan(&predecessor))
	before := lifecycleRows(t, ctx, fixture.db, predecessor)
	require.Len(t, before, 1)

	rotated, err := fixture.service.Refresh(ctx, first, auth.DevBrowserClientID)
	require.NoError(t, err)
	require.Equal(t, storage.BrowserRefreshRotated, rotated.Decision)
	successor := rotated.Issued.Credential.CredentialID
	require.Equal(t, []outboxRow{{successor, 1, "acr_credential.issued", "system", ""}}, lifecycleRows(t, ctx, fixture.db, successor))
	require.Equal(t, before, lifecycleRows(t, ctx, fixture.db, predecessor), "the predecessor stays live: no event at refresh")
}

func TestCredentialOutboxClass_RotateWritesRotatedAndLeavesTheParentLiveWithOverlap(t *testing.T) {
	f := newOutboxFixture(t)
	parent, err := f.service.Create(f.ctx, credentialCreateRequest("class-rotate"))
	require.NoError(t, err)
	child, err := f.service.Rotate(f.ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: parent.Credential.CredentialID, CreatedBy: credentialTestActorID, Overlap: time.Minute})
	require.NoError(t, err)
	cid := child.Credential.CredentialID
	require.Equal(t, []outboxRow{{cid, 1, "acr_credential.rotated", "user", credentialTestActorID}}, lifecycleRows(t, f.ctx, f.db, cid))
	require.Len(t, lifecycleRows(t, f.ctx, f.db, parent.Credential.CredentialID), 1, "overlap above 0: no revoked row for the parent")
	var parentRef string
	require.NoError(t, f.db.QueryRowContext(f.ctx, `SELECT payload->'payload'->'change'->>'rotation_parent_id' FROM acr.credential_lifecycle_outbox WHERE aggregate_id = $1`, cid).Scan(&parentRef))
	require.Equal(t, parent.Credential.CredentialID, parentRef)
}

func TestCredentialOutboxClass_OverlapZeroWritesTheParentsRevokedRow(t *testing.T) {
	f := newOutboxFixture(t)
	parent, err := f.service.Create(f.ctx, credentialCreateRequest("class-overlap0"))
	require.NoError(t, err)
	pid := parent.Credential.CredentialID
	_, err = f.service.Rotate(f.ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: pid, CreatedBy: credentialTestActorID})
	require.NoError(t, err)
	require.Equal(t, []outboxRow{
		{pid, 1, "acr_credential.issued", "user", credentialTestActorID},
		{pid, 2, "acr_credential.revoked", "user", credentialTestActorID},
	}, lifecycleRows(t, f.ctx, f.db, pid))
}

func TestCredentialOutboxClass_AdminRevokeWritesRevokedByTheUser(t *testing.T) {
	f := newOutboxFixture(t)
	issued, err := f.service.Create(f.ctx, credentialCreateRequest("class-admin-revoke"))
	require.NoError(t, err)
	id := issued.Credential.CredentialID
	_, err = f.service.Revoke(f.ctx, credentialTestOrgID, id, credentialTestActorID)
	require.NoError(t, err)
	require.Equal(t, []outboxRow{
		{id, 1, "acr_credential.issued", "user", credentialTestActorID},
		{id, 2, "acr_credential.revoked", "user", credentialTestActorID},
	}, lifecycleRows(t, f.ctx, f.db, id))
}

func TestCredentialOutboxClass_SelfRevokeWritesRevokedByTheCredential(t *testing.T) {
	f := newOutboxFixture(t)
	issued, err := f.service.Create(f.ctx, credentialCreateRequest("class-self-revoke"))
	require.NoError(t, err)
	id := issued.Credential.CredentialID
	_, err = f.service.RevokeSelf(f.ctx, principalOf(id, issued.Credential.RepositoryScopes, issued.Credential.Scopes))
	require.NoError(t, err)
	require.Equal(t, []outboxRow{
		{id, 1, "acr_credential.issued", "user", credentialTestActorID},
		{id, 2, "acr_credential.revoked", "credential", id},
	}, lifecycleRows(t, f.ctx, f.db, id))
}

func TestCredentialOutboxClass_SelfRotateAndRollbackNameTheCredentialAsActor(t *testing.T) {
	f := newOutboxFixture(t)
	source, err := f.service.Create(f.ctx, credentialCreateRequest("class-self-rotate"))
	require.NoError(t, err)
	sid := source.Credential.CredentialID
	rotation, err := f.service.RotateSelf(f.ctx, principalOf(sid, source.Credential.RepositoryScopes, source.Credential.Scopes))
	require.NoError(t, err)
	cid := rotation.Issued.Credential.CredentialID
	require.Equal(t, []outboxRow{{cid, 1, "acr_credential.rotated", "credential", sid}}, lifecycleRows(t, f.ctx, f.db, cid))
	require.Len(t, lifecycleRows(t, f.ctx, f.db, sid), 1)

	_, err = f.service.RollbackSelfRotation(f.ctx, principalOf(cid, rotation.Issued.Credential.RepositoryScopes, rotation.Issued.Credential.Scopes), rotation.Receipt)
	require.NoError(t, err)
	require.Equal(t, []outboxRow{
		{cid, 1, "acr_credential.rotated", "credential", sid},
		{cid, 2, "acr_credential.revoked", "credential", cid},
	}, lifecycleRows(t, f.ctx, f.db, cid), "the rollback is the successor revoking itself")
}

func TestCredentialOutboxClass_FamilyRevokeWritesARevokedRowForEveryMember(t *testing.T) {
	ctx := context.Background()
	fixture := newRefreshFixture(t, ctx, newCredentialStoreDatabase(t, ctx))
	familyID, first := fixture.startFamily(t, ctx, "fr1")
	successor, err := fixture.service.Refresh(ctx, first, auth.DevBrowserClientID)
	require.NoError(t, err)
	_, err = fixture.creds.RevokeCredential(ctx, storage.CredentialRevocationInput{
		OrgID: successor.Issued.Credential.OrgID, CredentialID: successor.Issued.Credential.CredentialID, ActorID: credentialTestActorID,
	})
	require.NoError(t, err)
	live, revoked := familyCredentials(t, ctx, fixture.db, familyID)
	require.Equal(t, 0, live)
	require.Equal(t, 2, revoked)
	members, err := fixture.db.QueryContext(ctx, `SELECT credential_id FROM acr.client_credentials WHERE family_id = $1 ORDER BY credential_id`, familyID)
	require.NoError(t, err)
	defer members.Close()
	count := 0
	for members.Next() {
		var id string
		require.NoError(t, members.Scan(&id))
		rows := lifecycleRows(t, ctx, fixture.db, id)
		require.Len(t, rows, 2, id)
		require.Equal(t, 2, rows[1].Revision, id)
		require.Equal(t, "acr_credential.revoked", rows[1].EventType, id)
		count++
	}
	require.NoError(t, members.Err())
	require.Equal(t, 2, count)
}

func TestCredentialOutboxClass_AckSweepWritesRevokedBySystem(t *testing.T) {
	f := newPostgresAckFixture(t)
	first, err := f.redeemAckable(t)
	require.NoError(t, err)
	f.now = f.now.Add(storage.DeviceCredentialAckWindow)
	revoked, err := f.store.RevokeUnacknowledged(f.ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, revoked)
	rows := lifecycleRows(t, f.ctx, f.db, first)
	require.Equal(t, []outboxRow{
		{first, 1, "acr_credential.device_redeemed", "user", wireRef(t, f.grant.ApprovingSubject)},
		{first, 2, "acr_credential.revoked", "system", ""},
	}, rows)
}

func TestCredentialOutboxClass_ReplacedCredentialRevokeWritesRevokedBySystem(t *testing.T) {
	f := newPostgresAckFixture(t)
	first, err := f.redeemAckable(t)
	require.NoError(t, err)
	f.now = f.now.Add(storage.DeviceCredentialAckWindow - time.Second)
	second, err := f.redeemAckable(t)
	require.NoError(t, err)
	require.Equal(t, []outboxRow{
		{first, 1, "acr_credential.device_redeemed", "user", wireRef(t, f.grant.ApprovingSubject)},
		{first, 2, "acr_credential.revoked", "system", ""},
	}, lifecycleRows(t, f.ctx, f.db, first))
	require.Equal(t, []outboxRow{{second, 1, "acr_credential.device_redeemed", "user", wireRef(t, f.grant.ApprovingSubject)}},
		lifecycleRows(t, f.ctx, f.db, second))
}

func TestCredentialOutboxClass_RotateOfALegacyParentWritesTheParentsRevisionOneFirst(t *testing.T) {
	f := newOutboxFixture(t)
	parent, err := f.service.Create(f.ctx, credentialCreateRequest("class-legacy-parent"))
	require.NoError(t, err)
	pid := parent.Credential.CredentialID
	_, err = f.db.ExecContext(f.ctx, `DELETE FROM acr.credential_lifecycle_outbox`)
	require.NoError(t, err)
	child, err := f.service.Rotate(f.ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: pid, CreatedBy: credentialTestActorID})
	require.NoError(t, err)
	require.Equal(t, []outboxRow{
		{pid, 1, "acr_credential.issued", "system", ""},
		{pid, 2, "acr_credential.revoked", "user", credentialTestActorID},
	}, lifecycleRows(t, f.ctx, f.db, pid))
	require.Equal(t, []outboxRow{{child.Credential.CredentialID, 1, "acr_credential.rotated", "user", credentialTestActorID}},
		lifecycleRows(t, f.ctx, f.db, child.Credential.CredentialID))

	// With an overlap the replaced credential is not revoked, so no revoked
	// append can write its revision 1 as a side effect: only the first touch
	// the rotation itself makes can. A rotation that skips it leaves the
	// parent without any row while the rotated row names it.
	live, err := f.service.Create(f.ctx, credentialCreateRequest("class-legacy-parent-overlap"))
	require.NoError(t, err)
	lid := live.Credential.CredentialID
	_, err = f.db.ExecContext(f.ctx, `DELETE FROM acr.credential_lifecycle_outbox`)
	require.NoError(t, err)
	overlapChild, err := f.service.Rotate(f.ctx, auth.RotateCredentialRequest{OrgID: credentialTestOrgID, CredentialID: lid, CreatedBy: credentialTestActorID, Overlap: time.Minute})
	require.NoError(t, err)
	require.Equal(t, []outboxRow{{lid, 1, "acr_credential.issued", "system", ""}}, lifecycleRows(t, f.ctx, f.db, lid),
		"the parent's revision 1 (issued, system, no ref) exists before the rotated event that names it")
	require.Equal(t, []outboxRow{{overlapChild.Credential.CredentialID, 1, "acr_credential.rotated", "user", credentialTestActorID}},
		lifecycleRows(t, f.ctx, f.db, overlapChild.Credential.CredentialID))
}

func browserFamilyMembers(t *testing.T, f outboxFixture, family string, n int) []string {
	t.Helper()
	end := time.Now().UTC().Add(24 * time.Hour)
	var ids []string
	for i := 0; i < n; i++ {
		suffix := family + string(rune('a'+i))
		expires := time.Now().UTC().Add(time.Hour)
		credential, err := f.store.CreateCredential(f.ctx, storage.CredentialCreateInput{
			CredentialID: "cred_" + suffix, OrgID: credentialTestOrgID, Name: suffix, TokenPrefix: "fcacr_abcdefghij",
			TokenHash:        sha256Hex(suffix),
			RepositoryScopes: []string{"acme/widgets"}, Scopes: []string{"context:read"}, ActorID: "web-user-1",
			ExpiresAt: &expires, IssuanceProvenance: storage.CredentialIssuanceProvenanceDeviceAuthorization,
			ApprovingUserID: "web-user-1", Resource: "https://acr.example/mcp", ClientID: "client-1",
			ClientClass: storage.CredentialClientClassBrowser, SessionID: "sess-" + family, FamilyID: family, FamilyExpiresAt: &end,
		})
		require.NoError(t, err)
		ids = append(ids, credential.CredentialID)
	}
	return ids
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func requireFamilyRows(t *testing.T, f outboxFixture, ids []string, wantType string, wantRef func(id string) string) {
	t.Helper()
	for _, id := range ids {
		rows := lifecycleRows(t, f.ctx, f.db, id)
		require.Len(t, rows, 2, id)
		require.Equal(t, 2, rows[1].Revision, id)
		require.Equal(t, "acr_credential.revoked", rows[1].EventType, id)
		require.Equal(t, wantType, rows[1].ActorType, "%s: every member keeps the mapped actor type", id)
		require.Equal(t, wantRef(id), rows[1].ActorRef, id)
	}
}

func TestCredentialOutboxClass_FamilyRevokeByUserKeepsTheActorOnEveryMember(t *testing.T) {
	f := newOutboxFixture(t)
	ids := browserFamilyMembers(t, f, "famuser", 3)
	_, err := f.service.Revoke(f.ctx, credentialTestOrgID, ids[0], "web-user-1")
	require.NoError(t, err)
	requireFamilyRows(t, f, ids, "user", func(string) string { return "web-user-1" })
}

func TestCredentialOutboxClass_FamilyRevokeByOperatorKeepsTheActorOnEveryMember(t *testing.T) {
	f := newOutboxFixture(t)
	ids := browserFamilyMembers(t, f, "famoperator", 3)
	_, err := f.service.RevokeAsOperator(f.ctx, credentialTestOrgID, ids[0], "operator_1", "")
	require.NoError(t, err)
	requireFamilyRows(t, f, ids, "operator", func(string) string { return "operator_1" })
}

func TestCredentialOutboxClass_FamilyRevokeBySelfNamesEachMemberAsItsOwnActor(t *testing.T) {
	f := newOutboxFixture(t)
	ids := browserFamilyMembers(t, f, "famself", 3)
	_, err := f.service.RevokeSelf(f.ctx, principalOf(ids[0], []string{"acme/widgets"}, []string{"context:read"}))
	require.NoError(t, err)
	// The contract fixes a credential actor's reference to the row's own
	// aggregate, so each member names itself.
	requireFamilyRows(t, f, ids, "credential", func(id string) string { return id })
}

func TestCredentialOutboxClass_RefreshReuseAndSignOutRevokeTheFamilyAsTheRefreshGrant(t *testing.T) {
	ctx := context.Background()
	fixture := newRefreshFixture(t, ctx, newCredentialStoreDatabase(t, ctx))
	f := outboxFixture{ctx: ctx, db: fixture.db}
	members := func(familyID string) []string {
		rows, err := fixture.db.QueryContext(ctx, `SELECT credential_id FROM acr.client_credentials WHERE family_id = $1 ORDER BY credential_id`, familyID)
		require.NoError(t, err)
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			require.NoError(t, rows.Scan(&id))
			ids = append(ids, id)
		}
		require.NoError(t, rows.Err())
		return ids
	}

	reuseFamily, first := fixture.startFamily(t, ctx, "ru1")
	_, err := fixture.service.Refresh(ctx, first, auth.DevBrowserClientID)
	require.NoError(t, err)
	*fixture.clock = fixture.clock.Add(auth.BrowserRefreshGrace + time.Second)
	reuse, err := fixture.service.Refresh(ctx, first, auth.DevBrowserClientID)
	require.NoError(t, err)
	require.Equal(t, storage.BrowserRefreshReuseRevoked, reuse.Decision)
	reuseIDs := members(reuseFamily)
	require.Len(t, reuseIDs, 2)
	requireFamilyRows(t, f, reuseIDs, "user", func(string) string { return "oauth_refresh_grant" })

	*fixture.clock = fixture.now
	signedFamily, token := fixture.startFamily(t, ctx, "so1")
	decision, err := fixture.service.RevokeFamily(ctx, token, auth.DevBrowserClientID)
	require.NoError(t, err)
	require.Equal(t, storage.BrowserFamilyRevokedNow, decision)
	signedIDs := members(signedFamily)
	require.Len(t, signedIDs, 1)
	requireFamilyRows(t, f, signedIDs, "user", func(string) string { return "oauth_refresh_grant" })
}
