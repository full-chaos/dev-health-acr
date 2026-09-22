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
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

const (
	oauthTestIssuer     = "https://acr.example.test"
	oauthTestResource   = "https://mcp.example.test/mcp"
	oauthTestRedirect   = "http://127.0.0.1:4711/callback"
	oauthTestConsentURL = "https://web.example.test/acr/authorize"
	oauthTestOrg        = "org_1"
)

// oauthTestWebKey signs the web assertions of the OAuth route tests.
var oauthTestWebKey ed25519.PrivateKey

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
	if oauthRuntime != nil && oauthRuntime.ConsentURL == "" {
		oauthRuntime.ConsentURL = oauthTestConsentURL
	}
	var verifier *auth.WebAssertionVerifier
	if withWebAssertions {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		oauthTestWebKey = private
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
	for _, consentURL := range []string{"https://web.example.test", "https://web.example.test/acr/authorize?x=1", "http://web.example.test/acr/authorize", "/acr/authorize",
		"https://web.example.test/acr/authorize#", "https://web.example.test/acr/authorize?"} {
		if _, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}, ConsentURL: consentURL}, true); !errors.Is(err, ErrOAuthRequiresConsentURL) {
			t.Fatalf("OAuth with consent URL %q: err = %v, want ErrOAuthRequiresConsentURL", consentURL, err)
		}
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
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, OAuthConsentPath, strings.NewReader(`{}`)))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("%s without OAuth: status %d, want 404", OAuthConsentPath, recorder.Code)
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
		"client_id_metadata_document_supported": false,
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

func TestOAuthMetadataAdvertisesClientIDMetadataDocumentsOnlyWithAFetcher(t *testing.T) {
	for name, fetcher := range map[string]auth.OAuthClientMetadataFetcher{"fetcher": auth.NewClientMetadataFetcher(http.DefaultClient), "none": nil} {
		t.Run(name, func(t *testing.T) {
			app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}, ClientMetadata: fetcher}, true)
			if err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, OAuthAuthorizationServerMetadataPath, nil))
			var metadata map[string]any
			if err := json.NewDecoder(recorder.Body).Decode(&metadata); err != nil {
				t.Fatal(err)
			}
			if got := metadata["client_id_metadata_document_supported"]; got != (fetcher != nil) {
				t.Fatalf("client_id_metadata_document_supported = %v, want %v", got, fetcher != nil)
			}
		})
	}
}

// registerOAuthTestClient registers a dynamic client and returns its ID.
func registerOAuthTestClient(t *testing.T, app *App, name string) string {
	t.Helper()
	registration, _ := json.Marshal(map[string]any{"client_name": name, "redirect_uris": []string{oauthTestRedirect}})
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
	return registered.ClientID
}

func oauthTestAuthorizeQuery(clientID string) url.Values {
	digest := sha256.Sum256([]byte(strings.Repeat("v", 50)))
	return url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {oauthTestRedirect},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"S256"}, "state": {"st"},
	}
}

// authorizeToConsent runs GET /authorize and returns the handle the redirect
// to the web consent page carries.
func authorizeToConsent(t *testing.T, app *App, query url.Values) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, OAuthAuthorizePath+"?"+query.Encode(), nil))
	if recorder.Code != http.StatusFound {
		t.Fatalf("authorize: %d %s", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Scheme+"://"+location.Host+location.Path != oauthTestConsentURL || len(location.Query()) != 1 {
		t.Fatalf("authorize redirected to %s, want %s?handle=...", location, oauthTestConsentURL)
	}
	return location.Query().Get("handle")
}

// consentRequest builds a POST /authorize/consent the web makes for its
// signed-in user, signed with the test web key for these repositories.
func consentRequest(t *testing.T, body any, repositories []string, jti string) *http.Request {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, OAuthConsentPath, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(auth.WebAssertionHeader, signConsentAssertion(t, request, encoded, repositories, jti))
	return request
}

func signConsentAssertion(t *testing.T, request *http.Request, body []byte, repositories []string, jti string) string {
	t.Helper()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	digest := sha256.Sum256(body)
	claims := map[string]any{
		"iss": "https://web.example.test", "aud": "acr-api", "sub": "user_123", "org_id": oauthTestOrg,
		"repository_scopes": repositories, "permissions": []string{auth.WebAssertionPermissionCredentialIssue},
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(30 * time.Second).Unix(), "jti": jti,
		"method": request.Method, "path": request.URL.EscapedPath(), "body_sha256": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "current"})
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(oauthTestWebKey, []byte(input)))
}

