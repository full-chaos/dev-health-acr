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

	token, err := h.oauth.Exchange(context.Background(), OAuthTokenRequest{
		GrantType: "authorization_code", Code: h.login(t, clientID, challenge, testResource),
		RedirectURI: testRedirect, ClientID: clientID, CodeVerifier: verifier, Resource: testResource,
	})

	if err != nil {
		t.Fatal(err)
	}
	if token.ExpiresIn > 10*time.Minute {
		t.Fatalf("the web client's token lives %s, want at most 10m", token.ExpiresIn)
	}
}

// browserHarness is an OAuth harness whose registered client is the web's
// first-party client. Before the browser class exists, nothing marks it.
func browserHarness(t *testing.T) (*oauthHarness, string) {
	t.Helper()
	h := newOAuthHarness(t)
	return h, h.register(t)
}
