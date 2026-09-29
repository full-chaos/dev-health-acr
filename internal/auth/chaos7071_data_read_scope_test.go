package auth

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7071 (decision K4): data:read is a separate grant. These tests pin
// the state the system exists to reach -- which credential scopes a real
// OAuth exchange issues -- for a client that names no scope (must be exactly
// the pre-CHAOS-7071 pair), one that names data:read (must carry it), and
// the RFC 8628 device grant (carries it only when requested).

func TestChaos7071OAuthExchangeIssuesDataReadOnlyWhenNamed(t *testing.T) {
	for requested, want := range map[string][]string{
		// Pre-CHAOS-7071 shapes: byte-identical outcome.
		"":                           {ScopeContextRead, ScopeEvidenceRead},
		"context:read":               {ScopeContextRead},
		"context:read evidence:read": {ScopeContextRead, ScopeEvidenceRead},
		// New shapes.
		"data:read":                            {ScopeDataRead},
		"context:read data:read":               {ScopeContextRead, ScopeDataRead},
		"data:read evidence:read context:read": {ScopeContextRead, ScopeEvidenceRead, ScopeDataRead},
	} {
		h := newOAuthHarness(t)
		clientID := h.register(t)
		verifier, challenge := pkce(t)
		authorization, err := h.oauth.Authorize(context.Background(), OAuthAuthorizeRequest{ResponseType: "code", ClientID: clientID, RedirectURI: testRedirect, CodeChallenge: challenge, CodeChallengeMethod: "S256", Scope: requested})
		if err != nil {
			t.Fatalf("requested %q: authorize: %v", requested, err)
		}
		redirect := h.approve(t, authorization.Handle, []string{"org/repo"})
		token, err := h.oauth.Exchange(context.Background(), OAuthTokenRequest{GrantType: "authorization_code", Code: codeFrom(t, redirect), RedirectURI: testRedirect, ClientID: clientID, CodeVerifier: verifier})
		if err != nil {
			t.Fatalf("requested %q: exchange: %v", requested, err)
		}
		// The token's scope string is in canonical OAuth order; the stored
		// credential's scope list is sorted by the credential normalizer.
		if token.Scope != strings.Join(want, " ") || !slices.Equal(token.Issued.Credential.Scopes, slices.Sorted(slices.Values(want))) {
			t.Fatalf("requested %q: token scope %q credential scopes %v, want %v", requested, token.Scope, token.Issued.Credential.Scopes, want)
		}
	}
}

// A default approval must never authorize data:read, so a redeem that asks
// for it after a consent that did not name it is refused.
func TestChaos7071DefaultApprovalNeverAuthorizesDataRead(t *testing.T) {
	h := newOAuthHarness(t)
	started, err := h.devices.StartForOAuth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.approveDevice(t, started.DeviceCodeHash, []string{"org/repo"})
	for _, scopes := range [][]string{{ScopeDataRead}, {ScopeContextRead, ScopeDataRead}} {
		if issued, err := h.devices.RedeemForResource(context.Background(), started.DeviceCodeHash, testResource, scopes); err == nil {
			t.Fatalf("scopes %v redeemed as %v after a default approval", scopes, issued.Credential.Scopes)
		}
	}
}

func TestChaos7071ApprovalScopeSetIsClosed(t *testing.T) {
	for _, scopes := range [][]string{nil, {}, {ScopeContextRead}, {ScopeDataRead}, {ScopeContextRead, ScopeEvidenceRead, ScopeContextAdmin}, {ScopeDataRead, ScopeContextRead, ScopeEvidenceRead}} {
		if validApprovalScopes(scopes) {
			t.Fatalf("approval scope set %v accepted", scopes)
		}
	}
	for _, scopes := range [][]string{{ScopeContextRead, ScopeEvidenceRead}, {ScopeContextRead, ScopeEvidenceRead, ScopeDataRead}} {
		if !validApprovalScopes(scopes) {
			t.Fatalf("approval scope set %v refused", scopes)
		}
	}
	if got := oauthApprovalScopes("context:read"); !slices.Equal(got, []string{ScopeContextRead, ScopeEvidenceRead}) {
		t.Fatalf("approval for context:read = %v", got)
	}
	if got := oauthApprovalScopes("context:read data:read"); !slices.Equal(got, []string{ScopeContextRead, ScopeEvidenceRead, ScopeDataRead}) {
		t.Fatalf("approval for data:read = %v", got)
	}
}