func serveConsent(app *App, request *http.Request) (*httptest.ResponseRecorder, map[string]any) {
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	body := map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	return recorder, body
}

func TestOAuthAuthorizeRedirectsToTheWebConsentPage(t *testing.T) {
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	clientID := registerOAuthTestClient(t, app, "Claude Code")
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, OAuthAuthorizePath+"?"+oauthTestAuthorizeQuery(clientID).Encode(), nil))
	if recorder.Code != http.StatusFound {
		t.Fatalf("authorize: %d, want 302", recorder.Code)
	}
	header := recorder.Header()
	if header.Get("Cache-Control") != "no-store" || header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("redirect headers = %v", header)
	}
	location, _ := url.Parse(header.Get("Location"))
	handle := location.Query().Get("handle")
	if location.String() != oauthTestConsentURL+"?handle="+handle || len(handle) != 43 {
		t.Fatalf("Location = %s", location)
	}
	// Nothing to type: no user code, no page body with a code.
	if strings.Contains(recorder.Body.String(), "code") && !strings.Contains(recorder.Body.String(), "Found") {
		t.Fatalf("redirect body = %q", recorder.Body.String())
	}
	for _, secret := range []string{handle, clientID, `"st"`} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the OAuth lines carry %q", secret)
		}
	}
	if !strings.Contains(logs.String(), `"step":"authorize","outcome":"ok","client_kind":"dynamic","scopes":["context:read","evidence:read"],"status":302`) {
		t.Fatalf("authorize ok line missing: %s", logs.String())
	}
}

// An authorize request the server cannot verify never reaches the consent
// page: an unknown client or an unregistered redirect URI gets the problem
// page, a verified redirect URI gets the OAuth error redirect.
func TestOAuthAuthorizeNeverSendsAnUnverifiedRequestToConsent(t *testing.T) {
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	clientID := registerOAuthTestClient(t, app, "c")
	for name, tc := range map[string]struct {
		mutate     func(url.Values)
		wantStatus int
		wantPrefix string
	}{
		"unknown client":           {func(q url.Values) { q.Set("client_id", "acrc_00000000000000000000000000000000") }, http.StatusBadRequest, ""},
		"unregistered redirect":    {func(q url.Values) { q.Set("redirect_uri", "http://127.0.0.1:4712/callback") }, http.StatusBadRequest, ""},
		"redirect on another host": {func(q url.Values) { q.Set("redirect_uri", "https://evil.example/callback") }, http.StatusBadRequest, ""},
		"no pkce":                  {func(q url.Values) { q.Del("code_challenge") }, http.StatusSeeOther, oauthTestRedirect + "?error=invalid_request"},
		"plain pkce":               {func(q url.Values) { q.Set("code_challenge_method", "plain") }, http.StatusSeeOther, oauthTestRedirect + "?error=invalid_request"},
		"other resource":           {func(q url.Values) { q.Set("resource", "https://other.example/mcp") }, http.StatusSeeOther, oauthTestRedirect + "?error=invalid_target"},
		"unknown scope":            {func(q url.Values) { q.Set("scope", "context:admin") }, http.StatusSeeOther, oauthTestRedirect + "?error=invalid_scope"},
		"token response type":      {func(q url.Values) { q.Set("response_type", "token") }, http.StatusSeeOther, oauthTestRedirect + "?error=unsupported_response_type"},
	} {
		t.Run(name, func(t *testing.T) {
			query := oauthTestAuthorizeQuery(clientID)
			tc.mutate(query)
			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, OAuthAuthorizePath+"?"+query.Encode(), nil))
			location := recorder.Header().Get("Location")
			if recorder.Code != tc.wantStatus || strings.HasPrefix(location, oauthTestConsentURL) || (tc.wantPrefix != "" && !strings.HasPrefix(location, tc.wantPrefix)) {
				t.Fatalf("status %d Location %q, want %d %q", recorder.Code, location, tc.wantStatus, tc.wantPrefix)
			}
		})
	}
}

