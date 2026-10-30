package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

type oauthTokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
}

func postOAuthForm(t *testing.T, app *App, path string, form url.Values) (int, oauthTokenAnswer, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	var answer oauthTokenAnswer
	_ = json.Unmarshal(recorder.Body.Bytes(), &answer)
	return recorder.Code, answer, recorder.Body.String()
}

// CHAOS-12124, through the routes: the browser login returns a refresh
// token; /token rotates it; a replay inside the grace gets the same
// successor; /revoke revokes the family by the refresh token; and the
// metadata names the grant and the endpoint only with a browser client.
func TestOAuthBrowserRefreshThroughTheRoutes(t *testing.T) {
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{
		Issuer: oauthTestIssuer, Resources: []string{oauthTestResource, oauthTestIssuer}, DevBrowserRedirectURIs: []string{oauthTestRedirect},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { oauthConsentExtraClaims = nil })
	all := []string{"*"}
	oauthConsentExtraClaims = map[string]any{"sid": "sess_1"}
	_, body := serveConsent(app, consentRequest(t, map[string]any{"action": "approve", "handle": authorizeToConsent(t, app, oauthTestAuthorizeQuery(auth.DevBrowserClientID)), "repository_scopes": all}, all, "r1"))
	redirect, _ := url.Parse(body["redirect_url"].(string))
	status, login, raw := postOAuthForm(t, app, OAuthTokenPath, url.Values{"grant_type": {"authorization_code"}, "code": {redirect.Query().Get("code")},
		"redirect_uri": {oauthTestRedirect}, "client_id": {auth.DevBrowserClientID}, "code_verifier": {strings.Repeat("v", 50)}})
	if status != http.StatusOK || !auth.ValidRefreshTokenShape(login.RefreshToken) || login.ExpiresIn != 600 {
		t.Fatalf("login: %d %s", status, raw)
	}

	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {login.RefreshToken}, "client_id": {auth.DevBrowserClientID}}
	status, rotated, raw := postOAuthForm(t, app, OAuthTokenPath, refresh)
	if status != http.StatusOK || rotated.AccessToken == login.AccessToken || !auth.ValidRefreshTokenShape(rotated.RefreshToken) || rotated.RefreshToken == login.RefreshToken {
		t.Fatalf("refresh: %d %s", status, raw)
	}
	status, replay, raw := postOAuthForm(t, app, OAuthTokenPath, refresh)
	if status != http.StatusOK || replay.AccessToken != rotated.AccessToken || replay.RefreshToken != rotated.RefreshToken {
		t.Fatalf("grace replay: %d %s", status, raw)
	}
	// The access token rotated in works with the acr-api resource.
	read := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	read.Header.Set("Authorization", "Bearer "+rotated.AccessToken)
	read.Header.Set(auth.ResourceHeader, oauthTestIssuer)
	read.Header.Set("X-ACR-Client-Version", "0.1.0")
	readRecorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(readRecorder, read)
	if readRecorder.Code != http.StatusOK {
		t.Fatalf("rotated access token read: %d %s", readRecorder.Code, readRecorder.Body.String())
	}

	status, _, raw = postOAuthForm(t, app, OAuthRevocationPath, url.Values{"token": {rotated.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {auth.DevBrowserClientID}})
	if status != http.StatusOK || strings.TrimSpace(raw) != "{}" {
		t.Fatalf("revoke: %d %s", status, raw)
	}
	status, after, raw := postOAuthForm(t, app, OAuthTokenPath, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rotated.RefreshToken}, "client_id": {auth.DevBrowserClientID}})
	if status != http.StatusBadRequest || after.Error != "invalid_grant" {
		t.Fatalf("refresh after revoke: %d %s", status, raw)
	}
	readRecorder = httptest.NewRecorder()
	app.Handler().ServeHTTP(readRecorder, read)
	if readRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("access token after the family revoke: %d, want 401", readRecorder.Code)
	}
	status, _, raw = postOAuthForm(t, app, OAuthRevocationPath, url.Values{"token": {rotated.RefreshToken}, "client_id": {auth.DevBrowserClientID}})
	if status != http.StatusOK {
		t.Fatalf("second revoke: %d %s, want 200", status, raw)
	}

	for _, want := range []string{
		`"step":"token","outcome":"ok","client_kind":"metadata_document"`,
		`"step":"token","outcome":"grace_replay","client_kind":"metadata_document"`,
		`"step":"revoke","outcome":"ok","client_kind":"metadata_document","scopes":[],"status":200`,
		`"step":"token","outcome":"family_revoked","client_kind":"metadata_document"`,
		`"step":"revoke","outcome":"family_revoked","client_kind":"metadata_document","scopes":[],"status":200`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing line %s", want)
		}
	}
	for _, secret := range []string{login.RefreshToken, rotated.RefreshToken, rotated.AccessToken} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("the logs carry a token")
		}
	}

	metadata := httptest.NewRecorder()
	app.Handler().ServeHTTP(metadata, httptest.NewRequest(http.MethodGet, OAuthAuthorizationServerMetadataPath, nil))
	if !strings.Contains(metadata.Body.String(), `"refresh_token"]`) || !strings.Contains(metadata.Body.String(), `"revocation_endpoint":"`+oauthTestIssuer+`/revoke"`) {
		t.Fatalf("metadata with a browser client: %s", metadata.Body.String())
	}
}

