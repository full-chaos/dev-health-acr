package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

const (
	onBehalfApprover = "usr_approver_1"
	onBehalfOther    = "usr_other_2"
	onBehalfAdmin    = "usr_admin_3"
	onBehalfResource = "https://acr.example.test/mcp"
)

type onBehalfRig struct {
	service *Service
	store   *storage.CredentialLifecycle
	audit   *memory.AuditStore
}

func newOnBehalfRig(t *testing.T) onBehalfRig {
	t.Helper()
	now := time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, now, audit)
	return onBehalfRig{service: newTestService(t, store, audit, now), store: store, audit: audit}
}

// mcpCredential stores a device-redeemed credential bound to resource (empty
// for an unbound native-device row), approved by approver.
func (r onBehalfRig) mcpCredential(t *testing.T, id, resource string) string {
	t.Helper()
	input := storage.CredentialCreateInput{
		CredentialID: id, OrgID: deviceFlowTestOrgID, Name: "mcp client", TokenPrefix: "fcacr_" + id[:8], TokenHash: HashToken("fcacr_" + id + "_secret_value_for_hash"),
		RepositoryScopes: []string{"owner/repo"}, Scopes: []string{ScopeContextRead}, ActorID: onBehalfApprover,
		IssuanceProvenance: storage.CredentialIssuanceProvenanceDeviceAuthorization, ApprovingUserID: onBehalfApprover,
		Resource: resource,
	}
	if resource != "" {
		input.ClientID = "acrc_0123456789abcdef0123456789abcdef"
	}
	created, err := r.store.CreateCredential(context.Background(), input)
	require.NoError(t, err)
	return created.CredentialID
}

func (r onBehalfRig) revokedRows(id string) []storage.AuditEvent {
	var rows []storage.AuditEvent
	for _, event := range r.audit.Events() {
		if event.Action == storage.AuditActionCredentialRevoked && event.ResourceID == id {
			rows = append(rows, event)
		}
	}
	return rows
}

func (r onBehalfRig) live(t *testing.T, id string) bool {
	t.Helper()
	credential, err := r.store.GetByID(context.Background(), deviceFlowTestOrgID, id)
	require.NoError(t, err)
	return credential.RevokedAt == nil
}

func TestRevokeOnBehalf_RevokesAnMCPBoundCredentialThroughTheSharedPathAndNamesTheActor(t *testing.T) {
	for _, cell := range []struct{ actorType, actorID string }{
		{OnBehalfActorAdmin, onBehalfAdmin},
		{OnBehalfActorUser, onBehalfApprover},
	} {
		t.Run(cell.actorType, func(t *testing.T) {
			rig := newOnBehalfRig(t)
			id := rig.mcpCredential(t, "cred_mcpbound00001", onBehalfResource)
			revoked, err := rig.service.RevokeOnBehalf(context.Background(), deviceFlowTestOrgID, id, cell.actorType, cell.actorID)
			require.NoError(t, err)
			require.NotNil(t, revoked.RevokedAt)
			require.False(t, rig.live(t, id))
			rows := rig.revokedRows(id)
			require.Len(t, rows, 1, "one credential_revoked audit row")
			require.Equal(t, cell.actorType, rows[0].ActorType)
			require.Equal(t, cell.actorID, rows[0].ActorID)
			outbox := selfOutbox(t, rig.audit, id)
			require.Equal(t, "acr_credential.revoked", outbox[len(outbox)-1].EventType)
			require.Equal(t, "user", outbox[len(outbox)-1].Payload.ActorType, "the lifecycle event keeps its closed actor vocabulary")
			require.Equal(t, cell.actorID, outbox[len(outbox)-1].Payload.ActorRef)
		})
	}
}

func TestRevokeOnBehalf_RefusesEveryCaseItMustNotRevokeAndLeavesTheRowLive(t *testing.T) {
	for name, cell := range map[string]struct {
		resource, actorType, actorID string
		otherOrg                     bool
		want                         string
	}{
		"user who did not approve":   {onBehalfResource, OnBehalfActorUser, onBehalfOther, false, OnBehalfRefusedNotApprover},
		"admin on an unbound row":    {"", OnBehalfActorAdmin, onBehalfAdmin, false, OnBehalfRefusedNotResourceBound},
		"approver on an unbound row": {"", OnBehalfActorUser, onBehalfApprover, false, OnBehalfRefusedNotResourceBound},
		"admin in another org":       {onBehalfResource, OnBehalfActorAdmin, onBehalfAdmin, true, OnBehalfRefusedUnknownCredential},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newOnBehalfRig(t)
			id := rig.mcpCredential(t, "cred_mcpbound00002", cell.resource)
			org := deviceFlowTestOrgID
			if cell.otherOrg {
				org = "8f7c2e4a-1b3d-4c5e-9f6a-7b8c9d0e1f2a"
			}
			_, err := rig.service.RevokeOnBehalf(context.Background(), org, id, cell.actorType, cell.actorID)
			require.ErrorIs(t, err, ErrRevokeOnBehalfRefused)
			var refusal *OnBehalfRefusal
			require.True(t, errors.As(err, &refusal))
			require.Equal(t, cell.want, refusal.Reason)
			require.True(t, rig.live(t, id))
			require.Empty(t, rig.revokedRows(id))
		})
	}
}