func TestOAuthConsentRouteDecidesOnceForTheSignedInUser(t *testing.T) {
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	clientID := registerOAuthTestClient(t, app, `<script>alert(1)</script>`)
	handle := authorizeToConsent(t, app, oauthTestAuthorizeQuery(clientID))
	all := []string{"*"}

	recorder, body := serveConsent(app, consentRequest(t, map[string]any{"action": "preview", "handle": handle}, all, "p1"))
	if recorder.Code != http.StatusOK || body["client_name"] != `<script>alert(1)</script>` || body["client_self_asserted"] != true ||
		body["client_kind"] != "dynamic" || body["redirect_origin"] != "http://127.0.0.1:4711" || body["resource"] != oauthTestResource ||
		body["expires_at"] != "2026-09-21T12:10:00Z" {
		t.Fatalf("preview: %d %v", recorder.Code, body)
	}

	recorder, body = serveConsent(app, consentRequest(t, map[string]any{"action": "approve", "handle": handle, "repository_scopes": all}, all, "a1"))
	redirect, _ := url.Parse(body["redirect_url"].(string))
	if recorder.Code != http.StatusOK || redirect.Scheme+"://"+redirect.Host+redirect.Path != oauthTestRedirect ||
		redirect.Query().Get("code") == "" || redirect.Query().Get("state") != "st" || redirect.Query().Get("iss") != oauthTestIssuer {
		t.Fatalf("approve: %d %v", recorder.Code, body)
	}
	code := redirect.Query().Get("code")

	// Handle reuse: every later read or decision is 409 already_completed.
	for i, action := range []map[string]any{
		{"action": "approve", "handle": handle, "repository_scopes": all},
		{"action": "deny", "handle": handle},
		{"action": "preview", "handle": handle},
	} {
		recorder, body = serveConsent(app, consentRequest(t, action, all, "r"+string(rune('0'+i))))
		if recorder.Code != http.StatusConflict || body["error"] != "already_completed" || body["redirect_url"] != nil {
			t.Fatalf("reuse %v: %d %v", action, recorder.Code, body)
		}
	}

	// The code the approval issued is the one the token endpoint redeems.
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {oauthTestRedirect}, "client_id": {clientID}, "code_verifier": {strings.Repeat("v", 50)}}
	token := httptest.NewRequest(http.MethodPost, OAuthTokenPath, strings.NewReader(form.Encode()))
	token.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenRecorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(tokenRecorder, token)
	if tokenRecorder.Code != http.StatusOK || !strings.Contains(tokenRecorder.Body.String(), `"access_token":"fcacr_`) {
		t.Fatalf("token: %d %s", tokenRecorder.Code, tokenRecorder.Body.String())
	}

	for _, want := range []string{
		`"step":"consent_preview","outcome":"ok","client_kind":"dynamic","scopes":[],"status":200`,
		`"step":"consent","outcome":"ok","client_kind":"dynamic","scopes":[],"status":200`,
		`"step":"consent","outcome":"already_completed","client_kind":"dynamic","scopes":[],"status":409`,
		`"step":"consent_preview","outcome":"already_completed","client_kind":"dynamic","scopes":[],"status":409`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing line %s", want)
		}
	}
	for _, secret := range []string{handle, code, clientID} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the logs carry a secret or identifier %q", secret)
		}
	}
}

func TestOAuthConsentRouteDenyReturnsAccessDenied(t *testing.T) {
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	handle := authorizeToConsent(t, app, oauthTestAuthorizeQuery(registerOAuthTestClient(t, app, "c")))
	recorder, body := serveConsent(app, consentRequest(t, map[string]any{"action": "deny", "handle": handle}, []string{"*"}, "d1"))
	redirect, _ := url.Parse(body["redirect_url"].(string))
	if recorder.Code != http.StatusOK || redirect.Query().Get("error") != "access_denied" || redirect.Query().Get("code") != "" || redirect.Query().Get("state") != "st" {
		t.Fatalf("deny: %d %v", recorder.Code, body)
	}
	if !strings.Contains(logs.String(), `"step":"consent","outcome":"access_denied","client_kind":"dynamic","scopes":[],"status":200`) {
		t.Fatal("deny line missing")
	}
}

