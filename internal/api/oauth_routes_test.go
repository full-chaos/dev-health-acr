package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

const (
	oauthTestIssuer   = "https://acr.example.test"
	oauthTestResource = "https://mcp.example.test/mcp"
	oauthTestRedirect = "http://127.0.0.1:4711/callback"
)

func newOAuthTestApp(t *testing.T, oauthRuntime *OAuthRuntime, withWebAssertions bool) (*App, *bytes.Buffer, error) {
	t.Helper()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	audit := memory.NewAuditStore()
	credentials := newMemoryCredentialLifecycle(t, audit, now)
	devices, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentials, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	if oauthRuntime != nil && oauthRuntime.Store == nil {
		oauthRuntime.Store = memory.NewOAuthStore(clock)
	}
	var verifier *auth.WebAssertionVerifier
	if withWebAssertions {
		public, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		verifier, err = auth.NewWebAssertionVerifier(auth.WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: clock})
		if err != nil {
			t.Fatal(err)
		}
	}
	manager, err := limits.NewManager(limits.Options{Now: clock, PerOrgConcurrency: 4, Policies: limits.PolicySet{
		Auth: limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 100},
	}})
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second}, Dependencies{
		Capabilities: StaticCapabilitiesProvider{Now: clock, Value: hostedCapabilities()}, Limits: manager, Now: clock, WebAssertions: verifier,
		Runtime: &RuntimeDependencies{
			Credentials: credentials, Audit: audit, Entitlements: EntitlementFunc(func(context.Context, string, string) (bool, error) { return true, nil }),
			Assembler: noopAssembler{}, Evidence: noopEvidenceStore{},
			DeviceAuthorizations: devices, DeviceVerificationURL: "https://web.example.test/acr/device",
			DeviceAuthorizationLimiter: NewDeviceAuthorizationLimiter(ClockFunc(clock)),
			ReadinessChecks:            exactRuntimeChecks(),
			OAuth:                      oauthRuntime,
		},
	}, slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return app, logs, err
}

func TestOAuthRoutesRequireWebApprovalAndStayUnregisteredWhenOff(t *testing.T) {
	if _, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, false); !errors.Is(err, ErrOAuthRequiresWebApproval) {
		t.Fatalf("OAuth without web assertions: err = %v, want ErrOAuthRequiresWebApproval", err)
	}
	app, _, err := newOAuthTestApp(t, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{OAuthAuthorizationServerMetadataPath, OAuthAuthorizePath} {
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s without OAuth: status %d, want 404", path, recorder.Code)
		}
	}
}

func TestOAuthMetadataDocument(t *testing.T) {
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, OAuthAuthorizationServerMetadataPath, nil))
	var metadata map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"issuer": oauthTestIssuer, "authorization_endpoint": oauthTestIssuer + "/authorize", "token_endpoint": oauthTestIssuer + "/token",
		"registration_endpoint": oauthTestIssuer + "/register", "authorization_response_iss_parameter_supported": true,
	}
	if _, present := metadata["client_id_metadata_document_supported"]; present {
		t.Error("metadata advertises client ID metadata documents, which this server does not accept")
	}
	for key, value := range want {
		if metadata[key] != value {
			t.Errorf("metadata %s = %v, want %v", key, metadata[key], value)
		}
	}
	for key, value := range map[string]string{"code_challenge_methods_supported": "S256", "grant_types_supported": "authorization_code", "token_endpoint_auth_methods_supported": "none", "response_types_supported": "code"} {
		list, _ := metadata[key].([]any)
		if len(list) != 1 || list[0] != value {
			t.Errorf("metadata %s = %v, want [%s]", key, metadata[key], value)
		}
	}
}

