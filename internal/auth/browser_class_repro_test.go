package auth

import (
	"context"
	"testing"
	"time"
)

// TestReproTheWebClientGetsAShortLivedBrowserToken reproduces the gap the
// browser client class closes: the first-party web client's OAuth code
// exchange must issue a short-lived (10-minute) credential, not the 30-day
// one every OAuth client gets. browserHarness makes the web client.
func TestReproTheWebClientGetsAShortLivedBrowserToken(t *testing.T) {
	h, clientID := browserHarness(t)
	verifier, challenge := pkce(t)

	// The web client names no resource: it binds the acr-api one.
	token, err := h.oauth.Exchange(context.Background(), OAuthTokenRequest{
		GrantType: "authorization_code", Code: h.login(t, clientID, challenge, ""),
		RedirectURI: testRedirect, ClientID: clientID, CodeVerifier: verifier,
	})

	if err != nil {
		t.Fatal(err)
	}
	if token.ExpiresIn > 10*time.Minute {
		t.Fatalf("the web client's token lives %s, want at most 10m", token.ExpiresIn)
	}
}

// browserHarness is an OAuth harness whose client is the web's first-party
// client: the development static browser client on the test redirect, the
// acr-api resource (the issuer) beside the MCP one, and consent approvals
// signed in web session sid_1.
func browserHarness(t *testing.T) (*oauthHarness, string) {
	t.Helper()
	h := newOAuthHarnessWith(t, func(cfg *OAuthConfig) {
		cfg.Resources = []string{testResource, testIssuer}
		cfg.DevBrowserRedirectURIs = []string{testRedirect}
	})
	h.sessionID = "sid_1"
	return h, DevBrowserClientID
}
