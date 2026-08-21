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
)

type fakeWorkloadTokenExchanger struct {
	result auth.WorkloadTokenExchangeResult
	err    error
	// lastSubjectToken/lastScope record what the handler passed through,
	// so a test can assert the form body was parsed correctly.
	lastSubjectToken string
	lastScope        []string
}

func (f *fakeWorkloadTokenExchanger) Exchange(_ context.Context, subjectToken string, requestedScope []string) (auth.WorkloadTokenExchangeResult, error) {
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
	exchanger := &fakeWorkloadTokenExchanger{result: auth.WorkloadTokenExchangeResult{
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
	exchanger := &fakeWorkloadTokenExchanger{result: auth.WorkloadTokenExchangeResult{AccessToken: "fcacr_x", ExpiresIn: 60}}
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
	app := newTokenExchangeTestApp(t, &fakeWorkloadTokenExchanger{err: auth.ErrSubjectTokenInvalid})
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
	app := newTokenExchangeTestApp(t, &fakeWorkloadTokenExchanger{err: auth.ErrWorkloadBindingNotFound})
	form := url.Values{}
	form.Set("grant_type", contractsv1.TokenExchangeGrantType)
	form.Set("subject_token", "jwt")
	form.Set("subject_token_type", contractsv1.TokenExchangeSubjectTokenTypeJWT)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, tokenExchangeFormRequest(form))
	assertTokenExchangeError(t, response, http.StatusBadRequest, contractsv1.TokenExchangeErrorInvalidGrant)
}

func TestTokenExchange_scopeNotGrantedMapsToInvalidScope(t *testing.T) {
	app := newTokenExchangeTestApp(t, &fakeWorkloadTokenExchanger{err: auth.ErrScopeNotGranted})
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