func TestOAuthConsentPageEscapesClientNameAndIsLockedDown(t *testing.T) {
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	registration, _ := json.Marshal(map[string]any{"client_name": `<script>alert(1)</script>`, "redirect_uris": []string{oauthTestRedirect}})
	request := httptest.NewRequest(http.MethodPost, OAuthRegisterPath, bytes.NewReader(registration))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	var registered struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&registered); err != nil || recorder.Code != http.StatusCreated {
		t.Fatalf("register: %d %v", recorder.Code, err)
	}
	digest := sha256.Sum256([]byte(strings.Repeat("v", 50)))
	query := url.Values{
		"response_type": {"code"}, "client_id": {registered.ClientID}, "redirect_uri": {oauthTestRedirect},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"S256"}, "state": {"st"},
	}
	recorder = httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, OAuthAuthorizePath+"?"+query.Encode(), nil))
	page := recorder.Body.String()
	if recorder.Code != http.StatusOK {
		t.Fatalf("authorize: %d %s", recorder.Code, page)
	}
	if strings.Contains(page, "<script>alert(1)") || !strings.Contains(page, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatal("the client name reached the consent page unescaped")
	}
	header := recorder.Header()
	csp := header.Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'none'", "frame-ancestors 'none'", "form-action 'self'", "connect-src 'self'", "script-src 'nonce-"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP %q lacks %q", csp, directive)
		}
	}
	if header.Get("X-Frame-Options") != "DENY" || header.Get("Cache-Control") != "no-store" || header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("consent page headers = %v", header)
	}
	if strings.Contains(page, "?handle=") || strings.Contains(recorder.Header().Get("Location"), "handle") {
		t.Fatal("the browser handle reached a URL")
	}
	if strings.Contains(logs.String(), registered.ClientID) || strings.Contains(logs.String(), "st\"") {
		t.Fatal("the OAuth line carries a client id or state value")
	}
}

func TestOAuthTokenClientIDFromBasicAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name   string
		form   string
		basic  func(*http.Request)
		wantID string
		wantOK bool
	}{
		{"form only", "client_id=a", nil, "a", true},
		{"basic only", "", func(r *http.Request) { r.SetBasicAuth("https%3A%2F%2Fc.example%2Fx", "") }, "https://c.example/x", true},
		{"both agree", "client_id=a", func(r *http.Request) { r.SetBasicAuth("a", "") }, "a", true},
		{"both disagree", "client_id=a", func(r *http.Request) { r.SetBasicAuth("b", "") }, "", false},
		{"secret sent", "", func(r *http.Request) { r.SetBasicAuth("a", "secret") }, "", false},
		{"bearer header", "client_id=a", func(r *http.Request) { r.Header.Set("Authorization", "Bearer x") }, "", false},
		{"two headers", "client_id=a", func(r *http.Request) {
			r.Header.Add("Authorization", "Basic YTo=")
			r.Header.Add("Authorization", "Basic YTo=")
		}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, OAuthTokenPath, strings.NewReader(tc.form))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tc.basic != nil {
				tc.basic(request)
			}
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}
			id, ok := tokenClientID(request)
			if id != tc.wantID || ok != tc.wantOK {
				t.Fatalf("tokenClientID = %q %v, want %q %v", id, ok, tc.wantID, tc.wantOK)
			}
		})
	}
}

func TestOAuthTokenRefusesNonFormAndRepeatedParameters(t *testing.T) {
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	for name, build := range map[string]func() *http.Request{
		"json body": func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, OAuthTokenPath, strings.NewReader(`{"grant_type":"authorization_code"}`))
			r.Header.Set("Content-Type", "application/json")
			return r
		},
		"repeated code": func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, OAuthTokenPath, strings.NewReader("grant_type=authorization_code&code=a&code=b"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		},
		"query parameters": func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, OAuthTokenPath+"?code=a", strings.NewReader("grant_type=authorization_code"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		},
	} {
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, build())
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"invalid_request"`) {
			t.Errorf("%s: %d %s, want 400 invalid_request", name, recorder.Code, recorder.Body.String())
		}
	}
	if count := strings.Count(logs.String(), `"step":"token","outcome":"invalid_request"`); count != 3 {
		t.Fatalf("token invalid_request lines = %d, want 3", count)
	}
}

func TestOAuthConsentChecksAreRateLimitedPerIP(t *testing.T) {
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[int]int{}
	for range 70 {
		request := httptest.NewRequest(http.MethodPost, OAuthConsentPath, strings.NewReader("handle=unknown"))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, request)
		statuses[recorder.Code]++
	}
	if statuses[http.StatusBadRequest] != 60 || statuses[http.StatusTooManyRequests] != 10 {
		t.Fatalf("70 consent checks from one IP: statuses %v, want 60x400 then 10x429", statuses)
	}
	if count := strings.Count(logs.String(), `"step":"consent","outcome":"rate_limited"`); count != 10 {
		t.Fatalf("consent rate_limited lines = %d, want 10", count)
	}
}
