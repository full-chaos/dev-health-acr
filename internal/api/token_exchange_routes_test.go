package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-go/authverify"
)

type fakeWorkloadTokenExchanger struct {
	result authverify.WorkloadTokenExchangeResult
	err    error
	// lastSubjectToken/lastScope record what the handler passed through,
	// so a test can assert the form body was parsed correctly.
	lastSubjectToken string
	lastScope        []string
}

func (f *fakeWorkloadTokenExchanger) Exchange(_ context.Context, subjectToken string, requestedScope []string) (authverify.WorkloadTokenExchangeResult, error) {
	f.lastSubjectToken = subjectToken
	f.lastScope = requestedScope
	return f.result, f.err
}

func newTokenExchangeTestApp(t *testing.T, exchanger WorkloadTokenExchanger) *App {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	app, _ := newHostedTestAppWithWebAssertions(t, nil, nil, nil, nil, nil, verifier)
	app.runtime.WorkloadTokenExchange = exchanger
	return app
}

func tokenExchangeFormRequest(form url.Values) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/token", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

func TestTokenExchange_happyPathReturnsAnOAuthTokenResponse(t *testing.T) {
	exchanger := &fakeWorkloadTokenExchanger{result: authverify.WorkloadTokenExchangeResult{
		AccessToken: "fcacr_workload_test", ExpiresIn: 600, Scope: []string{auth.ScopeContextRead, auth.ScopeEvidenceRead},
	}}
	app := newTokenExchangeTestApp(t, exchanger)
	form := url.Values{}
	form.Set("grant_type", contractsv1.TokenExchangeGrantType)
	form.Set("subject_token", "the-subject-jwt")
	form.Set("subject_token_type", contractsv1.TokenExchangeSubjectTokenTypeJWT)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, tokenExchangeFormRequest(form))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var decoded contractsv1.TokenExchangeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.AccessToken != "fcacr_workload_test" || decoded.TokenType != "Bearer" || decoded.ExpiresIn != 600 {
		t.Fatalf("decoded response = %#v", decoded)
	}
	if decoded.IssuedTokenType != contractsv1.TokenExchangeAccessTokenType {
		t.Fatalf("issued_token_type = %q", decoded.IssuedTokenType)
	}
	if exchanger.lastSubjectToken != "the-subject-jwt" {
		t.Fatalf("subject token passed to Exchange = %q", exchanger.lastSubjectToken)
	}
}

func TestTokenExchange_narrowedScopeIsForwardedToExchange(t *testing.T) {
	exchanger := &fakeWorkloadTokenExchanger{result: authverify.WorkloadTokenExchangeResult{AccessToken: "fcacr_x", ExpiresIn: 60}}
	app := newTokenExchangeTestApp(t, exchanger)
	form := url.Values{}
	form.Set("grant_type", contractsv1.TokenExchangeGrantType)
	form.Set("subject_token", "jwt")
	form.Set("subject_token_type", contractsv1.TokenExchangeSubjectTokenTypeJWT)
	form.Set("scope", "context:read evidence:read")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, tokenExchangeFormRequest(form))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if len(exchanger.lastScope) != 2 || exchanger.lastScope[0] != "context:read" || exchanger.lastScope[1] != "evidence:read" {
		t.Fatalf("scope passed to Exchange = %#v", exchanger.lastScope)
	}
}

func TestTokenExchange_deviceCodeGrantOnTheSameEndpointIsUnaffected(t *testing.T) {
	// A JSON body on the same path must still dispatch to the device-code
	// handler, never to the workload exchanger.
	exchanger := &fakeWorkloadTokenExchanger{}
	app := newTokenExchangeTestApp(t, exchanger)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, deviceRequest(t, http.MethodPost, "/api/v1/oauth/token", contractsv1.DeviceTokenRequest{
		SchemaVersion: contractsv1.DeviceTokenRequestSchema, GrantType: contractsv1.DeviceCodeGrantType, DeviceCode: "nonexistent",
	}))
	if exchanger.lastSubjectToken != "" {
		t.Fatal("the workload exchanger must never be invoked for a JSON device-code request")
	}
	// The device code does not exist, so this is expected to fail --
	// what matters is which handler it failed IN.
	var errorResponse contractsv1.OAuthDeviceErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &errorResponse); err != nil {
		t.Fatalf("expected an OAuthDeviceErrorResponse body, got: %s", response.Body.String())
	}
}

