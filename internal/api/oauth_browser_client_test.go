package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12123, through the routes: the development static browser client
// logs in with the web session and gets a 10-minute token bound to the
// acr-api resource; a consent without the session, or while impersonating,
// is refused and its line names why.
func TestOAuthBrowserClientLoginThroughTheRoutes(t *testing.T) {
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{
		Issuer: oauthTestIssuer, Resources: []string{oauthTestResource, oauthTestIssuer},
		DevBrowserRedirectURIs: []string{oauthTestRedirect},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { oauthConsentExtraClaims = nil })
	all := []string{"*"}
	approve := func(handle, jti string, claims map[string]any) (*httptest.ResponseRecorder, map[string]any) {
		oauthConsentExtraClaims = claims
		return serveConsent(app, consentRequest(t, map[string]any{"action": "approve", "handle": handle, "repository_scopes": all}, all, jti))
	}

	recorder, body := approve(authorizeToConsent(t, app, oauthTestAuthorizeQuery(auth.DevBrowserClientID)), "b1", nil)
	if recorder.Code != http.StatusBadRequest || body["error"] != "invalid_request" {
		t.Fatalf("consent without sid: %d %v", recorder.Code, body)
	}
	recorder, body = approve(authorizeToConsent(t, app, oauthTestAuthorizeQuery(auth.DevBrowserClientID)), "b2", map[string]any{"sid": "sess_1", "impersonating": true})
	if recorder.Code != http.StatusBadRequest || body["error"] != "invalid_request" {
		t.Fatalf("consent while impersonating: %d %v", recorder.Code, body)
	}
	recorder, body = approve(authorizeToConsent(t, app, oauthTestAuthorizeQuery(auth.DevBrowserClientID)), "b3", map[string]any{"sid": "sess_1"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("consent with sid: %d %v", recorder.Code, body)
	}
	redirect, _ := url.Parse(body["redirect_url"].(string))
	form := url.Values{"grant_type": {"authorization_code"}, "code": {redirect.Query().Get("code")}, "redirect_uri": {oauthTestRedirect},
		"client_id": {auth.DevBrowserClientID}, "code_verifier": {strings.Repeat("v", 50)}}
	tokenRequest := httptest.NewRequest(http.MethodPost, OAuthTokenPath, strings.NewReader(form.Encode()))
	tokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenRecorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(tokenRecorder, tokenRequest)
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(tokenRecorder.Body.Bytes(), &token); err != nil || tokenRecorder.Code != http.StatusOK ||
		!strings.HasPrefix(token.AccessToken, "fcacr_") || token.ExpiresIn != 600 {
		t.Fatalf("token: %d %s", tokenRecorder.Code, tokenRecorder.Body.String())
	}
	// The token is bound to the acr-api resource, not the MCP one.
	for resource, want := range map[string]int{oauthTestIssuer: http.StatusOK, oauthTestResource: http.StatusUnauthorized} {
		read := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
		read.Header.Set("Authorization", "Bearer "+token.AccessToken)
		read.Header.Set(auth.ResourceHeader, resource)
		read.Header.Set("X-ACR-Client-Version", "0.1.0")
		readRecorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(readRecorder, read)
		if readRecorder.Code != want {
			t.Errorf("capabilities read with %s = %d, want %d: %s", resource, readRecorder.Code, want, readRecorder.Body.String())
		}
	}
	// It renews only by its refresh grant: a self-rotation is refused.
	rotate := deviceRequest(t, http.MethodPost, "/api/v1/auth/credentials/self/rotate", contractsv1.CredentialRotateRequest{SchemaVersion: contractsv1.CredentialRotateRequestSchema})
	rotate.Header.Set("Authorization", "Bearer "+token.AccessToken)
	rotate.Header.Set(auth.ResourceHeader, oauthTestIssuer)
	rotateRecorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(rotateRecorder, rotate)
	if rotateRecorder.Code != http.StatusBadRequest {
		t.Fatalf("self-rotate of the browser token: %d %s", rotateRecorder.Code, rotateRecorder.Body.String())
	}
	for _, want := range []string{
		`"step":"credential_rotate","outcome":"rotation_not_allowed","client_kind":"none","scopes":[],"status":400`,
		`"step":"consent","outcome":"session_required","client_kind":"metadata_document"`,
		`"step":"consent","outcome":"impersonating","client_kind":"metadata_document"`,
		`"step":"consent","outcome":"ok","client_kind":"metadata_document"`,
		`"step":"token","outcome":"ok","client_kind":"metadata_document"`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing line %s", want)
		}
	}
}
