package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	"github.com/full-chaos/dev-health-go/authverify"
)

// A credential of an organization Dev Health answers as disabled (the 404
// coded acr_organization_disabled) is refused at use and at every mint path,
// and nothing new is issued for it; the revoke binding still reaches it, and a
// disabled organization is never read as gone.

func (h *orgGoneApp) assertOrganizationDisabledRefusal(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	assertErrorResponse(t, response, http.StatusUnauthorized, "invalid_token")
	for _, event := range h.app.runtime.Audit.(*memory.AuditStore).Events() {
		if event.Action == "credential_auth_denied" && event.ResourceID == h.credentialID && event.Metadata["reason"] == "organization_disabled" {
			return
		}
	}
	t.Fatal("no credential_auth_denied audit with reason organization_disabled")
}

// startDevice starts a native device authorization and returns its codes.
func (h *orgGoneApp) startDevice(t *testing.T) contractsv1.DeviceAuthorizationResponse {
	t.Helper()
	created := h.serve(deviceRequest(t, http.MethodPost, "/api/v1/oauth/device_authorization", contractsv1.DeviceAuthorizationRequest{SchemaVersion: contractsv1.DeviceAuthorizationRequestSchema}))
	if created.Code != http.StatusOK {
		t.Fatalf("device authorization: %d %s", created.Code, created.Body.String())
	}
	var authorization contractsv1.DeviceAuthorizationResponse
	if err := json.NewDecoder(created.Body).Decode(&authorization); err != nil {
		t.Fatal(err)
	}
	return authorization
}

func (h *orgGoneApp) approveDevice(t *testing.T, userCode, jti string) *httptest.ResponseRecorder {
	t.Helper()
	approval := contractsv1.DeviceApprovalRequest{SchemaVersion: contractsv1.DeviceApprovalRequestSchema, UserCode: userCode, RepositoryScopes: []string{"*"}}
	return h.serve(deviceApprovalRequest(t, h.now, h.private, approval, jti))
}

func (h *orgGoneApp) liveCredentials(t *testing.T) int {
	t.Helper()
	listed, err := h.credentials.List(context.Background(), h.orgID)
	if err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, credential := range listed {
		if credential.RevokedAt == nil {
			live++
		}
	}
	return live
}

// DA: a disabled organization's credential is 401 organization_disabled at
// authentication, before the principal access read.
func TestOrganizationDisabled_capabilitiesRefusesTheCredential(t *testing.T) {
	h := newOrgGoneApp(t, orgOpsDisabled)

	response := h.capabilities(h.token)

	h.assertOrganizationDisabledRefusal(t, response)
	if calls := h.access.reset(); len(calls) != 0 {
		t.Fatalf("principal access read %d times, want 0 (refused at authentication)", len(calls))
	}
}

// DB: no successor is minted for a disabled organization.
func TestOrganizationDisabled_selfRotateRefusesWithoutARotation(t *testing.T) {
	h := newOrgGoneApp(t, orgOpsDisabled)

	response := h.rotate(t, h.token)

	h.assertOrganizationDisabledRefusal(t, response)
	if got := h.rotations.Load(); got != 0 {
		t.Fatalf("store Rotate calls = %d, want 0", got)
	}
}

// DC: the ack that would keep a credential is refused.
func TestOrganizationDisabled_ackRefuses(t *testing.T) {
	h := newOrgGoneApp(t, orgOpsDisabled)

	h.assertOrganizationDisabledRefusal(t, h.ack(t, h.token, h.credentialID))
}

// DD: self revoke stays open.
func TestOrganizationDisabled_selfRevokeStillRevokes(t *testing.T) {
	h := newOrgGoneApp(t, orgOpsDisabled)

	if response := h.revoke(t, h.token); response.Code != http.StatusOK {
		t.Fatalf("self revoke: %d %s, want 200", response.Code, response.Body.String())
	}
	if h.storedCredential(t).RevokedAt == nil {
		t.Fatal("the credential is not revoked")
	}
}