func TestTokenExchange_missingGrantTypeIsUnsupportedGrantType(t *testing.T) {
	app := newTokenExchangeTestApp(t, &fakeWorkloadTokenExchanger{})
	form := url.Values{}
	form.Set("subject_token", "jwt")
	form.Set("subject_token_type", contractsv1.TokenExchangeSubjectTokenTypeJWT)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, tokenExchangeFormRequest(form))
	assertTokenExchangeError(t, response, http.StatusBadRequest, contractsv1.TokenExchangeErrorUnsupportedGrantType)
}

func TestTokenExchange_missingSubjectTokenIsInvalidRequest(t *testing.T) {
	app := newTokenExchangeTestApp(t, &fakeWorkloadTokenExchanger{})
	form := url.Values{}
	form.Set("grant_type", contractsv1.TokenExchangeGrantType)
	form.Set("subject_token_type", contractsv1.TokenExchangeSubjectTokenTypeJWT)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, tokenExchangeFormRequest(form))
	assertTokenExchangeError(t, response, http.StatusBadRequest, contractsv1.TokenExchangeErrorInvalidRequest)
}

func TestTokenExchange_invalidSubjectTokenMapsToInvalidGrant(t *testing.T) {
	app := newTokenExchangeTestApp(t, &fakeWorkloadTokenExchanger{err: authverify.ErrSubjectTokenInvalid})
	form := url.Values{}
	form.Set("grant_type", contractsv1.TokenExchangeGrantType)
	form.Set("subject_token", "jwt")
	form.Set("subject_token_type", contractsv1.TokenExchangeSubjectTokenTypeJWT)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, tokenExchangeFormRequest(form))
	assertTokenExchangeError(t, response, http.StatusBadRequest, contractsv1.TokenExchangeErrorInvalidGrant)
}

func TestTokenExchange_unresolvedBindingMapsToInvalidGrantNotUnauthorizedClient(t *testing.T) {
	// A disabled/unresolved binding must not be distinguishable from an
	// invalid subject token to the caller -- see
	// storageGrantResolver.Resolve's own doc comment.
	app := newTokenExchangeTestApp(t, &fakeWorkloadTokenExchanger{err: authverify.ErrWorkloadBindingNotFound})
	form := url.Values{}
	form.Set("grant_type", contractsv1.TokenExchangeGrantType)
	form.Set("subject_token", "jwt")
	form.Set("subject_token_type", contractsv1.TokenExchangeSubjectTokenTypeJWT)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, tokenExchangeFormRequest(form))
	assertTokenExchangeError(t, response, http.StatusBadRequest, contractsv1.TokenExchangeErrorInvalidGrant)
}

func TestTokenExchange_scopeNotGrantedMapsToInvalidScope(t *testing.T) {
	app := newTokenExchangeTestApp(t, &fakeWorkloadTokenExchanger{err: authverify.ErrScopeNotGranted})
	form := url.Values{}
	form.Set("grant_type", contractsv1.TokenExchangeGrantType)
	form.Set("subject_token", "jwt")
	form.Set("subject_token_type", contractsv1.TokenExchangeSubjectTokenTypeJWT)
	form.Set("scope", "context:admin")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, tokenExchangeFormRequest(form))
	assertTokenExchangeError(t, response, http.StatusBadRequest, contractsv1.TokenExchangeErrorInvalidScope)
}

