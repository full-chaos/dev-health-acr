package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-go/pep"
	"github.com/full-chaos/dev-health-go/pep/pepfake"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// approvalTestOrg is a uuid: the decision point's organization resource key
// pattern takes no underscore.
const approvalTestOrg = "3f9a1c77-2e4b-4d10-b5c8-6a7e9d0b1c23"

// CHAOS-13586: with ACR_DEVICE_APPROVAL_PDP=enforce the device approval route
// answers a policy decision point deny or a missing approver reference with
// 403 access_denied and an outage with 503; a preview never asks.

type routeDecider struct {
	next  pep.Decider
	calls int
}

func (d *routeDecider) Decide(ctx context.Context, req pep.Request) pep.Decision {
	d.calls++
	return d.next.Decide(ctx, req)
}

func (d *routeDecider) DecideBatch(ctx context.Context, reqs []pep.Request) []pep.Decision {
	d.calls += len(reqs)
	return d.next.DecideBatch(ctx, reqs)
}

func signApproverReference(t *testing.T, private ed25519.PrivateKey, now time.Time, assertion string) string {
	t.Helper()
	digest := sha256.Sum256([]byte(assertion))
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "web-approver+jwt", "kid": "current"})
	claims, _ := json.Marshal(map[string]any{
		"action": "acr.credential.issue", "aud": "auth-service", "iss": "https://web.example.test", "sub": "user_123", "org_id": approvalTestOrg,
		"sid": "ses_fQw-UpG0T3qKYw5bnC1PEA", "impersonating": false, "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(30 * time.Second).Unix(),
		"jti": "reference_" + base64.RawURLEncoding.EncodeToString(digest[:6]), "req": base64.RawURLEncoding.EncodeToString(digest[:]),
	})
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(input)))
}

func approvalDecisionApp(t *testing.T, decider pep.Decider) (*App, ed25519.PrivateKey, time.Time) {
	t.Helper()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := auth.NewApprovalDecisions(auth.ApprovalDecisionEnforce, decider, nil)
	if err != nil {
		t.Fatal(err)
	}
	hostedTestApprovalDecisions = decisions
	deviceApprovalExtraClaims = map[string]any{"org_id": approvalTestOrg}
	t.Cleanup(func() { hostedTestApprovalDecisions, deviceApprovalExtraClaims = nil, nil })
	app, _ := newHostedTestAppWithWebAssertions(t, nil, nil, nil, nil, nil, verifier)
	return app, private, now
}

func startApproval(t *testing.T, app *App) contractsv1.DeviceApprovalRequest {
	t.Helper()
	created := httptest.NewRecorder()
	app.Handler().ServeHTTP(created, deviceRequest(t, http.MethodPost, "/api/v1/oauth/device_authorization", contractsv1.DeviceAuthorizationRequest{SchemaVersion: contractsv1.DeviceAuthorizationRequestSchema}))
	var authorization contractsv1.DeviceAuthorizationResponse
	if err := json.NewDecoder(created.Body).Decode(&authorization); err != nil {
		t.Fatal(err)
	}
	return contractsv1.DeviceApprovalRequest{SchemaVersion: contractsv1.DeviceApprovalRequestSchema, UserCode: authorization.UserCode, RepositoryScopes: []string{hostedTestRepository}}
}