func TestOAuthConsentRouteRefusals(t *testing.T) {
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	handle := authorizeToConsent(t, app, oauthTestAuthorizeQuery(registerOAuthTestClient(t, app, "c")))
	all := []string{"*"}
	jti := 0
	next := func() string { jti++; return "j" + strings.Repeat("x", jti) }

	// No web assertion (the browser holding the handle, a CSRF attempt from
	// another site): 401, and the request stays undecided.
	for name, build := range map[string]func() *http.Request{
		"no assertion": func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, OAuthConsentPath, strings.NewReader(`{"action":"approve","handle":"`+handle+`","repository_scopes":["*"]}`))
			r.Header.Set("Content-Type", "application/json")
			return r
		},
		"bearer credential": func() *http.Request {
			r := consentRequest(t, map[string]any{"action": "approve", "handle": handle, "repository_scopes": all}, all, next())
			r.Header.Set("Authorization", "Bearer fcacr_x")
			return r
		},
		"assertion for another body": func() *http.Request {
			r := consentRequest(t, map[string]any{"action": "preview", "handle": handle}, all, next())
			r.Body = io.NopCloser(strings.NewReader(`{"action":"approve","handle":"` + handle + `","repository_scopes":["*"]}`))
			return r
		},
	} {
		recorder, _ := serveConsent(app, build())
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d, want 401", name, recorder.Code)
		}
	}
	// Each rejected request still wrote its one OAuth line.
	if count := strings.Count(logs.String(), `"step":"consent","outcome":"unauthenticated","client_kind":"none","scopes":[],"status":401`); count != 3 {
		t.Fatalf("unauthenticated consent lines = %d, want 3: %s", count, logs.String())
	}
	for name, tc := range map[string]struct {
		body   any
		repos  []string
		status int
		code   string
	}{
		"unknown action":               {map[string]any{"action": "poll", "handle": handle}, all, http.StatusBadRequest, "invalid_request"},
		"approve without repositories": {map[string]any{"action": "approve", "handle": handle}, all, http.StatusBadRequest, "invalid_request"},
		"preview with repositories":    {map[string]any{"action": "preview", "handle": handle, "repository_scopes": all}, all, http.StatusBadRequest, "invalid_request"},
		"unknown field":                {map[string]any{"action": "preview", "handle": handle, "redirect_uri": "https://evil.example/"}, all, http.StatusBadRequest, "invalid_request"},
		"unknown handle":               {map[string]any{"action": "preview", "handle": strings.Repeat("A", 43)}, all, http.StatusBadRequest, "invalid_request"},
		"malformed handle":             {map[string]any{"action": "preview", "handle": "not a handle"}, all, http.StatusBadRequest, "invalid_request"},
		"repositories outside grant":   {map[string]any{"action": "approve", "handle": handle, "repository_scopes": []string{"org/other"}}, []string{"org/repo"}, http.StatusBadRequest, "invalid_request"},
	} {
		recorder, body := serveConsent(app, consentRequest(t, tc.body, tc.repos, next()))
		if recorder.Code != tc.status || body["error"] != tc.code || body["redirect_url"] != nil {
			t.Fatalf("%s: %d %v, want %d %s", name, recorder.Code, body, tc.status, tc.code)
		}
	}
	// None of the refusals decided the request.
	recorder, body := serveConsent(app, consentRequest(t, map[string]any{"action": "preview", "handle": handle}, all, next()))
	if recorder.Code != http.StatusOK {
		t.Fatalf("request decided by a refusal: %d %v", recorder.Code, body)
	}
}

func TestOAuthConsentRouteRefusesAnExpiredHandle(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	store := memory.NewOAuthStore(func() time.Time { return now })
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}, Store: expiredOAuthStore{OAuthStore: store}}, true)
	if err != nil {
		t.Fatal(err)
	}
	handle := authorizeToConsent(t, app, oauthTestAuthorizeQuery(registerOAuthTestClient(t, app, "c")))
	for i, action := range []map[string]any{
		{"action": "preview", "handle": handle},
		{"action": "approve", "handle": handle, "repository_scopes": []string{"*"}},
		{"action": "deny", "handle": handle},
	} {
		recorder, body := serveConsent(app, consentRequest(t, action, []string{"*"}, "e"+strings.Repeat("x", i)))
		if recorder.Code != http.StatusGone || body["error"] != "expired" || body["redirect_url"] != nil {
			t.Fatalf("expired %v: %d %v, want 410 expired", action, recorder.Code, body)
		}
	}
}