func TestTokenExchange_infrastructureFailureIsUpstreamUnavailable(t *testing.T) {
	app := newTokenExchangeTestApp(t, &fakeWorkloadTokenExchanger{err: context.DeadlineExceeded})
	form := url.Values{}
	form.Set("grant_type", contractsv1.TokenExchangeGrantType)
	form.Set("subject_token", "jwt")
	form.Set("subject_token_type", contractsv1.TokenExchangeSubjectTokenTypeJWT)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, tokenExchangeFormRequest(form))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want 503", response.Code, response.Body.String())
	}
}

func TestTokenExchange_unconfiguredExchangerDegradesTo503(t *testing.T) {
	app := newTokenExchangeTestApp(t, nil)
	form := url.Values{}
	form.Set("grant_type", contractsv1.TokenExchangeGrantType)
	form.Set("subject_token", "jwt")
	form.Set("subject_token_type", contractsv1.TokenExchangeSubjectTokenTypeJWT)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, tokenExchangeFormRequest(form))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want 503 for an unconfigured deployment", response.Code, response.Body.String())
	}
}

// TestTokenExchange_succeedsWithoutWebAssertionsWhileDeviceCodeGrantStaysFailClosed
// is CHAOS-4071's own regression proof. deviceRuntimeHandler used to wrap
// the ENTIRE POST /api/v1/oauth/token route (app.go), so a deployment with
// a.authenticator.WebAssertions() == nil -- the normal case for one that
// enables RFC 8693 workload token exchange but never configures the
// entirely unrelated browser/device-login web-assertion JWKS -- 503'd
// every token-exchange call before WorkloadTokenExchange.Exchange was ever
// reached. Every prior token-exchange test in this file used
// newTokenExchangeTestApp, which ALWAYS configures WebAssertions -- that
// handler-level gap (testing handleTokenExchange's own nil check in
// isolation, never the route wrapper actually gating it) is exactly what
// let this ship unnoticed. This test goes through app.Handler() with
// newHostedTestApp's no-WebAssertions app, the real route wiring, and also
// re-confirms the device-code grant and device_authorization endpoint stay
// fail-closed exactly as before -- WebAssertions really is required for
// those.
func TestTokenExchange_succeedsWithoutWebAssertionsWhileDeviceCodeGrantStaysFailClosed(t *testing.T) {
	exchanger := &fakeWorkloadTokenExchanger{result: authverify.WorkloadTokenExchangeResult{
		AccessToken: "fcacr_workload_test", ExpiresIn: 600, Scope: []string{auth.ScopeContextRead},
	}}
	app, _ := newHostedTestApp(t, nil, nil, nil, nil, nil)
	t.Cleanup(func() { _ = app.Close() })
	app.runtime.WorkloadTokenExchange = exchanger

	form := url.Values{}
	form.Set("grant_type", contractsv1.TokenExchangeGrantType)
	form.Set("subject_token", "the-subject-jwt")
	form.Set("subject_token_type", contractsv1.TokenExchangeSubjectTokenTypeJWT)
	exchangeResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(exchangeResponse, tokenExchangeFormRequest(form))
	if exchangeResponse.Code != http.StatusOK {
		t.Fatalf("token-exchange grant status = %d body=%s, want 200 despite WebAssertions being unconfigured", exchangeResponse.Code, exchangeResponse.Body.String())
	}
	if exchanger.lastSubjectToken != "the-subject-jwt" {
		t.Fatalf("Exchange was not actually invoked: lastSubjectToken = %q", exchanger.lastSubjectToken)
	}

	// The device-code (JSON) grant on the SAME endpoint must stay
	// fail-closed: WebAssertions really is required for it.
	deviceResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(deviceResponse, deviceRequest(t, http.MethodPost, "/api/v1/oauth/token", contractsv1.DeviceTokenRequest{
		SchemaVersion: contractsv1.DeviceTokenRequestSchema, GrantType: contractsv1.DeviceCodeGrantType, DeviceCode: "irrelevant",
	}))
	assertErrorResponse(t, deviceResponse, http.StatusServiceUnavailable, "upstream_unavailable")

	// device_authorization and device_approval must ALSO stay fail-closed
	// without WebAssertions -- deviceRuntimeHandler is unchanged for both
	// of those routes.
	authorizationResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(authorizationResponse, deviceRequest(t, http.MethodPost, "/api/v1/oauth/device_authorization", contractsv1.DeviceAuthorizationRequest{SchemaVersion: contractsv1.DeviceAuthorizationRequestSchema}))
	assertErrorResponse(t, authorizationResponse, http.StatusServiceUnavailable, "upstream_unavailable")

	approvalResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(approvalResponse, deviceRequest(t, http.MethodPost, "/api/v1/oauth/device_approval", contractsv1.DeviceApprovalRequest{
		SchemaVersion: contractsv1.DeviceApprovalRequestSchema, UserCode: "ABCDEFGH", RepositoryScopes: []string{hostedTestRepository},
	}))
	assertErrorResponse(t, approvalResponse, http.StatusServiceUnavailable, "upstream_unavailable")
}