func TestOAuthRefreshGrantIsAbsentWithoutABrowserClient(t *testing.T) {
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	metadata := httptest.NewRecorder()
	app.Handler().ServeHTTP(metadata, httptest.NewRequest(http.MethodGet, OAuthAuthorizationServerMetadataPath, nil))
	if strings.Contains(metadata.Body.String(), "refresh_token") || strings.Contains(metadata.Body.String(), "revocation_endpoint") {
		t.Fatalf("metadata without a browser client advertises the refresh grant: %s", metadata.Body.String())
	}
	if status, _, _ := postOAuthForm(t, app, OAuthRevocationPath, url.Values{"token": {"x"}, "client_id": {"y"}}); status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		t.Fatalf("/revoke without a browser client = %d, want not served", status)
	}
	if status, answer, _ := postOAuthForm(t, app, OAuthTokenPath, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}, "client_id": {auth.DevBrowserClientID}}); status != http.StatusBadRequest || answer.Error != "unsupported_grant_type" {
		t.Fatalf("refresh without a browser client = %d %q, want unsupported_grant_type", status, answer.Error)
	}
}

// Sign-out from the web origin: the browser preflights /revoke and must read
// its answer, so /revoke answers CORS for the listed origin like /token
// (CHAOS-12124; found by the web lane against 1442f42f: preflight 405, POST
// 200 with no Access-Control-Allow-Origin).
func TestOAuthRevokeAnswersCORSForTheWebOrigin(t *testing.T) {
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{
		Issuer: oauthTestIssuer, Resources: []string{oauthTestResource, oauthTestIssuer}, DevBrowserRedirectURIs: []string{oauthTestRedirect},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	app.config.CORSAllowedOrigins = []string{corsTestOrigin}

	for _, path := range []string{OAuthTokenPath, OAuthRevocationPath} {
		preflight := httptest.NewRequest(http.MethodOptions, path, nil)
		preflight.Header.Set("Origin", corsTestOrigin)
		preflight.Header.Set("Access-Control-Request-Method", "POST")
		preflight.Header.Set("Access-Control-Request-Headers", "content-type, x-request-id")
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, preflight)
		if recorder.Code != http.StatusNoContent || recorder.Header().Get("Access-Control-Allow-Origin") != corsTestOrigin ||
			!containsValue(recorder.Header().Values("Access-Control-Allow-Methods"), "POST") {
			t.Fatalf("%s preflight: %d, Allow-Origin %q, Allow-Methods %q; want 204 for %s with POST", path, recorder.Code,
				recorder.Header().Get("Access-Control-Allow-Origin"), recorder.Header().Get("Access-Control-Allow-Methods"), corsTestOrigin)
		}
		expectNoCredentialsOrWildcard(t, path+" preflight", recorder)
	}

	revoke := httptest.NewRequest(http.MethodPost, OAuthRevocationPath, strings.NewReader(url.Values{"token": {"acrrt_" + strings.Repeat("A", 43)}, "client_id": {auth.DevBrowserClientID}}.Encode()))
	revoke.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	revoke.Header.Set("Origin", corsTestOrigin)
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, revoke)
	expectCORSActual(t, "revoke", recorder, http.StatusOK, corsTestOrigin)
	if strings.TrimSpace(recorder.Body.String()) != "{}" {
		t.Fatalf("revoke body = %q, want {}", recorder.Body.String())
	}

	other := httptest.NewRequest(http.MethodOptions, OAuthRevocationPath, nil)
	other.Header.Set("Origin", "https://evil.example")
	other.Header.Set("Access-Control-Request-Method", "POST")
	recorder = httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, other)
	expectNoCORS(t, "revoke preflight from an unlisted origin", recorder)
}