func approveWith(t *testing.T, app *App, private ed25519.PrivateKey, now time.Time, approval contractsv1.DeviceApprovalRequest, jti string, withReference bool) *httptest.ResponseRecorder {
	t.Helper()
	request := deviceApprovalRequest(t, now, private, approval, jti)
	if withReference {
		request.Header.Set(auth.ApproverReferenceHeader, signApproverReference(t, private, now, request.Header.Get(auth.WebAssertionHeader)))
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func allowOrg1(t *testing.T) pep.Decider {
	t.Helper()
	return &referenceAllow{}
}

// referenceAllow allows acr.credential.issue in approvalTestOrg for any reference the
// route forwards (the reference is minted per request).
type referenceAllow struct{}

func (referenceAllow) Decide(ctx context.Context, req pep.Request) pep.Decision {
	if req.Action != "acr.credential.issue" || req.ResourceType != "organization" || req.ResourceKey != approvalTestOrg || req.OrganizationID != approvalTestOrg {
		return pepfake.New().Decide(ctx, req)
	}
	return pepfake.New(pepfake.Rule{PrincipalReference: req.PrincipalReference, Action: req.Action, ResourceType: req.ResourceType,
		ResourceKey: req.ResourceKey, OrganizationID: req.OrganizationID, TenantScope: approvalTestOrg}).Decide(ctx, req)
}

func (r referenceAllow) DecideBatch(ctx context.Context, reqs []pep.Request) []pep.Decision {
	out := make([]pep.Decision, len(reqs))
	for i, req := range reqs {
		out[i] = r.Decide(ctx, req)
	}
	return out
}

func pdpDenyingRoute(t *testing.T) pep.Decider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"allowed":false,"decision_id":"dec_EXAMPLE0000000000000000001","expires_at":%q,"organization_id":%q,"policy_revision":42,"principal_id":"prn_EXAMPLE0000000000000001","reason_code":"no_membership","schema_version":"authorization_decision.v1"}`,
			time.Now().UTC().Add(30*time.Second).Format(time.RFC3339), approvalTestOrg)
	}))
	t.Cleanup(server.Close)
	client, err := pep.New(pep.Config{BaseURL: server.URL, AllowPlainHTTP: true, HTTPClient: server.Client(), ClientService: "dev-health-acr-api"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestApprovalDecisionRoute_allowApproves(t *testing.T) {
	decider := &routeDecider{next: allowOrg1(t)}
	app, private, now := approvalDecisionApp(t, decider)

	response := approveWith(t, app, private, now, startApproval(t, app), "approval_allow", true)

	if response.Code != http.StatusOK || decider.calls != 1 {
		t.Fatalf("status = %d calls = %d (%s); want 200 and 1", response.Code, decider.calls, response.Body.String())
	}
}

func TestApprovalDecisionRoute_denyAndMissingReferenceAre403(t *testing.T) {
	for name, test := range map[string]struct {
		decider   pep.Decider
		reference bool
		calls     int
	}{
		"deny":              {pdpDenyingRoute(t), true, 1},
		"missing reference": {allowOrg1(t), false, 0},
	} {
		t.Run(name, func(t *testing.T) {
			decider := &routeDecider{next: test.decider}
			app, private, now := approvalDecisionApp(t, decider)

			response := approveWith(t, app, private, now, startApproval(t, app), "approval_"+strings.ReplaceAll(name, " ", "_"), test.reference)

			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"access_denied"`) || decider.calls != test.calls {
				t.Fatalf("status = %d calls = %d body = %s; want 403 access_denied and %d", response.Code, decider.calls, response.Body.String(), test.calls)
			}
		})
	}
}

func TestApprovalDecisionRoute_outageIs503(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	t.Cleanup(server.Close)
	client, err := pep.New(pep.Config{BaseURL: server.URL, AllowPlainHTTP: true, HTTPClient: server.Client(), ClientService: "dev-health-acr-api"})
	if err != nil {
		t.Fatal(err)
	}
	app, private, now := approvalDecisionApp(t, client)

	response := approveWith(t, app, private, now, startApproval(t, app), "approval_outage", true)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s; want 503", response.Code, response.Body.String())
	}
}

func TestApprovalDecisionRoute_previewNeverAsks(t *testing.T) {
	decider := &routeDecider{next: pepfake.New()}
	app, private, now := approvalDecisionApp(t, decider)
	approval := startApproval(t, app)
	preview := contractsv1.DeviceApprovalPreviewRequest{SchemaVersion: contractsv1.DeviceApprovalPreviewRequestSchema, UserCode: approval.UserCode}

	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, deviceApprovalRequest(t, now, private, preview, "preview_1"))

	if response.Code != http.StatusOK || decider.calls != 0 {
		t.Fatalf("preview status = %d calls = %d; want 200 and 0", response.Code, decider.calls)
	}
}

func TestApprovalDecisionRoute_consentDenyIs403(t *testing.T) {
	if oauthConsentStatus[auth.OAuthConsentCodeDenied] != http.StatusForbidden {
		t.Fatalf("consent access_denied status = %d; want 403", oauthConsentStatus[auth.OAuthConsentCodeDenied])
	}
}
