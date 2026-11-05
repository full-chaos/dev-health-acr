package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The on-behalf revoke on a real database: one credential_revoked audit row
// with the actor type and id the Auth Control Plane named, one lifecycle
// revoked event (actor user + ref), and nothing for a refused request.
func TestCredentialRevokeOnBehalf_RecordsTheActorOnARealDatabase(t *testing.T) {
	f := newOutboxFixture(t)
	create := func(id, resource string) string {
		expires := time.Now().UTC().Add(time.Hour)
		input := storage.CredentialCreateInput{
			CredentialID: id, OrgID: credentialTestOrgID, Name: id, TokenPrefix: "fcacr_abcdefghij", TokenHash: sha256Hex(id),
			RepositoryScopes: []string{"acme/widgets"}, Scopes: []string{"context:read"}, ActorID: "web-user-1",
			ExpiresAt: &expires, IssuanceProvenance: storage.CredentialIssuanceProvenanceDeviceAuthorization,
			ApprovingUserID: "web-user-1", Resource: resource,
		}
		if resource != "" {
			input.ClientID = "client-1"
		}
		credential, err := f.store.CreateCredential(f.ctx, input)
		require.NoError(t, err)
		return credential.CredentialID
	}
	auditRows := func(id string) (n int, actorType, actorID string) {
		require.NoError(t, f.db.QueryRowContext(f.ctx, `
SELECT count(*), coalesce(max(actor_type), ''), coalesce(max(actor_id), '')
FROM acr.audit_events WHERE org_id = $1 AND action = 'credential_revoked' AND resource_id = $2`, credentialTestOrgID, id).Scan(&n, &actorType, &actorID))
		return n, actorType, actorID
	}

	for _, cell := range []struct{ id, actorType, actorID string }{
		{"cred_onbehalfadmin1", auth.OnBehalfActorAdmin, "web-admin-7"},
		{"cred_onbehalfuser01", auth.OnBehalfActorUser, "web-user-1"},
	} {
		id := create(cell.id, "https://acr.example/mcp")
		_, err := f.service.RevokeOnBehalf(f.ctx, credentialTestOrgID, id, cell.actorType, cell.actorID)
		require.NoError(t, err)
		n, actorType, actorID := auditRows(id)
		require.Equal(t, 1, n, cell.id)
		require.Equal(t, cell.actorType, actorType, cell.id)
		require.Equal(t, cell.actorID, actorID, cell.id)
		rows := lifecycleRows(t, f.ctx, f.db, id)
		require.Equal(t, "acr_credential.revoked", rows[len(rows)-1].EventType, cell.id)
		require.Equal(t, "user", rows[len(rows)-1].ActorType, cell.id)
		require.Equal(t, cell.actorID, rows[len(rows)-1].ActorRef, cell.id)
		_, err = f.service.RevokeOnBehalf(f.ctx, credentialTestOrgID, id, auth.OnBehalfActorAdmin, "web-admin-7")
		require.ErrorIs(t, err, storage.ErrConflict, cell.id)
		n, _, _ = auditRows(id)
		require.Equal(t, 1, n, "a second revoke wrote a second audit row")
	}

	for _, cell := range []struct{ id, resource, actorType, actorID, reason string }{
		{"cred_onbehalfother1", "https://acr.example/mcp", auth.OnBehalfActorUser, "web-user-2", auth.OnBehalfRefusedNotApprover},
		{"cred_onbehalfunbnd1", "", auth.OnBehalfActorAdmin, "web-admin-7", auth.OnBehalfRefusedNotMCPBound},
	} {
		id := create(cell.id, cell.resource)
		_, err := f.service.RevokeOnBehalf(f.ctx, credentialTestOrgID, id, cell.actorType, cell.actorID)
		var refusal *auth.OnBehalfRefusal
		require.True(t, errors.As(err, &refusal), cell.id)
		require.Equal(t, cell.reason, refusal.Reason, cell.id)
		n, _, _ := auditRows(id)
		require.Zero(t, n, cell.id)
		current, err := f.store.GetByID(f.ctx, credentialTestOrgID, id)
		require.NoError(t, err)
		require.Nil(t, current.RevokedAt, cell.id)
	}
	// A browser credential (bound to the issuer) is not MCP-bound.
	browser := browserFamilyMembers(t, f, "onbehalfbrowser", 1)[0]
	_, err := f.service.RevokeOnBehalf(f.ctx, credentialTestOrgID, browser, auth.OnBehalfActorAdmin, "web-admin-7")
	var browserRefusal *auth.OnBehalfRefusal
	require.True(t, errors.As(err, &browserRefusal))
	require.Equal(t, auth.OnBehalfRefusedNotMCPBound, browserRefusal.Reason)
	n, _, _ := auditRows(browser)
	require.Zero(t, n)
	_, err = f.service.RevokeOnBehalf(f.ctx, "00000000-0000-4000-8000-000000000099", "cred_onbehalfadmin1", auth.OnBehalfActorAdmin, "web-admin-7")
	var refusal *auth.OnBehalfRefusal
	require.True(t, errors.As(err, &refusal))
	require.Equal(t, auth.OnBehalfRefusedUnknownCredential, refusal.Reason, "another organization's id is unknown")
}