func assertTokenExchangeError(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, wantCode contractsv1.OAuthTokenExchangeErrorCode) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status = %d body=%s, want %d", response.Code, response.Body.String(), wantStatus)
	}
	var decoded contractsv1.OAuthTokenExchangeErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Error != wantCode {
		t.Fatalf("error = %q, want %q", decoded.Error, wantCode)
	}
}

type selectorRecordingExchanger struct {
	err      error
	called   bool
	selector string
	present  bool
}

func (f *selectorRecordingExchanger) Exchange(ctx context.Context, _ string, _ []string) (authverify.WorkloadTokenExchangeResult, error) {
	f.called = true
	f.selector, f.present = auth.WorkloadOrgSelectorFrom(ctx)
	if f.err != nil {
		return authverify.WorkloadTokenExchangeResult{}, f.err
	}
	return authverify.WorkloadTokenExchangeResult{AccessToken: "fcacr_x", ExpiresIn: 60}, nil
}

func selectorExchangeForm(resources ...string) url.Values {
	form := url.Values{}
	form.Set("grant_type", contractsv1.TokenExchangeGrantType)
	form.Set("subject_token", "jwt")
	form.Set("subject_token_type", contractsv1.TokenExchangeSubjectTokenTypeJWT)
	for _, resource := range resources {
		form.Add("resource", resource)
	}
	return form
}

func exchangeErrorCode(t *testing.T, response *httptest.ResponseRecorder) (int, contractsv1.OAuthTokenExchangeErrorCode) {
	t.Helper()
	var decoded contractsv1.OAuthTokenExchangeErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("body = %s: %v", response.Body.String(), err)
	}
	return response.Code, decoded.Error
}

func TestTokenExchange_resourceSelectorIsForwardedToTheResolverContext(t *testing.T) {
	exchanger := &selectorRecordingExchanger{}
	app := newTokenExchangeTestApp(t, exchanger)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, tokenExchangeFormRequest(selectorExchangeForm("urn:acr:org:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")))
	if response.Code != http.StatusOK || !exchanger.present || exchanger.selector != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("status = %d present = %v selector = %q body=%s", response.Code, exchanger.present, exchanger.selector, response.Body.String())
	}
}

func TestTokenExchange_noResourceSetsNoSelector(t *testing.T) {
	exchanger := &selectorRecordingExchanger{}
	app := newTokenExchangeTestApp(t, exchanger)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, tokenExchangeFormRequest(selectorExchangeForm()))
	if response.Code != http.StatusOK || exchanger.present {
		t.Fatalf("status = %d present = %v selector = %q", response.Code, exchanger.present, exchanger.selector)
	}
}

