package postgres

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The organization revoke on a real database: every live credential of the
// organization is revoked through the one revoke transaction. Each writes one
// credential_revoked audit row (actor type admin, the reason) and one revoked
// lifecycle event; the browser family row and both members record
// org_deprovisioned; the caller's binding's credentials go last; an expired
// row and another organization's row stay as they are.
func TestCredentialRevokeOrganization_RevokesEveryLiveCredentialOnARealDatabase(t *testing.T) {
	f := newOutboxFixture(t)
	otherOrg := "00000000-0000-4000-8000-000000000099"
	create := func(id, org string, expires time.Time, provenance storage.CredentialIssuanceProvenance, binding string) string {
		input := storage.CredentialCreateInput{
			CredentialID: id, OrgID: org, Name: id, TokenPrefix: "fcacr_abcdefghij", TokenHash: sha256Hex(id),
			RepositoryScopes: []string{"*"}, Scopes: []string{"context:read"}, ActorID: "seed",
			ExpiresAt: &expires, IssuanceProvenance: provenance, WorkloadBindingID: binding,
		}
		if provenance == storage.CredentialIssuanceProvenanceDeviceAuthorization {
			input.ApprovingUserID = "web-user-1"
		}
		credential, err := f.store.CreateCredential(f.ctx, input)
		require.NoError(t, err)
		return credential.CredentialID
	}
	for binding, role := range map[string]string{"wlb_projector_read": "read", "wlb_go_api_revoke": "revoke"} {
		_, err := f.db.ExecContext(f.ctx, `
INSERT INTO acr.workload_bindings (binding_id, org_id, trust_domain, namespace, service_account_name, service_account_uid, role, repository_scopes, created_at, created_by)
VALUES ($1, $2, 'cluster.local', 'ns', 'sa-' || $1, 'uid-' || $1, $3, '["*"]'::jsonb, now(), 'test_actor')`, binding, credentialTestOrgID, role)
		require.NoError(t, err)
	}
	hour := time.Now().UTC().Add(time.Hour)
	browser := browserFamilyMembers(t, f, "orgrevokefam", 2)
	_, err := f.db.ExecContext(f.ctx, `
INSERT INTO acr.browser_token_families (family_id, org_id, client_id, created_at, expires_at)
VALUES ('orgrevokefam', $1, 'client-1', now(), now() + interval '1 day')`, credentialTestOrgID)
	require.NoError(t, err)
	native := create("cred_orgrevnative01", credentialTestOrgID, hour, storage.CredentialIssuanceProvenanceDeviceAuthorization, "")
	bearer := create("cred_orgrevbearer01", credentialTestOrgID, hour, "", "")
	workload := create("cred_orgrevworkld01", credentialTestOrgID, hour, storage.CredentialIssuanceProvenanceWorkloadExchange, "wlb_projector_read")
	caller := create("cred_orgrevcaller01", credentialTestOrgID, hour, storage.CredentialIssuanceProvenanceWorkloadExchange, "wlb_go_api_revoke")
	// No issuance path writes the operator class or a NULL class
	// (credentiallifecycle input.go: ClientClassOperator; migration 0063
	// keeps NULL for rows that predate the class column), so both are
	// inserted as the CHECK admits them.
	raw := func(id, class string) string {
		_, err := f.db.ExecContext(f.ctx, `
INSERT INTO acr.client_credentials (credential_id, org_id, name, token_prefix, token_hash, repository_scopes, scopes, created_at, expires_at, client_class)
VALUES ($1, $2, $1, 'fcacr_abcdefghij', $3, '["*"]'::jsonb, '["context:read"]'::jsonb, now(), $4, NULLIF($5, ''))`, id, credentialTestOrgID, sha256Hex(id), hour, class)
		require.NoError(t, err)
		return id
	}
	operator := raw("cred_orgrevoperat01", "operator")
	legacy := raw("cred_orgrevlegacy01", "")
	expiring := create("cred_orgrevexpire01", credentialTestOrgID, time.Now().UTC().Add(2*time.Second), "", "")
	other := create("cred_orgrevother001", otherOrg, hour, "", "")
	time.Sleep(3 * time.Second)

	result, err := f.service.RevokeOrganization(f.ctx, credentialTestOrgID, "wlb_go_api_revoke", caller, "usr_platform_admin_1", auth.OrganizationRevokeReasonDeleted, auth.OrganizationRevokeBatch)

	require.NoError(t, err)
	require.Equal(t, auth.OrganizationRevocation{Revoked: 6, CallerRevoked: 1, More: false}, result, "the second browser member goes with its family")
	for _, id := range append(append([]string(nil), browser...), native, bearer, workload, operator, legacy, caller) {
		current, err := f.store.GetByID(f.ctx, credentialTestOrgID, id)
		require.NoError(t, err)
		require.NotNil(t, current.RevokedAt, id)
		var n int
		var actorType, metadata string
		require.NoError(t, f.db.QueryRowContext(f.ctx, `
SELECT count(*), coalesce(max(actor_type), ''), coalesce(max(metadata::text), '')
FROM acr.audit_events WHERE org_id = $1 AND action = 'credential_revoked' AND resource_id = $2`, credentialTestOrgID, id).Scan(&n, &actorType, &metadata))
		require.Equal(t, 1, n, id)
		// A family sibling's row is written by the family revoke, whose audit
		// actor type is fixed to user (both stores, since CHAOS-12124); the
		// credential the organization revoke named carries admin.
		wantActor := auth.OnBehalfActorAdmin
		if id == browser[1] {
			wantActor = "user"
		}
		require.Equal(t, wantActor, actorType, id)
		var details map[string]any
		require.NoError(t, json.Unmarshal([]byte(metadata), &details), id)
		if id == browser[0] || id == browser[1] {
			require.Equal(t, "org_deprovisioned", details["family_revoke_reason"], id)
		} else {
			require.Equal(t, auth.OrganizationRevokeReasonDeleted, details["reason"], id)
		}
		rows := lifecycleRows(t, f.ctx, f.db, id)
		require.Equal(t, "acr_credential.revoked", rows[len(rows)-1].EventType, id)
		revoked := 0
		for _, row := range rows {
			if row.EventType == "acr_credential.revoked" {
				revoked++
			}
		}
		require.Equal(t, 1, revoked, "%s: revoked events", id)
	}
	var familyReason string
	require.NoError(t, f.db.QueryRowContext(f.ctx, `SELECT coalesce(revoked_reason, '') FROM acr.browser_token_families WHERE family_id = 'orgrevokefam'`).Scan(&familyReason))
	require.Equal(t, "org_deprovisioned", familyReason)
	for org, id := range map[string]string{credentialTestOrgID: expiring, otherOrg: other} {
		current, err := f.store.GetByID(f.ctx, org, id)
		require.NoError(t, err)
		require.Nil(t, current.RevokedAt, id)
	}

	again, err := f.service.RevokeOrganization(f.ctx, credentialTestOrgID, "wlb_go_api_revoke", caller, "usr_platform_admin_1", auth.OrganizationRevokeReasonDeleted, auth.OrganizationRevokeBatch)
	require.NoError(t, err)
	require.Equal(t, auth.OrganizationRevocation{}, again, "a repeat revokes nothing")
}