// expiredOAuthStore reports every stored request as already expired.
type expiredOAuthStore struct{ storage.OAuthStore }

func (s expiredOAuthStore) GetAuthorizationRequest(ctx context.Context, hash storage.OAuthSecretHash) (storage.OAuthAuthorizationRequest, error) {
	request, err := s.OAuthStore.GetAuthorizationRequest(ctx, hash)
	request.ExpiresAt = request.CreatedAt
	return request, err
}

func TestOAuthConsentPathIsAnOrganizationWideAssertionPath(t *testing.T) {
	if !slices.Contains(auth.WebAssertionOrganizationWidePaths(), OAuthConsentPath) {
		t.Fatalf("%s is not in the organization-wide web assertion paths %v", OAuthConsentPath, auth.WebAssertionOrganizationWidePaths())
	}
}

func TestOAuthProblemPageIsLockedDown(t *testing.T) {
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, OAuthAuthorizePath+"?client_id=unknown", nil))
	csp := recorder.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'none'", "frame-ancestors 'none'", "form-action 'none'", "connect-src 'none'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP %q lacks %q", csp, directive)
		}
	}
	if recorder.Code != http.StatusBadRequest || recorder.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("problem page: %d %v", recorder.Code, recorder.Header())
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

// The consent limiter keys on the request handle, never the peer address:
// every consent request comes from the web server (one peer address, and
// behind the ingress every public user shares it), so many logins through
// one address are never throttled together, and one handle over its limit is.
func TestOAuthConsentIsRateLimitedPerHandleNotPerAddress(t *testing.T) {
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	jti := 0
	preview := func(handle string) int {
		jti++
		request := consentRequest(t, map[string]any{"action": "preview", "handle": handle}, []string{"*"}, "rl"+strconv.Itoa(jti))
		request.RemoteAddr = "10.42.0.7:40000" // the one ingress pod every request arrives from
		recorder, _ := serveConsent(app, request)
		return recorder.Code
	}
	// 70 distinct handles (70 logins) through one address: none throttled.
	for i := range 70 {
		handle := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(string(rune('a'+i%26)), 31) + string(rune('A'+i/26))))
		if status := preview(handle); status != http.StatusBadRequest {
			t.Fatalf("login %d through one address: status %d, want 400 (unknown handle, not throttled)", i, status)
		}
	}
	// One handle: 20 requests a minute, then 429 with Retry-After.
	handle := strings.Repeat("B", 43)
	statuses := map[int]int{}
	for range 25 {
		statuses[preview(handle)]++
	}
	if statuses[http.StatusBadRequest] != 20 || statuses[http.StatusTooManyRequests] != 5 {
		t.Fatalf("25 requests for one handle: statuses %v, want 20x400 then 5x429", statuses)
	}
	if count := strings.Count(logs.String(), `"step":"consent_preview","outcome":"rate_limited"`); count != 5 {
		t.Fatalf("consent rate_limited lines = %d, want 5", count)
	}
}