// Every user of a first-party browser client shares its public client_id, so
// a client-keyed limit would put every browser session -- and anyone who
// knows the id -- in one bucket (codex #1786 r2 P2). Its token and revoke
// requests are limited per presented secret instead: a flood of bad refresh
// tokens and bad revokes under the browser client id does not throttle a
// real session's refresh or sign-out, while one token replayed past the
// limit is still throttled. A dynamic client keeps its per-client bucket.
func TestOAuthBrowserClientIsLimitedPerSecretNotPerSharedClientID(t *testing.T) {
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{
		Issuer: oauthTestIssuer, Resources: []string{oauthTestResource, oauthTestIssuer}, DevBrowserRedirectURIs: []string{oauthTestRedirect},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { oauthConsentExtraClaims = nil })
	all := []string{"*"}
	oauthConsentExtraClaims = map[string]any{"sid": "sess_limit"}
	_, body := serveConsent(app, consentRequest(t, map[string]any{"action": "approve", "handle": authorizeToConsent(t, app, oauthTestAuthorizeQuery(auth.DevBrowserClientID)), "repository_scopes": all}, all, "rl1"))
	redirect, _ := url.Parse(body["redirect_url"].(string))
	status, login, raw := postOAuthForm(t, app, OAuthTokenPath, url.Values{"grant_type": {"authorization_code"}, "code": {redirect.Query().Get("code")},
		"redirect_uri": {oauthTestRedirect}, "client_id": {auth.DevBrowserClientID}, "code_verifier": {strings.Repeat("v", 50)}})
	if status != http.StatusOK {
		t.Fatalf("login: %d %s", status, raw)
	}

	bad := func(i int) string { return "acrrt_" + strings.Repeat(strconv.Itoa(i%10), 43) + strconv.Itoa(i) }
	for i := range 80 {
		if status, _, _ := postOAuthForm(t, app, OAuthTokenPath, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {bad(i)}, "client_id": {auth.DevBrowserClientID}}); status == http.StatusTooManyRequests {
			t.Fatalf("refresh %d with a distinct bad token was throttled: the browser client's sessions share one bucket", i)
		}
		if status, _, _ := postOAuthForm(t, app, OAuthRevocationPath, url.Values{"token": {bad(i)}, "client_id": {auth.DevBrowserClientID}}); status == http.StatusTooManyRequests {
			t.Fatalf("revoke %d with a distinct bad token was throttled: the browser client's sessions share one bucket", i)
		}
	}
	status, rotated, raw := postOAuthForm(t, app, OAuthTokenPath, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {login.RefreshToken}, "client_id": {auth.DevBrowserClientID}})
	if status != http.StatusOK {
		t.Fatalf("a real session's refresh after the flood: %d %s, want 200", status, raw)
	}
	if status, _, raw = postOAuthForm(t, app, OAuthRevocationPath, url.Values{"token": {rotated.RefreshToken}, "client_id": {auth.DevBrowserClientID}}); status != http.StatusOK {
		t.Fatalf("a real session's sign-out after the flood: %d %s, want 200", status, raw)
	}

	// One presented token is still limited: replayed past the limit it is throttled.
	replayed := map[int]int{}
	for range 70 {
		status, _, _ := postOAuthForm(t, app, OAuthTokenPath, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {bad(999)}, "client_id": {auth.DevBrowserClientID}})
		replayed[status]++
	}
	if replayed[http.StatusTooManyRequests] != 10 {
		t.Fatalf("70 refreshes with one token: statuses %v, want 10x429", replayed)
	}
	// A dynamic client keeps its per-client bucket.
	dynamic := map[int]int{}
	for i := range 70 {
		status, _, _ := postOAuthForm(t, app, OAuthTokenPath, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {bad(i)}, "client_id": {"acrc_" + strings.Repeat("a", 32)}})
		dynamic[status]++
	}
	if dynamic[http.StatusTooManyRequests] != 10 {
		t.Fatalf("70 token calls for one dynamic client: statuses %v, want 10x429", dynamic)
	}
}
