package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-go/authverify"
)

type acceptingHopValidator struct{}

func (acceptingHopValidator) Validate(context.Context, string) (authverify.SubjectIdentity, error) {
	return authverify.SubjectIdentity{Namespace: "acr", ServiceAccountName: "acr-mcp", ExpiresAt: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
}

// mcpBoundOAuthToken runs the authorization-code flow for a dynamic client and
// returns its access token (bound to the MCP resource) and the client id.
func mcpBoundOAuthToken(t *testing.T, app *App) (string, string) {
	t.Helper()
	clientID := registerOAuthTestClient(t, app, "mcp client")
	query := oauthTestAuthorizeQuery(clientID)
	query.Set("resource", oauthTestResource)
	all := []string{"*"}
	_, body := serveConsent(app, consentRequest(t, map[string]any{"action": "approve", "handle": authorizeToConsent(t, app, query), "repository_scopes": all}, all, "r1"))
	redirect, _ := url.Parse(body["redirect_url"].(string))
	status, token, raw := postOAuthForm(t, app, OAuthTokenPath, url.Values{"grant_type": {"authorization_code"}, "code": {redirect.Query().Get("code")},
		"redirect_uri": {oauthTestRedirect}, "client_id": {clientID}, "code_verifier": {strings.Repeat("v", 50)}, "resource": {oauthTestResource}})
	if status != http.StatusOK || token.AccessToken == "" {
		t.Fatalf("token: %d %s", status, raw)
	}
	return token.AccessToken, clientID
}

// forwardedCall is a call as acr-mcp forwards it: the MCP resource and a hop.
func forwardedCall(app *App, token string) int {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(auth.ResourceHeader, oauthTestResource)
	request.Header.Set(auth.MCPHopHeader, "hop")
	request.Header.Set("X-ACR-Client-Version", "0.1.0")
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	return recorder.Code
}

// CHAOS-12825: an MCP-bound credential is usable only through acr-mcp (the
// hop), and acr-mcp forwards no lifecycle route, so the holder had no way to
// revoke it. RFC 7009 /revoke now takes the credential itself.
func TestMCPBoundAccessTokenCanBeRevokedAtTheRevocationEndpoint(t *testing.T) {
	policy, err := auth.NewMCPHopPolicy(auth.MCPHopPolicyOptions{Validator: acceptingHopValidator{}, Namespace: "acr", ServiceAccount: "acr-mcp", Issuer: oauthTestIssuer})
	if err != nil {
		t.Fatal(err)
	}
	oauthTestMCPHop = policy
	t.Cleanup(func() { oauthTestMCPHop = nil })
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}, ConsentURL: oauthTestConsentURL}, true)
	if err != nil {
		t.Fatal(err)
	}
	token, clientID := mcpBoundOAuthToken(t, app)
	if got := forwardedCall(app, token); got != http.StatusOK {
		t.Fatalf("forwarded call before revoke = %d, want 200", got)
	}

	direct := httptest.NewRequest(http.MethodPost, "/api/v1/auth/credentials/self/revoke", strings.NewReader(`{"schema_version":"credential_revoke_request.v1"}`))
	direct.Header.Set("Authorization", "Bearer "+token)
	direct.Header.Set(auth.ResourceHeader, oauthTestResource)
	direct.Header.Set("Content-Type", "application/json")
	directRecorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(directRecorder, direct)
	if directRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("direct self revoke = %d, want 401 (the hop keeps MCP bearers off acr-api)", directRecorder.Code)
	}

	wrong := registerOAuthTestClient(t, app, "other client")
	if status, _, raw := postOAuthForm(t, app, OAuthRevocationPath, url.Values{"token": {token}, "token_type_hint": {"access_token"}, "client_id": {wrong}}); status != http.StatusOK {
		t.Fatalf("revoke by another client = %d %s, want 200 (no oracle)", status, raw)
	}
	if got := forwardedCall(app, token); got != http.StatusOK {
		t.Fatalf("another client's revoke killed the credential: forwarded call = %d, want 200", got)
	}

	status, _, raw := postOAuthForm(t, app, OAuthRevocationPath, url.Values{"token": {token}, "token_type_hint": {"access_token"}, "client_id": {clientID}})
	if status != http.StatusOK || strings.TrimSpace(raw) != "{}" {
		t.Fatalf("revoke = %d %s, want 200 {}", status, raw)
	}
	if got := forwardedCall(app, token); got != http.StatusUnauthorized {
		t.Fatalf("forwarded call after revoke = %d, want 401", got)
	}
	if status, _, raw := postOAuthForm(t, app, OAuthRevocationPath, url.Values{"token": {token}, "client_id": {clientID}}); status != http.StatusOK {
		t.Fatalf("second revoke = %d %s, want 200", status, raw)
	}
	if !strings.Contains(logs.String(), `"step":"revoke","outcome":"ok"`) || !strings.Contains(logs.String(), `"outcome":"client_mismatch"`) {
		t.Fatalf("missing revoke telemetry lines: %s", logs.String())
	}
	if strings.Contains(logs.String(), token) {
		t.Fatal("the logs carry the token")
	}
}