// The consent body's whole input domain in one pass: every cell that is not
// a contract shape is 400 invalid_request and decides nothing. Repository
// lists follow the device approval's own rules (NormalizeRepositoryScopes):
// a repeated slug is normalized away, never refused; each approve cell that
// can succeed runs on a request of its own.
func TestOAuthConsentBodyDomain(t *testing.T) {
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	clientID := registerOAuthTestClient(t, app, "c")
	handle := authorizeToConsent(t, app, oauthTestAuthorizeQuery(clientID))
	h := `"` + handle + `"`
	cells := []struct {
		name, contentType, body string
		want                    int
	}{
		{"canonical preview", "application/json", `{"action":"preview","handle":` + h + `}`, http.StatusOK},
		{"form content type", "application/x-www-form-urlencoded", "action=preview&handle=" + handle, http.StatusBadRequest},
		{"no content type", "", `{"action":"preview","handle":` + h + `}`, http.StatusBadRequest},
		{"empty body", "application/json", ``, http.StatusBadRequest},
		{"null body", "application/json", `null`, http.StatusBadRequest},
		{"array body", "application/json", `[]`, http.StatusBadRequest},
		{"two objects", "application/json", `{"action":"preview","handle":` + h + `}{}`, http.StatusBadRequest},
		{"action absent", "application/json", `{"handle":` + h + `}`, http.StatusBadRequest},
		{"action null", "application/json", `{"action":null,"handle":` + h + `}`, http.StatusBadRequest},
		{"action number", "application/json", `{"action":1,"handle":` + h + `}`, http.StatusBadRequest},
		{"action case", "application/json", `{"action":"Preview","handle":` + h + `}`, http.StatusBadRequest},
		{"action out of vocabulary", "application/json", `{"action":"poll","handle":` + h + `}`, http.StatusBadRequest},
		{"duplicate action", "application/json", `{"action":"preview","action":"approve","handle":` + h + `,"repository_scopes":["*"]}`, http.StatusBadRequest},
		{"handle absent", "application/json", `{"action":"preview"}`, http.StatusBadRequest},
		{"handle null", "application/json", `{"action":"preview","handle":null}`, http.StatusBadRequest},
		{"handle number", "application/json", `{"action":"preview","handle":1}`, http.StatusBadRequest},
		{"handle empty", "application/json", `{"action":"preview","handle":""}`, http.StatusBadRequest},
		{"handle 42 chars", "application/json", `{"action":"preview","handle":"` + handle[:42] + `"}`, http.StatusBadRequest},
		{"handle 44 chars", "application/json", `{"action":"preview","handle":"` + handle + `A"}`, http.StatusBadRequest},
		{"handle padded", "application/json", `{"action":"preview","handle":"` + handle[:42] + `="}`, http.StatusBadRequest},
		{"unknown field", "application/json", `{"action":"preview","handle":` + h + `,"redirect_uri":"https://evil.example/"}`, http.StatusBadRequest},
		{"preview with repositories", "application/json", `{"action":"preview","handle":` + h + `,"repository_scopes":["*"]}`, http.StatusBadRequest},
		{"deny with repositories", "application/json", `{"action":"deny","handle":` + h + `,"repository_scopes":["*"]}`, http.StatusBadRequest},
		{"approve repositories absent", "application/json", `{"action":"approve","handle":` + h + `}`, http.StatusBadRequest},
		{"approve repositories null", "application/json", `{"action":"approve","handle":` + h + `,"repository_scopes":null}`, http.StatusBadRequest},
		{"approve repositories empty", "application/json", `{"action":"approve","handle":` + h + `,"repository_scopes":[]}`, http.StatusBadRequest},
		{"approve repositories string", "application/json", `{"action":"approve","handle":` + h + `,"repository_scopes":"*"}`, http.StatusBadRequest},
		{"approve repositories number item", "application/json", `{"action":"approve","handle":` + h + `,"repository_scopes":[1]}`, http.StatusBadRequest},
		{"approve wildcard mixed", "application/json", `{"action":"approve","handle":` + h + `,"repository_scopes":["*","org/repo"]}`, http.StatusBadRequest},
		{"approve repository not a slug", "application/json", `{"action":"approve","handle":` + h + `,"repository_scopes":["not a slug"]}`, http.StatusBadRequest},
	}
	for i, cell := range cells {
		request := httptest.NewRequest(http.MethodPost, OAuthConsentPath, strings.NewReader(cell.body))
		if cell.contentType != "" {
			request.Header.Set("Content-Type", cell.contentType)
		}
		request.Header.Set(auth.WebAssertionHeader, signConsentAssertion(t, request, []byte(cell.body), []string{"*"}, "dom"+strconv.Itoa(i)))
		recorder, body := serveConsent(app, request)
		t.Logf("cell %-34s -> %d %v", cell.name, recorder.Code, body["error"])
		if recorder.Code != cell.want || (cell.want == http.StatusBadRequest && body["error"] != "invalid_request") {
			t.Errorf("%s: %d %v, want %d", cell.name, recorder.Code, body, cell.want)
		}
	}
	recorder, _ := serveConsent(app, consentRequest(t, map[string]any{"action": "preview", "handle": handle}, []string{"*"}, "dom-final"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("a refused cell decided the request: %d", recorder.Code)
	}
	// Accepted shapes, each on its own request.
	for i, repositories := range [][]string{{"*"}, {"org/repo"}, {"org/repo", "org/repo"}, {"org/a", "org/b"}} {
		fresh := authorizeToConsent(t, app, oauthTestAuthorizeQuery(clientID))
		recorder, body := serveConsent(app, consentRequest(t, map[string]any{"action": "approve", "handle": fresh, "repository_scopes": repositories}, slices.Compact(slices.Clone(repositories)), "acc"+strconv.Itoa(i)))
		t.Logf("cell approve %-30v -> %d", repositories, recorder.Code)
		if recorder.Code != http.StatusOK || body["redirect_url"] == nil {
			t.Errorf("approve %v: %d %v, want 200 with redirect_url", repositories, recorder.Code, body)
		}
	}
}

// Every request to the consent route writes exactly one OAuth line, whether
// the handler decided it or a wrapper in front of it refused it.
func TestOAuthConsentRouteWritesOneOAuthLinePerRequest(t *testing.T) {
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	handle := authorizeToConsent(t, app, oauthTestAuthorizeQuery(registerOAuthTestClient(t, app, "c")))
	requests := []*http.Request{
		consentRequest(t, map[string]any{"action": "preview", "handle": handle}, []string{"*"}, "one1"),
		consentRequest(t, map[string]any{"action": "poll", "handle": handle}, []string{"*"}, "one2"),
		consentRequest(t, map[string]any{"action": "preview", "handle": strings.Repeat("A", 43)}, []string{"*"}, "one3"),
		consentRequest(t, map[string]any{"action": "approve", "handle": handle, "repository_scopes": []string{"*"}}, []string{"*"}, "one4"),
		consentRequest(t, map[string]any{"action": "approve", "handle": handle, "repository_scopes": []string{"*"}}, []string{"*"}, "one5"),
		httptest.NewRequest(http.MethodPost, OAuthConsentPath, strings.NewReader(`{}`)),
	}
	before := strings.Count(logs.String(), `"msg":"acr-api oauth step"`)
	for _, request := range requests {
		serveConsent(app, request)
	}
	lines := []string{}
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, `"msg":"acr-api oauth step"`) {
			lines = append(lines, line)
		}
	}
	if len(lines)-before != len(requests) {
		t.Fatalf("consent requests %d wrote %d OAuth lines: %v", len(requests), len(lines)-before, lines[before:])
	}
}