// DE: bearer authentication keeps the cached read: a disable is seen past the
// 30 s cache, and a mint path sees it at once.
func TestOrganizationDisabled_bearerReadIsBoundedByTheCacheAndMintReadsFresh(t *testing.T) {
	h := newOrgGoneApp(t, orgOpsLive)
	if warm := h.capabilities(h.token); warm.Code != http.StatusOK {
		t.Fatalf("warm capabilities: %d %s", warm.Code, warm.Body.String())
	}
	h.ops.set(orgOpsDisabled)
	h.ops.advance(29 * time.Second)

	if inside := h.capabilities(h.token); inside.Code != http.StatusOK {
		t.Fatalf("capabilities inside the cache: %d %s, want 200", inside.Code, inside.Body.String())
	}
	h.assertOrganizationDisabledRefusal(t, h.rotate(t, h.token))
	h.ops.advance(2 * time.Second)

	h.assertOrganizationDisabledRefusal(t, h.capabilities(h.token))
}

// DF: the native device approval of a disabled or gone organization is
// refused before anything is approved, even with a warm cached answer (the
// approval reads fresh); an outage fails closed.
func TestOrganizationDisabled_deviceApprovalIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		mode   orgOpsMode
		status int
		code   string
	}{
		"disabled": {orgOpsDisabled, http.StatusBadRequest, "invalid_request"},
		"gone":     {orgOpsGone, http.StatusBadRequest, "invalid_request"},
		"5xx":      {orgOpsServerError, http.StatusServiceUnavailable, "upstream_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newOrgGoneApp(t, orgOpsLive)
			authorization := h.startDevice(t)
			if warm := h.capabilities(h.token); warm.Code != http.StatusOK {
				t.Fatalf("warm capabilities: %d %s", warm.Code, warm.Body.String())
			}
			h.ops.set(tc.mode)
			before := h.ops.hits.Load()

			response := h.approveDevice(t, authorization.UserCode, "approval_disabled_"+name)

			assertErrorResponse(t, response, tc.status, tc.code)
			if got := h.ops.hits.Load() - before; got != 1 {
				t.Fatalf("Dev Health reads = %d, want 1 (one fresh read)", got)
			}
			h.ops.set(orgOpsLive)
			if pending := h.serve(deviceTokenRequest(t, authorization.DeviceCode)); pending.Code != http.StatusBadRequest || !strings.Contains(pending.Body.String(), "authorization_pending") {
				t.Fatalf("redeem after the refused approval: %d %s, want authorization_pending", pending.Code, pending.Body.String())
			}
		})
	}
}

// DG: an approval made while the organization was live is not redeemed once
// it is disabled or gone, even with a warm cached answer (the redemption reads
// fresh); an outage fails closed; nothing is issued.
func TestOrganizationDisabled_deviceRedeemIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		mode   orgOpsMode
		status int
		body   string
	}{
		"disabled": {orgOpsDisabled, http.StatusBadRequest, "access_denied"},
		"gone":     {orgOpsGone, http.StatusBadRequest, "access_denied"},
		"5xx":      {orgOpsServerError, http.StatusServiceUnavailable, "upstream_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newOrgGoneApp(t, orgOpsLive)
			authorization := h.startDevice(t)
			if approved := h.approveDevice(t, authorization.UserCode, "approval_live_"+name); approved.Code != http.StatusOK {
				t.Fatalf("approval while live: %d %s", approved.Code, approved.Body.String())
			}
			live := h.liveCredentials(t)
			if warm := h.capabilities(h.token); warm.Code != http.StatusOK {
				t.Fatalf("warm capabilities: %d %s", warm.Code, warm.Body.String())
			}
			h.ops.set(tc.mode)

			response := h.serve(deviceTokenRequest(t, authorization.DeviceCode))

			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.body) {
				t.Fatalf("redeem: %d %s, want %d %s", response.Code, response.Body.String(), tc.status, tc.body)
			}
			if got := h.liveCredentials(t); got != live {
				t.Fatalf("live credentials = %d, want %d (nothing issued)", got, live)
			}
		})
	}
}

