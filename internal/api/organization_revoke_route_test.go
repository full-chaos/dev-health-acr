package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// The organization revoke route: POST /internal/v1/organization/credentials/revoke
// revokes every live credential of the caller's organization for a workload
// holding credential_lifecycle:revoke_organization, a disabled organization
// included.

const (
	orgRevokeRoutePath    = "/internal/v1/organization/credentials/revoke"
	orgRevokeRouteScope   = "credential_lifecycle:revoke_organization"
	orgRevokeRouteBinding = "wlb_go_api_revoke"
	orgRevokeRouteOther   = "7c1d2e3f-4a5b-4c6d-8e7f-9a0b1c2d3e4f"
)

type orgRevokeAnswer struct {
	SchemaVersion string `json:"schema_version"`
	Revoked       int    `json:"revoked"`
	CallerRevoked int    `json:"caller_revoked"`
	More          bool   `json:"more"`
}

// issue mints a credential through the app's own service and returns its token.
func (h *orgGoneApp) issue(t *testing.T, request auth.CreateCredentialRequest) string {
	t.Helper()
	if request.OrgID == "" {
		request.OrgID = h.orgID
	}
	if request.RepositoryScopes == nil {
		request.RepositoryScopes = []string{"*"}
	}
	if request.Scopes == nil {
		request.Scopes = []string{auth.ScopeContextRead}
	}
	request.Name, request.CreatedBy = "seed", "seed_actor"
	issued, err := h.app.credentialService.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	return issued.Token
}

func (h *orgGoneApp) workloadToken(t *testing.T, binding string, scopes []string) string {
	t.Helper()
	expires := h.now.Add(10 * time.Minute)
	return h.issue(t, auth.CreateCredentialRequest{Scopes: scopes, ExpiresAt: &expires,
		IssuanceProvenance: storage.CredentialIssuanceProvenanceWorkloadExchange, WorkloadBindingID: binding})
}