// resumeOAuthStore fails the next code attachment once.
type resumeOAuthStore struct {
	storage.OAuthStore
	failNext *bool
}

func (s resumeOAuthStore) IssueAuthorizationCode(ctx context.Context, handle, code storage.OAuthSecretHash, expiresAt time.Time) (storage.OAuthAuthorizationRequest, error) {
	if *s.failNext {
		*s.failNext = false
		return storage.OAuthAuthorizationRequest{}, errors.New("transient")
	}
	return s.OAuthStore.IssueAuthorizationCode(ctx, handle, code, expiresAt)
}

// A transient failure after the approval is recorded: 503, then the same
// user's retry finishes the login with a code.
func TestOAuthConsentRouteApprovalRetriesAfterATransientFailure(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	failNext := false
	store := resumeOAuthStore{OAuthStore: memory.NewOAuthStore(func() time.Time { return now }), failNext: &failNext}
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}, Store: store}, true)
	if err != nil {
		t.Fatal(err)
	}
	handle := authorizeToConsent(t, app, oauthTestAuthorizeQuery(registerOAuthTestClient(t, app, "c")))
	failNext = true
	recorder, body := serveConsent(app, consentRequest(t, map[string]any{"action": "approve", "handle": handle, "repository_scopes": []string{"*"}}, []string{"*"}, "rs1"))
	if recorder.Code != http.StatusServiceUnavailable || body["error"] != "temporarily_unavailable" {
		t.Fatalf("approval with a failing code store: %d %v", recorder.Code, body)
	}
	recorder, body = serveConsent(app, consentRequest(t, map[string]any{"action": "approve", "handle": handle, "repository_scopes": []string{"*"}}, []string{"*"}, "rs2"))
	redirect, _ := body["redirect_url"].(string)
	if recorder.Code != http.StatusOK || !strings.Contains(redirect, "code=") {
		t.Fatalf("the approver's retry: %d %v, want 200 with a code", recorder.Code, body)
	}
}