func TestRevokeOnBehalf_UnknownIDsAreRefusedLikeAnyOtherCredential(t *testing.T) {
	rig := newOnBehalfRig(t)
	for _, id := range []string{"cred_doesnotexist01", "", "not a credential id", "cred_" + string(make([]byte, 300))} {
		_, err := rig.service.RevokeOnBehalf(context.Background(), deviceFlowTestOrgID, id, OnBehalfActorAdmin, onBehalfAdmin)
		var refusal *OnBehalfRefusal
		require.True(t, errors.As(err, &refusal), "id %q: %v", id, err)
		require.Equal(t, OnBehalfRefusedUnknownCredential, refusal.Reason)
	}
}

func TestRevokeOnBehalf_ASecondRevokeIsAConflictAndWritesNoSecondRow(t *testing.T) {
	rig := newOnBehalfRig(t)
	id := rig.mcpCredential(t, "cred_mcpbound00003", onBehalfResource)
	_, err := rig.service.RevokeOnBehalf(context.Background(), deviceFlowTestOrgID, id, OnBehalfActorAdmin, onBehalfAdmin)
	require.NoError(t, err)
	_, err = rig.service.RevokeOnBehalf(context.Background(), deviceFlowTestOrgID, id, OnBehalfActorUser, onBehalfApprover)
	require.ErrorIs(t, err, storage.ErrConflict)
	require.Len(t, rig.revokedRows(id), 1)
	// A user who did not approve learns nothing, not even that it is revoked.
	_, err = rig.service.RevokeOnBehalf(context.Background(), deviceFlowTestOrgID, id, OnBehalfActorUser, onBehalfOther)
	require.ErrorIs(t, err, ErrRevokeOnBehalfRefused)
}

func TestRevokeOnBehalf_RefusesAnActorItCannotName(t *testing.T) {
	rig := newOnBehalfRig(t)
	id := rig.mcpCredential(t, "cred_mcpbound00004", onBehalfResource)
	for name, cell := range map[string]struct{ actorType, actorID string }{
		"operator type":  {"operator", onBehalfAdmin},
		"system type":    {"system", onBehalfAdmin},
		"credential":     {"credential", id},
		"empty type":     {"", onBehalfAdmin},
		"upper admin":    {"ADMIN", onBehalfAdmin},
		"empty actor id": {OnBehalfActorAdmin, ""},
		"blank actor id": {OnBehalfActorUser, "   "},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := rig.service.RevokeOnBehalf(context.Background(), deviceFlowTestOrgID, id, cell.actorType, cell.actorID)
			require.ErrorIs(t, err, ErrInvalidCredential)
			require.True(t, rig.live(t, id))
		})
	}
}

func TestRevokeAsOperator_StillRecordsTheOperatorAfterTheSharedPathMoved(t *testing.T) {
	rig := newOnBehalfRig(t)
	id := rig.mcpCredential(t, "cred_mcpbound00005", onBehalfResource)
	_, err := rig.service.RevokeAsOperator(context.Background(), deviceFlowTestOrgID, id, "op-1", "lost laptop")
	require.NoError(t, err)
	rows := rig.revokedRows(id)
	require.Len(t, rows, 1)
	require.Equal(t, "operator", rows[0].ActorType)
	require.Equal(t, "lost laptop", rows[0].Metadata["reason"])
}

// The lost-token case end to end: an MCP-bound bearer that the authenticator
// admits for its resource is refused on its next call once an admin revoked
// it by id.
func TestRevokeOnBehalf_TheRevokedMCPBearerIsRefusedOnItsNextCall(t *testing.T) {
	rig := newOnBehalfRig(t)
	token, err := GenerateToken()
	require.NoError(t, err)
	created, err := rig.store.CreateCredential(context.Background(), storage.CredentialCreateInput{
		CredentialID: "cred_mcpbound00006", OrgID: deviceFlowTestOrgID, Name: "mcp client", TokenPrefix: DisplayPrefix(token), TokenHash: HashToken(token),
		RepositoryScopes: []string{"owner/repo"}, Scopes: []string{ScopeContextRead}, ActorID: onBehalfApprover,
		IssuanceProvenance: storage.CredentialIssuanceProvenanceDeviceAuthorization, ApprovingUserID: onBehalfApprover,
		Resource: onBehalfResource, ClientID: "acrc_0123456789abcdef0123456789abcdef",
	})
	require.NoError(t, err)
	authenticator, err := NewAuthenticator(rig.store, rig.audit, AuthenticatorOptions{
		Now: func() time.Time { return time.Date(2026, 11, 4, 18, 1, 0, 0, time.UTC) }, Limiter: NewMemoryLimiter(time.Minute, 1000, 1000),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = authenticator.Close() })
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	call := func() int {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/context-fabric/investigations", nil)
		request.RemoteAddr = "192.0.2.20:4000"
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set(ResourceHeader, onBehalfResource)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	require.Equal(t, http.StatusOK, call(), "the MCP-bound bearer works for its resource before the revoke")
	_, err = rig.service.RevokeOnBehalf(context.Background(), deviceFlowTestOrgID, created.CredentialID, OnBehalfActorAdmin, onBehalfAdmin)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, call(), "the revoked bearer is refused on its next call")
}