func TestTokenExchange_malformedResourceSelectorIsInvalidRequest(t *testing.T) {
	const uuid = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	cases := map[string][]string{
		"raw uuid":           {uuid},
		"upper case uuid":    {"urn:acr:org:AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"},
		"wrong prefix":       {"urn:acr:team:" + uuid},
		"https uri":          {"https://acr.example.test/" + uuid},
		"empty after prefix": {"urn:acr:org:"},
		"not a uuid":         {"urn:acr:org:not-a-uuid"},
		"two resources":      {"urn:acr:org:" + uuid, "urn:acr:org:bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"},
		"blank resource":     {" "},
	}
	for name, resources := range cases {
		exchanger := &selectorRecordingExchanger{}
		app := newTokenExchangeTestApp(t, exchanger)
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, tokenExchangeFormRequest(selectorExchangeForm(resources...)))
		status, code := exchangeErrorCode(t, response)
		if status != http.StatusBadRequest || code != contractsv1.TokenExchangeErrorInvalidRequest || exchanger.called {
			t.Fatalf("%s: status = %d code = %q exchange called = %v, want 400 invalid_request and no exchange", name, status, code, exchanger.called)
		}
	}
}

func TestTokenExchange_selectorErrorsMapToTheirOwnCodes(t *testing.T) {
	cases := map[string]struct {
		err  error
		want contractsv1.OAuthTokenExchangeErrorCode
	}{
		"selector required": {auth.ErrWorkloadOrgSelectorRequired, contractsv1.TokenExchangeErrorInvalidRequest},
		"org not bound":     {auth.ErrWorkloadOrgNotBound, contractsv1.TokenExchangeErrorInvalidTarget},
		"binding not found": {authverify.ErrWorkloadBindingNotFound, contractsv1.TokenExchangeErrorInvalidGrant},
	}
	for name, tc := range cases {
		app := newTokenExchangeTestApp(t, &selectorRecordingExchanger{err: tc.err})
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, tokenExchangeFormRequest(selectorExchangeForm()))
		status, code := exchangeErrorCode(t, response)
		if status != http.StatusBadRequest || code != tc.want {
			t.Fatalf("%s: status = %d code = %q, want 400 %q", name, status, code, tc.want)
		}
	}
}

func rawExchangeFormRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/token", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

func TestTokenExchange_presentButEmptyResourceIsInvalidRequest(t *testing.T) {
	base := "grant_type=" + url.QueryEscape(contractsv1.TokenExchangeGrantType) +
		"&subject_token=jwt&subject_token_type=" + url.QueryEscape(contractsv1.TokenExchangeSubjectTokenTypeJWT)
	for name, suffix := range map[string]string{
		"empty value": "&resource=",
		"bare key":    "&resource",
		"empty first": "&resource=&resource=urn:acr:org:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"empty last":  "&resource=urn:acr:org:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa&resource=",
	} {
		exchanger := &selectorRecordingExchanger{}
		app := newTokenExchangeTestApp(t, exchanger)
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, rawExchangeFormRequest(base+suffix))
		status, code := exchangeErrorCode(t, response)
		if status != http.StatusBadRequest || code != contractsv1.TokenExchangeErrorInvalidRequest || exchanger.called {
			t.Fatalf("%s: status = %d code = %q exchange called = %v, want 400 invalid_request and no exchange (a present empty resource is a malformed selector, not an absent one)", name, status, code, exchanger.called)
		}
	}
}

func TestTokenExchange_repeatedResourceIsInvalidRequestEvenWhenIdentical(t *testing.T) {
	const same = "urn:acr:org:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	exchanger := &selectorRecordingExchanger{}
	app := newTokenExchangeTestApp(t, exchanger)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, tokenExchangeFormRequest(selectorExchangeForm(same, same)))
	status, code := exchangeErrorCode(t, response)
	if status != http.StatusBadRequest || code != contractsv1.TokenExchangeErrorInvalidRequest || exchanger.called {
		t.Fatalf("status = %d code = %q exchange called = %v, want 400 invalid_request and no exchange for a repeated resource", status, code, exchanger.called)
	}
}