// chris 2026-09-28 ("don't block it"): the RFC 8628 device grant may request
// data:read like any other requestable scope. Driven through the PRODUCTION
// typed-user-code approval (DeviceFlowService.Approve), the path a headless
// client's user takes. The token carries data:read only when the grant
// asked for it; a grant without it gets the default pair.
func TestChaos7100DeviceGrantIssuesDataReadOnlyWhenRequested(t *testing.T) {
	for requested, want := range map[string][]string{
		"":                       {ScopeContextRead, ScopeEvidenceRead},
		"context:read":           {ScopeContextRead},
		"data:read":              {ScopeDataRead},
		"context:read data:read": {ScopeContextRead, ScopeDataRead},
	} {
		h := newOAuthHarness(t)
		clientID := h.register(t)
		started, err := h.oauth.StartDeviceAuthorization(context.Background(), OAuthDeviceAuthorizationRequest{ClientID: clientID, Scope: requested})
		if err != nil {
			t.Fatalf("requested %q: start: %v", requested, err)
		}
		if _, err := h.devices.Approve(context.Background(), DeviceApprovalRequest{Principal: webPrincipal([]string{"org/repo"}), UserCode: started.UserCode, RepositoryScopes: []string{"org/repo"}}); err != nil {
			t.Fatalf("requested %q: typed-code approval: %v", requested, err)
		}
		h.now = h.now.Add(storage.DeviceAuthorizationPollInterval)
		token, err := h.oauth.ExchangeDeviceCode(context.Background(), OAuthDeviceTokenRequest{GrantType: OAuthDeviceCodeGrantType, DeviceCode: started.DeviceCode, ClientID: clientID})
		if err != nil {
			t.Fatalf("requested %q: poll: %v", requested, err)
		}
		if token.Scope != strings.Join(want, " ") || !slices.Equal(token.Issued.Credential.Scopes, slices.Sorted(slices.Values(want))) {
			t.Fatalf("requested %q: token scope %q credential scopes %v, want %v", requested, token.Scope, token.Issued.Credential.Scopes, want)
		}
	}
}

// The legacy device flow (acr-mcp login's JSON device poll) has no scope
// parameter: after the same typed-code approval its credential is exactly
// the default pair, never data:read.
func TestChaos7100LegacyDevicePollNeverGetsDataRead(t *testing.T) {
	h := newOAuthHarness(t)
	started, err := h.devices.Start(context.Background(), DeviceAuthorizationHints{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.devices.Approve(context.Background(), DeviceApprovalRequest{Principal: webPrincipal([]string{"org/repo"}), UserCode: started.UserCode, RepositoryScopes: []string{"org/repo"}}); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(storage.DeviceAuthorizationPollInterval)
	issued, err := h.devices.Poll(context.Background(), started.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(issued.Credential.Scopes, []string{ScopeContextRead, ScopeEvidenceRead}) {
		t.Fatalf("legacy device credential scopes %v", issued.Credential.Scopes)
	}
}

// CHAOS-7106: the typed-code approval page's lookup (Preview) reports the
// scopes the device grant asked for. A legacy device authorization (acr-mcp
// login's JSON flow) has no grant row and no scope parameter, so it shows the
// default pair.
func TestChaos7106PreviewReportsRequestedScopes(t *testing.T) {
	for requested, want := range map[string][]string{
		"":                       {ScopeContextRead, ScopeEvidenceRead},
		"data:read":              {ScopeDataRead},
		"context:read data:read": {ScopeContextRead, ScopeDataRead},
	} {
		h := newOAuthHarness(t)
		clientID := h.register(t)
		started, err := h.oauth.StartDeviceAuthorization(context.Background(), OAuthDeviceAuthorizationRequest{ClientID: clientID, Scope: requested})
		if err != nil {
			t.Fatalf("requested %q: start: %v", requested, err)
		}
		preview, err := h.devices.Preview(context.Background(), DeviceApprovalPreviewRequest{Principal: webPrincipal([]string{"org/repo"}), UserCode: started.UserCode})
		if err != nil {
			t.Fatalf("requested %q: preview: %v", requested, err)
		}
		if !slices.Equal(preview.RequestedScopes, want) {
			t.Fatalf("requested %q: preview scopes %v, want %v", requested, preview.RequestedScopes, want)
		}
	}
}

func TestChaos7106LegacyDevicePreviewShowsDefaultScopes(t *testing.T) {
	h := newOAuthHarness(t)
	started, err := h.devices.Start(context.Background(), DeviceAuthorizationHints{})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := h.devices.Preview(context.Background(), DeviceApprovalPreviewRequest{Principal: webPrincipal([]string{"org/repo"}), UserCode: started.UserCode})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(preview.RequestedScopes, []string{ScopeContextRead, ScopeEvidenceRead}) {
		t.Fatalf("legacy preview scopes %v, want the default pair", preview.RequestedScopes)
	}
	if preview.RequestedScopesSource != PreviewScopesLegacyDefault {
		t.Fatalf("legacy preview source %q, want %q", preview.RequestedScopesSource, PreviewScopesLegacyDefault)
	}
}
