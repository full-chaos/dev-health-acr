package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// TestReproTheTokenEndpointSupportsTheRefreshGrant reproduces the gap the
// browser refresh grant closes: /token refuses grant_type=refresh_token
// outright, so a 10-minute browser credential cannot be renewed. With the
// grant, an unknown refresh token is an invalid_grant, not an unsupported
// grant type.
func TestReproTheTokenEndpointSupportsTheRefreshGrant(t *testing.T) {
	// The refresh grant serves the first-party browser client: the
	// development static client, with the acr-api resource.
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{
		Issuer: oauthTestIssuer, Resources: []string{oauthTestResource, oauthTestIssuer}, DevBrowserRedirectURIs: []string{oauthTestRedirect},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"fcacr_rt_" + strings.Repeat("a", 43)}, "client_id": {auth.DevBrowserClientID}}
	request := httptest.NewRequest(http.MethodPost, OAuthTokenPath, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()

	app.Handler().ServeHTTP(recorder, request)

	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	if body.Error != "invalid_grant" {
		t.Fatalf("refresh grant with an unknown token: %d %s, want invalid_grant", recorder.Code, recorder.Body.String())
	}
}