// consentCode runs one OAuth consent for the browser client and returns the
// recorder and its decoded body.
func (b *orgGoneBrowser) consent(t *testing.T, jti string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	all := []string{"*"}
	return serveConsent(b.app, consentRequest(t, map[string]any{"action": "approve", "handle": authorizeToConsent(t, b.app, oauthTestAuthorizeQuery(auth.DevBrowserClientID)), "repository_scopes": all}, all, jti))
}

func (b *orgGoneBrowser) exchange(t *testing.T, code string) (int, oauthTokenAnswer, string) {
	t.Helper()
	return postOAuthForm(t, b.app, OAuthTokenPath, url.Values{"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {oauthTestRedirect}, "client_id": {auth.DevBrowserClientID}, "code_verifier": {strings.Repeat("v", 50)}})
}

// DH: the OAuth consent of a disabled organization is refused (no code), and a
// code granted while it was live is not exchanged once it is disabled.
func TestOrganizationDisabled_oauthConsentAndExchangeAreRefused(t *testing.T) {
	t.Run("consent", func(t *testing.T) {
		b := newOrgGoneBrowser(t)
		b.ops.set(orgOpsDisabled)

		recorder, body := b.consent(t, "r_disabled")

		if recorder.Code != http.StatusBadRequest || body["error"] != auth.OAuthConsentCodeInvalid || body["redirect_url"] != nil {
			t.Fatalf("consent of a disabled organization: %d %s, want 400 invalid_request and no redirect", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(b.logs.String(), `"step":"consent","outcome":"organization_disabled"`) {
			t.Fatalf("no consent step line with outcome organization_disabled:\n%s", b.logs.String())
		}
	})
	t.Run("exchange", func(t *testing.T) {
		b := newOrgGoneBrowser(t)
		recorder, body := b.consent(t, "r_live")
		redirectURL, _ := body["redirect_url"].(string)
		redirect, err := url.Parse(redirectURL)
		if recorder.Code != http.StatusOK || err != nil || redirect.Query().Get("code") == "" {
			t.Fatalf("consent while live: %d %s", recorder.Code, recorder.Body.String())
		}
		b.ops.set(orgOpsDisabled)

		status, answer, raw := b.exchange(t, redirect.Query().Get("code"))

		if status != http.StatusBadRequest || answer.Error != "invalid_grant" || answer.AccessToken != "" {
			t.Fatalf("exchange for a disabled organization: %d %s, want 400 invalid_grant", status, raw)
		}
		if !strings.Contains(b.logs.String(), `"step":"token","outcome":"organization_disabled"`) {
			t.Fatalf("no token step line with outcome organization_disabled:\n%s", b.logs.String())
		}
	})
}

// DI: a disabled organization's browser family cannot refresh: invalid_grant
// organization_disabled, no rotation, and the family is revoked.
func TestOrganizationDisabled_browserRefreshRevokesTheFamily(t *testing.T) {
	b := newOrgGoneBrowser(t)
	b.ops.set(orgOpsDisabled)

	status, answer, raw := b.refreshWith(t, b.login.RefreshToken)

	if status != http.StatusBadRequest || answer.Error != "invalid_grant" {
		t.Fatalf("refresh of a disabled organization: %d %s, want 400 invalid_grant", status, raw)
	}
	if !strings.Contains(b.logs.String(), `"step":"token","outcome":"organization_disabled"`) {
		t.Fatalf("no token step line with outcome organization_disabled:\n%s", b.logs.String())
	}
	if got := b.refresh.rotations.Load(); got != 0 {
		t.Fatalf("RotateBrowserRefresh calls = %d, want 0", got)
	}
	b.ops.set(orgOpsLive)
	if code := b.read(b.login.AccessToken); code != http.StatusUnauthorized {
		t.Fatalf("the family's access token after the refusal: %d, want 401 (family revoked)", code)
	}
}

// DJ: the live_orgs revoke binding still binds a disabled organization (the
// revoke reaches it); a gone one stays unbound.
func TestOrganizationDisabled_liveOrgsRevokeBindingStillBinds(t *testing.T) {
	ops := newFakeOrgOps(t, orgOpsDisabled)
	bindings := memory.NewWorkloadBindingStore()
	identity := authverify.SubjectIdentity{TrustDomain: "cluster.local", Namespace: "dev-health", ServiceAccountName: "go-api", ServiceAccountUID: "uid-1"}
	bindings.Put(storage.WorkloadBindingKey{TrustDomain: identity.TrustDomain, Namespace: identity.Namespace, ServiceAccountName: identity.ServiceAccountName, ServiceAccountUID: identity.ServiceAccountUID},
		storage.WorkloadBinding{BindingID: "wlb_go_api_revoke", OrgScope: storage.WorkloadOrgScopeLiveOrgs, Role: "revoke", RepositoryScopes: []string{}})
	resolver, err := auth.NewGrantResolver(bindings, OrganizationPresenceFor(ops.client))
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithWorkloadOrgSelector(context.Background(), orgGoneTestOrg)

	grant, err := resolver.Resolve(ctx, identity)

	if err != nil || grant.OrgID != orgGoneTestOrg || !equalStrings(grant.GrantedScopes, auth.RoleScopes("revoke")) {
		t.Fatalf("disabled organization: grant %+v, err %v; want the revoke grant in the organization", grant, err)
	}
	ops.set(orgOpsGone)
	if _, err := resolver.Resolve(ctx, identity); err == nil {
		t.Fatal("gone organization: bound, want not bound")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// DK: the on-behalf revoke serves a disabled organization's revoke workload
// (that is how its credentials are revoked); a gone organization stays 401.
func TestOrganizationDisabled_onBehalfRevokeStillServesTheRevokeWorkload(t *testing.T) {
	for name, tc := range map[string]struct {
		mode   orgOpsMode
		status int
		live   bool
	}{
		"disabled": {orgOpsDisabled, http.StatusNoContent, false},
		"gone":     {orgOpsGone, http.StatusUnauthorized, true},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newOnBehalfRouteRig(t, storage.CredentialClientClassWorkload, revokeScopes())
			ops := newFakeOrgOps(t, tc.mode)
			authenticator, err := auth.NewAuthenticator(countingClassStore{
				lookups: rig.lookups,
				classCredentialStore: classCredentialStore{row: contractsv1.ClientCredential{
					CredentialID: "revoke_caller", OrgID: onBehalfRouteOrg, Scopes: revokeScopes(), RepositoryScopes: []string{hostedTestRepository},
					Provenance: contractsv1.CredentialProvenance{ClientClass: string(storage.CredentialClientClassWorkload)},
				}},
			}, nil, auth.AuthenticatorOptions{Now: time.Now, OrgPresence: OrganizationPresenceFor(ops.client)})
			if err != nil {
				t.Fatal(err)
			}
			rig.app.authenticator = authenticator
			id := rig.seed(t, onBehalfRouteOrg, "cred_routedisab01", onBehalfRouteResource)

			response := rig.post(id, onBehalfBody(contractsv1.CredentialRevokeOnBehalfActorAdmin, "usr_admin_9"), &rig.token)

			if response.Code != tc.status {
				t.Fatalf("on-behalf revoke: %d %s, want %d", response.Code, response.Body.String(), tc.status)
			}
			if got := rig.live(t, onBehalfRouteOrg, id); got != tc.live {
				t.Fatalf("credential live = %v, want %v", got, tc.live)
			}
		})
	}
}