func (h *orgGoneApp) revokeOrganization(token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, orgRevokeRoutePath, bytes.NewReader([]byte(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	return h.serve(request)
}

func (h *orgGoneApp) liveIn(t *testing.T, org string) int {
	t.Helper()
	listed, err := h.credentials.List(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, credential := range listed {
		if credential.RevokedAt == nil && (credential.ExpiresAt == nil || credential.ExpiresAt.After(h.now)) {
			live++
		}
	}
	return live
}

const orgRevokeRouteBody = `{"schema_version":"credential_revoke_organization_request.v1","actor_id":"usr_platform_admin_1","reason":"organization_disabled"}`

// OR1: for a live or a disabled organization the route revokes every live
// credential of the organization (native device, org bearer, another
// binding's workload, the caller last), answers the counts, writes one
// credential_revoked audit row with the reason each, and leaves another
// organization alone.
func TestOrganizationRevoke_routeRevokesEveryCredentialOfTheOrganization(t *testing.T) {
	for name, mode := range map[string]orgOpsMode{"live": orgOpsLive, "disabled": orgOpsDisabled} {
		t.Run(name, func(t *testing.T) {
			h := newOrgGoneApp(t, orgOpsLive)
			h.issue(t, auth.CreateCredentialRequest{})
			h.workloadToken(t, "wlb_projector_read", []string{auth.ScopeContextRead})
			h.issue(t, auth.CreateCredentialRequest{OrgID: orgRevokeRouteOther})
			caller := h.workloadToken(t, orgRevokeRouteBinding, []string{"credential_lifecycle:revoke", orgRevokeRouteScope})
			h.ops.set(mode)

			response := h.revokeOrganization(caller, orgRevokeRouteBody)

			if response.Code != http.StatusOK {
				t.Fatalf("organization revoke: %d %s, want 200", response.Code, response.Body.String())
			}
			var answer orgRevokeAnswer
			if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
				t.Fatal(err)
			}
			want := orgRevokeAnswer{SchemaVersion: "credential_revoke_organization_response.v1", Revoked: 3, CallerRevoked: 1}
			if answer != want {
				t.Fatalf("answer = %+v, want %+v", answer, want)
			}
			if got := h.liveIn(t, h.orgID); got != 0 {
				t.Fatalf("live credentials in the organization = %d, want 0", got)
			}
			if got := h.liveIn(t, orgRevokeRouteOther); got != 1 {
				t.Fatalf("live credentials of the other organization = %d, want 1", got)
			}
			rows := 0
			for _, event := range h.app.runtime.Audit.(*memory.AuditStore).Events() {
				if event.Action == "credential_revoked" {
					if event.OrgID != h.orgID || event.Metadata["reason"] != "organization_disabled" || event.ActorType != "admin" {
						t.Fatalf("credential_revoked row %+v, want org %s, reason organization_disabled, actor admin", event, h.orgID)
					}
					rows++
				}
			}
			if rows != 4 {
				t.Fatalf("credential_revoked rows = %d, want 4", rows)
			}
		})
	}
}

// OR2: a gone organization's caller is 401 at authentication; nothing is revoked.
func TestOrganizationRevoke_aGoneOrganizationIsRefused(t *testing.T) {
	h := newOrgGoneApp(t, orgOpsLive)
	caller := h.workloadToken(t, orgRevokeRouteBinding, []string{"credential_lifecycle:revoke", orgRevokeRouteScope})
	h.ops.set(orgOpsGone)

	response := h.revokeOrganization(caller, orgRevokeRouteBody)

	assertErrorResponse(t, response, http.StatusUnauthorized, "invalid_token")
	if got := h.liveIn(t, h.orgID); got != 2 {
		t.Fatalf("live credentials = %d, want 2 (nothing revoked)", got)
	}
}

// OR3: the route serves only a workload with the organization revoke scope,
// and only a closed body; every refusal revokes nothing.
func TestOrganizationRevoke_refusesEveryOtherCaller(t *testing.T) {
	for name, tc := range map[string]struct {
		token  func(*testing.T, *orgGoneApp) string
		body   string
		status int
		code   string
	}{
		"on-behalf revoke scope only": {func(t *testing.T, h *orgGoneApp) string {
			return h.workloadToken(t, orgRevokeRouteBinding, []string{"credential_lifecycle:revoke"})
		}, orgRevokeRouteBody, http.StatusForbidden, "insufficient_scope"},
		"user-class credential": {func(_ *testing.T, h *orgGoneApp) string { return h.token }, orgRevokeRouteBody, http.StatusForbidden, "credential_class_refused"},
		"unknown reason": {func(t *testing.T, h *orgGoneApp) string {
			return h.workloadToken(t, orgRevokeRouteBinding, []string{orgRevokeRouteScope})
		}, `{"schema_version":"credential_revoke_organization_request.v1","actor_id":"usr_1","reason":"organization_suspended"}`, http.StatusBadRequest, "invalid_request"},
		"an organization in the body": {func(t *testing.T, h *orgGoneApp) string {
			return h.workloadToken(t, orgRevokeRouteBinding, []string{orgRevokeRouteScope})
		}, `{"schema_version":"credential_revoke_organization_request.v1","actor_id":"usr_1","reason":"organization_disabled","org_id":"` + orgRevokeRouteOther + `"}`, http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newOrgGoneApp(t, orgOpsLive)
			token := tc.token(t, h)
			before := h.liveIn(t, h.orgID)

			response := h.revokeOrganization(token, tc.body)

			assertErrorResponse(t, response, tc.status, tc.code)
			if got := h.liveIn(t, h.orgID); got != before {
				t.Fatalf("live credentials = %d, want %d (nothing revoked)", got, before)
			}
		})
	}
}

// OR4: with a batch of one and a new caller credential per call (go-api
// exchanges per call), the loop ends: each call answers more until the last,
// which revokes every caller credential and answers more false.
func TestOrganizationRevoke_theBatchLoopEndsWithABatchOfOne(t *testing.T) {
	h := newOrgGoneApp(t, orgOpsLive)
	h.app.organizationRevokeBatch = 1
	h.issue(t, auth.CreateCredentialRequest{})
	h.workloadToken(t, "wlb_projector_read", []string{auth.ScopeContextRead})
	calls := 0
	for {
		calls++
		if calls > 5 {
			t.Fatal("the loop did not end")
		}
		response := h.revokeOrganization(h.workloadToken(t, orgRevokeRouteBinding, []string{orgRevokeRouteScope}), orgRevokeRouteBody)
		var answer orgRevokeAnswer
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &answer) != nil {
			t.Fatalf("call %d: %d %s", calls, response.Code, response.Body.String())
		}
		if answer.Revoked > 1 {
			t.Fatalf("call %d revoked %d, want at most the batch of 1", calls, answer.Revoked)
		}
		if !answer.More {
			if answer.CallerRevoked != calls {
				t.Fatalf("last call revoked %d caller credentials, want %d", answer.CallerRevoked, calls)
			}
			break
		}
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (one per credential the caller's binding did not mint; the third also ends the loop)", calls)
	}
	if got := h.liveIn(t, h.orgID); got != 0 {
		t.Fatalf("live credentials = %d, want 0", got)
	}
}
