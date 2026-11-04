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

// RFC 7009 s2.2: every outcome that is not a store failure answers the same
// 200 {} (no oracle), at the HTTP layer.
func TestRevocationEndpointAnswersEveryAccessTokenOutcomeAlike(t *testing.T) {
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}, ConsentURL: oauthTestConsentURL}, true)
	if err != nil {
		t.Fatal(err)
	}
	token, clientID := mcpBoundOAuthToken(t, app)
	other := registerOAuthTestClient(t, app, "other client")
	unknown := auth.TokenPrefix + strings.Repeat("A", 43)
	if !auth.IsTokenShapeValid(unknown) {
		t.Fatalf("test token %q is not well-shaped", unknown)
	}
	cases := []struct {
		name  string
		token string
		id    string
	}{
		{"another client", token, other},
		{"owner", token, clientID},
		{"already revoked", token, clientID},
		{"unknown well-shaped token", unknown, clientID},
		{"malformed fcacr token", auth.TokenPrefix + "short", clientID},
	}
	for _, c := range cases {
		status, _, raw := postOAuthForm(t, app, OAuthRevocationPath, url.Values{"token": {c.token}, "token_type_hint": {"access_token"}, "client_id": {c.id}})
		if status != http.StatusOK || strings.TrimSpace(raw) != "{}" {
			t.Fatalf("%s: %d %q, want 200 {}", c.name, status, raw)
		}
	}
}

func revokeFrom(t *testing.T, app *App, remote, token, clientID string) int {
	t.Helper()
	form := url.Values{"token": {token}, "token_type_hint": {"access_token"}, "client_id": {clientID}}
	request := httptest.NewRequest(http.MethodPost, OAuthRevocationPath, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.RemoteAddr = remote + ":4000"
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	return recorder.Code
}

// codex astra r on #2026: the client_id is public, so a flood of bad tokens
// under it must not use up the holder's revoke, and a flood from the holder's
// own address must not lock the holder out either.
func TestRevocationFloodDoesNotBlockTheHolder(t *testing.T) {
	policy, err := auth.NewMCPHopPolicy(auth.MCPHopPolicyOptions{Validator: acceptingHopValidator{}, Namespace: "acr", ServiceAccount: "acr-mcp", Issuer: oauthTestIssuer})
	if err != nil {
		t.Fatal(err)
	}
	oauthTestMCPHop = policy
	oauthTestAuthAttempts = auth.NewBoundedMemoryLimiter(auth.MemoryLimiterOptions{Window: time.Minute, AttemptLimit: 10000, FailureLimit: 5, MaxTrackedKeys: 64})
	t.Cleanup(func() { oauthTestMCPHop, oauthTestAuthAttempts = nil, nil })
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}, ConsentURL: oauthTestConsentURL}, true)
	if err != nil {
		t.Fatal(err)
	}
	token, clientID := mcpBoundOAuthToken(t, app)

	// From another address: 80 well-formed unknown tokens under the holder's public client_id.
	for i := 0; i < 80; i++ {
		bogus := auth.TokenPrefix + strings.Repeat("A", 42) + string(rune('A'+i%26))
		revokeFrom(t, app, "198.51.100.7", bogus, clientID)
	}
	// From the SAME address as the holder: malformed tokens until the failure budget is spent.
	refused := 0
	for i := 0; i < 20; i++ {
		if revokeFrom(t, app, "203.0.113.9", auth.TokenPrefix+"bad", clientID) == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused == 0 {
		t.Fatal("malformed tokens from one address were never refused: the address bound is absent")
	}
	if got := revokeFrom(t, app, "203.0.113.9", token, clientID); got != http.StatusOK {
		t.Fatalf("holder on the flooded address got %d, want 200", got)
	}
	if got := forwardedCall(app, token); got != http.StatusUnauthorized {
		t.Fatalf("forwarded call after the holder's revoke = %d, want 401", got)
	}
}
