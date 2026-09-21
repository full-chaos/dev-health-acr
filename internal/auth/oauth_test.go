package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

const (
	testIssuer   = "https://acr.example.test"
	testResource = "https://mcp.example.test/mcp"
	testRedirect = "http://127.0.0.1:4711/callback"
)

type oauthHarness struct {
	now     time.Time
	oauth   *OAuthService
	devices *DeviceFlowService
	store   storage.DeviceAuthorizationStore
	creds   *storage.CredentialLifecycle
	meta    *fakeMetadata
}

type fakeMetadata struct {
	documents map[string]OAuthClientMetadata
	calls     int
}

func (f *fakeMetadata) Fetch(_ context.Context, clientID string) (OAuthClientMetadata, error) {
	f.calls++
	document, ok := f.documents[clientID]
	if !ok {
		return OAuthClientMetadata{}, ErrClientMetadataUnavailable
	}
	return document, nil
}

func newOAuthHarness(t *testing.T, resources ...string) *oauthHarness {
	t.Helper()
	h := &oauthHarness{now: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC), meta: &fakeMetadata{documents: map[string]OAuthClientMetadata{}}}
	clock := func() time.Time { return h.now }
	credentials, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: memory.NewAuditStore(), Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	h.creds = credentials
	store, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentials, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	h.store = store
	service, err := NewService(credentials, ServiceOptions{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	h.devices, err = NewDeviceFlowService(store, service, DeviceFlowOptions{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) == 0 {
		resources = []string{testResource}
	}
	h.oauth, err = NewOAuthService(memory.NewOAuthStore(clock), h.devices, OAuthConfig{Issuer: testIssuer, Resources: resources, ClientMetadata: h.meta, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func pkce(t *testing.T) (string, string) {
	t.Helper()
	verifier := strings.Repeat("v", 50)
	digest := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(digest[:])
}

func (h *oauthHarness) register(t *testing.T) string {
	t.Helper()
	client, err := h.oauth.Register(context.Background(), OAuthRegistrationRequest{ClientName: "test", RedirectURIs: []string{testRedirect}})
	if err != nil {
		t.Fatal(err)
	}
	return client.ClientID
}

// approve approves the user code as a web assertion principal would.
func (h *oauthHarness) approve(t *testing.T, userCode string, repositories []string) {
	t.Helper()
	_, err := h.devices.Approve(context.Background(), DeviceApprovalRequest{
		Principal: storage.Principal{
			AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user_1", OrgID: "11111111-1111-4111-8111-111111111111",
			RepositoryScopes: repositories, Permissions: []string{WebAssertionPermissionCredentialIssue},
		},
		UserCode: userCode, RepositoryScopes: repositories,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func codeFrom(t *testing.T, redirect string) string {
	t.Helper()
	parsed, err := url.Parse(redirect)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query().Get("code")
}

func outcomeOf(err error) string {
	var oauthErr *OAuthError
	if errors.As(err, &oauthErr) {
		return oauthErr.Outcome
	}
	if err == nil {
		return "nil"
	}
	return "other:" + err.Error()
}

// login runs authorize → approve → consent and returns the code.
func (h *oauthHarness) login(t *testing.T, clientID, challenge, resource string) string {
	t.Helper()
	authorization, err := h.oauth.Authorize(context.Background(), OAuthAuthorizeRequest{
		ResponseType: "code", ClientID: clientID, RedirectURI: testRedirect, CodeChallenge: challenge,
		CodeChallengeMethod: "S256", Resource: resource, State: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.approve(t, authorization.UserCode, []string{"org/repo"})
	consent, err := h.oauth.Consent(context.Background(), authorization.Handle)
	if err != nil || consent.State != OAuthConsentApproved {
		t.Fatalf("consent = %+v, %v", consent, err)
	}
	return codeFrom(t, consent.RedirectURL)
}

func TestOAuthExchangeChecksEveryBinding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*OAuthTokenRequest)
		want   string
	}{
		{"valid", func(*OAuthTokenRequest) {}, "nil"},
		{"resource omitted uses the authorized one", func(r *OAuthTokenRequest) { r.Resource = "" }, "nil"},
		{"grant type", func(r *OAuthTokenRequest) { r.GrantType = "refresh_token" }, oauthvocab.OutcomeUnsupportedGrantType},
		{"unknown code", func(r *OAuthTokenRequest) { r.Code = "not-a-code" }, oauthvocab.OutcomeInvalidGrant},
		{"empty code", func(r *OAuthTokenRequest) { r.Code = "" }, oauthvocab.OutcomeInvalidGrant},
		{"other client", func(r *OAuthTokenRequest) { r.ClientID = "acrc_00000000000000000000000000000000" }, oauthvocab.OutcomeClientMismatch},
		{"other redirect", func(r *OAuthTokenRequest) { r.RedirectURI = "http://127.0.0.1:4711/other" }, oauthvocab.OutcomeRedirectMismatch},
		{"wrong verifier", func(r *OAuthTokenRequest) { r.CodeVerifier = strings.Repeat("w", 50) }, oauthvocab.OutcomePKCEMismatch},
		{"missing verifier", func(r *OAuthTokenRequest) { r.CodeVerifier = "" }, oauthvocab.OutcomePKCEMismatch},
		{"short verifier", func(r *OAuthTokenRequest) { r.CodeVerifier = "abc" }, oauthvocab.OutcomePKCEMismatch},
		{"other resource", func(r *OAuthTokenRequest) { r.Resource = "https://other.example.test/mcp" }, oauthvocab.OutcomeResourceMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newOAuthHarness(t)
			clientID := h.register(t)
			verifier, challenge := pkce(t)
			request := OAuthTokenRequest{GrantType: "authorization_code", Code: h.login(t, clientID, challenge, testResource), RedirectURI: testRedirect, ClientID: clientID, CodeVerifier: verifier, Resource: testResource}
			tc.mutate(&request)
			token, err := h.oauth.Exchange(context.Background(), request)
			if got := outcomeOf(err); got != tc.want {
				t.Fatalf("outcome = %s, want %s", got, tc.want)
			}
			if tc.want != "nil" {
				return
			}
			if token.Issued.Credential.Resource != testResource || token.Scope != "context:read evidence:read" || token.ExpiresIn != DeviceCredentialLifetime {
				t.Fatalf("token = resource %q scope %q expires_in %s", token.Issued.Credential.Resource, token.Scope, token.ExpiresIn)
			}
		})
	}
}

func TestOAuthAuthorizeRefusals(t *testing.T) {
	h := newOAuthHarness(t, testResource, "https://second.example.test/mcp")
	clientID := h.register(t)
	_, challenge := pkce(t)
	base := OAuthAuthorizeRequest{ResponseType: "code", ClientID: clientID, RedirectURI: testRedirect, CodeChallenge: challenge, CodeChallengeMethod: "S256", Resource: testResource}
	for _, tc := range []struct {
		name         string
		mutate       func(*OAuthAuthorizeRequest)
		want         string
		redirectable bool
	}{
		{"unknown client", func(r *OAuthAuthorizeRequest) { r.ClientID = "acrc_00000000000000000000000000000000" }, oauthvocab.OutcomeInvalidClient, false},
		{"malformed client", func(r *OAuthAuthorizeRequest) { r.ClientID = "claude" }, oauthvocab.OutcomeInvalidClient, false},
		{"unregistered redirect", func(r *OAuthAuthorizeRequest) { r.RedirectURI = "http://127.0.0.1:4711/x" }, oauthvocab.OutcomeInvalidRedirectURI, false},
		{"missing redirect", func(r *OAuthAuthorizeRequest) { r.RedirectURI = "" }, oauthvocab.OutcomeInvalidRedirectURI, false},
		{"token response type", func(r *OAuthAuthorizeRequest) { r.ResponseType = "token" }, oauthvocab.OutcomeUnsupportedResponseType, true},
		{"no challenge", func(r *OAuthAuthorizeRequest) { r.CodeChallenge = "" }, oauthvocab.OutcomePKCERequired, true},
		{"plain method", func(r *OAuthAuthorizeRequest) { r.CodeChallengeMethod = "plain" }, oauthvocab.OutcomePKCERequired, true},
		{"short challenge", func(r *OAuthAuthorizeRequest) { r.CodeChallenge = "abc" }, oauthvocab.OutcomePKCERequired, true},
		{"unknown resource", func(r *OAuthAuthorizeRequest) { r.Resource = "https://evil.example.test/mcp" }, oauthvocab.OutcomeInvalidTarget, true},
		{"no resource with two configured", func(r *OAuthAuthorizeRequest) { r.Resource = "" }, oauthvocab.OutcomeInvalidTarget, true},
		{"control in state", func(r *OAuthAuthorizeRequest) { r.State = "a\nb" }, oauthvocab.OutcomeInvalidRequest, true},
		{"oversized state", func(r *OAuthAuthorizeRequest) { r.State = strings.Repeat("s", 1025) }, oauthvocab.OutcomeInvalidRequest, true},
		{"unknown scope", func(r *OAuthAuthorizeRequest) { r.Scope = "context:admin" }, oauthvocab.OutcomeInvalidScope, true},
		{"one unknown scope among known", func(r *OAuthAuthorizeRequest) { r.Scope = "context:read episode:write" }, oauthvocab.OutcomeInvalidScope, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := base
			tc.mutate(&request)
			_, err := h.oauth.Authorize(context.Background(), request)
			var oauthErr *OAuthError
			if !errors.As(err, &oauthErr) || oauthErr.Outcome != tc.want || oauthErr.Redirectable != tc.redirectable {
				t.Fatalf("err = %v, want outcome %s redirectable %v", err, tc.want, tc.redirectable)
			}
			if tc.redirectable != (oauthErr.RedirectURL != "") {
				t.Fatalf("redirect URL %q for redirectable=%v", oauthErr.RedirectURL, tc.redirectable)
			}
			if tc.redirectable {
				target, _ := url.Parse(oauthErr.RedirectURL)
				if !strings.HasPrefix(oauthErr.RedirectURL, testRedirect+"?") || target.Query().Get("error") != oauthErr.Code || target.Query().Get("iss") != testIssuer || target.Query().Get("state") != request.State {
					t.Fatalf("refusal redirect = %q", oauthErr.RedirectURL)
				}
			}
		})
	}
	// A single configured resource is the default when none is named.
	single := newOAuthHarness(t)
	singleClient := single.register(t)
	request := base
	request.ClientID, request.Resource = singleClient, ""
	authorization, err := single.oauth.Authorize(context.Background(), request)
	if err != nil || authorization.Resource != testResource {
		t.Fatalf("default resource = %q, %v", authorization.Resource, err)
	}
}

func TestOAuthConsentStates(t *testing.T) {
	h := newOAuthHarness(t)
	clientID := h.register(t)
	_, challenge := pkce(t)
	start := func() OAuthAuthorization {
		authorization, err := h.oauth.Authorize(context.Background(), OAuthAuthorizeRequest{ResponseType: "code", ClientID: clientID, RedirectURI: testRedirect, CodeChallenge: challenge, CodeChallengeMethod: "S256", State: "st"})
		if err != nil {
			t.Fatal(err)
		}
		return authorization
	}
	pending := start()
	if consent, err := h.oauth.Consent(context.Background(), pending.Handle); err != nil || consent.State != OAuthConsentPending || consent.RedirectURL != "" {
		t.Fatalf("pending consent = %+v, %v", consent, err)
	}
	if _, err := h.oauth.Consent(context.Background(), "unknown-handle"); outcomeOf(err) != oauthvocab.OutcomeInvalidRequest {
		t.Fatalf("unknown handle = %v", err)
	}
	approved := start()
	h.approve(t, approved.UserCode, []string{"org/repo"})
	consent, err := h.oauth.Consent(context.Background(), approved.Handle)
	if err != nil || consent.State != OAuthConsentApproved {
		t.Fatalf("approved consent = %+v, %v", consent, err)
	}
	redirect, _ := url.Parse(consent.RedirectURL)
	if redirect.Query().Get("state") != "st" || redirect.Query().Get("iss") != testIssuer || redirect.Query().Get("code") == "" || redirect.Host != "127.0.0.1:4711" {
		t.Fatalf("redirect = %s", consent.RedirectURL)
	}
	if _, err := h.oauth.Consent(context.Background(), approved.Handle); outcomeOf(err) != oauthvocab.OutcomeAlreadyCompleted {
		t.Fatalf("second consent after the code was issued = %v, want already_completed", err)
	}
	h.now = h.now.Add(storage.DeviceAuthorizationTTL + time.Second)
	if consent, err := h.oauth.Consent(context.Background(), pending.Handle); err != nil || consent.State != OAuthConsentExpired {
		t.Fatalf("expired consent = %+v, %v", consent, err)
	}
}

func TestOAuthCodeExpiresAndDeviceGrantCannotRedeem(t *testing.T) {
	h := newOAuthHarness(t)
	clientID := h.register(t)
	verifier, challenge := pkce(t)
	code := h.login(t, clientID, challenge, testResource)
	h.now = h.now.Add(storage.OAuthAuthorizationCodeTTL + time.Second)
	_, err := h.oauth.Exchange(context.Background(), OAuthTokenRequest{GrantType: "authorization_code", Code: code, RedirectURI: testRedirect, ClientID: clientID, CodeVerifier: verifier})
	if outcomeOf(err) != oauthvocab.OutcomeInvalidGrant {
		t.Fatalf("expired code = %v, want invalid_grant", err)
	}
	// The consent's device authorization was approved, but no raw device
	// code exists anywhere: the device grant has nothing to poll with, and
	// the code itself is not a device code.
	if _, err := h.devices.Poll(context.Background(), code); err == nil {
		t.Fatal("the device grant redeemed an OAuth authorization code")
	}
}

func TestOAuthRegisterRefusals(t *testing.T) {
	h := newOAuthHarness(t)
	for _, tc := range []struct {
		name    string
		request OAuthRegistrationRequest
		want    string
	}{
		{"confidential", OAuthRegistrationRequest{RedirectURIs: []string{testRedirect}, TokenEndpointAuthMethod: "client_secret_basic"}, oauthvocab.OutcomeInvalidClientMetadata},
		{"implicit grant", OAuthRegistrationRequest{RedirectURIs: []string{testRedirect}, GrantTypes: []string{"implicit"}}, oauthvocab.OutcomeInvalidClientMetadata},
		{"token response", OAuthRegistrationRequest{RedirectURIs: []string{testRedirect}, ResponseTypes: []string{"token"}}, oauthvocab.OutcomeInvalidClientMetadata},
		{"no redirect", OAuthRegistrationRequest{}, oauthvocab.OutcomeInvalidRedirectURI},
		{"http non-loopback redirect", OAuthRegistrationRequest{RedirectURIs: []string{"http://example.test/cb"}}, oauthvocab.OutcomeInvalidRedirectURI},
		{"fragment redirect", OAuthRegistrationRequest{RedirectURIs: []string{"https://example.test/cb#x"}}, oauthvocab.OutcomeInvalidRedirectURI},
		{"custom scheme redirect", OAuthRegistrationRequest{RedirectURIs: []string{"javascript:alert(1)"}}, oauthvocab.OutcomeInvalidRedirectURI},
		{"duplicate redirects", OAuthRegistrationRequest{RedirectURIs: []string{testRedirect, testRedirect}}, oauthvocab.OutcomeInvalidClientMetadata},
		{"control in name", OAuthRegistrationRequest{ClientName: "a\x00b", RedirectURIs: []string{testRedirect}}, oauthvocab.OutcomeInvalidClientMetadata},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.oauth.Register(context.Background(), tc.request); outcomeOf(err) != tc.want {
				t.Fatalf("outcome = %s, want %s", outcomeOf(err), tc.want)
			}
		})
	}
	client, err := h.oauth.Register(context.Background(), OAuthRegistrationRequest{RedirectURIs: []string{testRedirect}, GrantTypes: []string{"authorization_code", "refresh_token"}, TokenEndpointAuthMethod: "none"})
	if err != nil || !storage.IsDynamicOAuthClientID(client.ClientID) {
		t.Fatalf("refresh_token in the request is accepted and dropped: %+v %v", client, err)
	}
}

func TestOAuthClientMetadataDocumentResolution(t *testing.T) {
	h := newOAuthHarness(t)
	id := "https://client.example.test/oauth/client.json"
	for _, tc := range []struct {
		name     string
		document *OAuthClientMetadata
		want     string
	}{
		{"valid", &OAuthClientMetadata{ClientID: id, RedirectURIs: []string{testRedirect}}, ""},
		{"absent", nil, oauthvocab.OutcomeInvalidClientMetadata},
		{"other client id", &OAuthClientMetadata{ClientID: "https://evil.example.test/c.json", RedirectURIs: []string{testRedirect}}, oauthvocab.OutcomeInvalidClientMetadata},
		{"no redirects", &OAuthClientMetadata{ClientID: id}, oauthvocab.OutcomeInvalidClientMetadata},
		{"confidential", &OAuthClientMetadata{ClientID: id, RedirectURIs: []string{testRedirect}, TokenEndpointAuthMethod: "private_key_jwt"}, oauthvocab.OutcomeInvalidClientMetadata},
		{"bad redirect", &OAuthClientMetadata{ClientID: id, RedirectURIs: []string{"http://evil.example.test/cb"}}, oauthvocab.OutcomeInvalidClientMetadata},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delete(h.meta.documents, id)
			if tc.document != nil {
				h.meta.documents[id] = *tc.document
			}
			client, err := h.oauth.ResolveClient(context.Background(), id)
			if tc.want == "" {
				if err != nil || client.Kind != storage.OAuthClientKindMetadataDocument {
					t.Fatalf("resolve = %+v %v", client, err)
				}
				return
			}
			if outcomeOf(err) != tc.want {
				t.Fatalf("outcome = %s, want %s", outcomeOf(err), tc.want)
			}
		})
	}
	for _, id := range []string{"http://client.example.test/c.json", "https://client.example.test/", "https://client.example.test", "https://client.example.test/a/../c.json"} {
		calls := h.meta.calls
		if _, err := h.oauth.ResolveClient(context.Background(), id); outcomeOf(err) != oauthvocab.OutcomeInvalidClient || h.meta.calls != calls {
			t.Fatalf("client id %q: %v (fetched %d times), want invalid_client without a fetch", id, err, h.meta.calls-calls)
		}
	}
}

func TestPublicAddress(t *testing.T) {
	for address, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "0.0.0.0": false, "::1": false, "fe80::1": false, "fc00::1": false, "::ffff:10.0.0.1": false,
		"::ffff:127.0.0.1": false, "224.0.0.1": false, "198.18.0.1": false, "::": false, "64:ff9b::a00:1": false,
	} {
		if got := PublicAddress(netip.MustParseAddr(address)); got != want {
			t.Errorf("PublicAddress(%s) = %v, want %v", address, got, want)
		}
	}
}

// TestClientMetadataDocumentsAreOffWithoutAFetcher: with no fetcher (or a
// typed-nil one) an HTTPS client ID is just an unknown client and nothing is
// fetched.
func TestClientMetadataDocumentsAreOffWithoutAFetcher(t *testing.T) {
	id := "https://client.example.test/oauth/client.json"
	for name, fetcher := range map[string]OAuthClientMetadataFetcher{"nil": nil, "typed nil": (*HTTPClientMetadataFetcher)(nil)} {
		t.Run(name, func(t *testing.T) {
			h := newOAuthHarness(t)
			service, err := NewOAuthService(memory.NewOAuthStore(func() time.Time { return h.now }), h.devices, OAuthConfig{Issuer: testIssuer, Resources: []string{testResource}, ClientMetadata: fetcher})
			if err != nil {
				t.Fatal(err)
			}
			if service.ClientMetadataDocumentsSupported() {
				t.Fatal("metadata documents reported as supported without a fetcher")
			}
			if _, err := service.ResolveClient(context.Background(), id); outcomeOf(err) != oauthvocab.OutcomeInvalidClient {
				t.Fatalf("resolve = %v, want invalid_client", err)
			}
		})
	}
	h := newOAuthHarness(t)
	if !h.oauth.ClientMetadataDocumentsSupported() {
		t.Fatal("metadata documents not supported with a fetcher")
	}
}

func TestUnknownClientIDShapesAreRefused(t *testing.T) {
	h := newOAuthHarness(t)
	for _, id := range []string{"claude", "acrc_00000000000000000000000000000000", ""} {
		if _, err := h.oauth.ResolveClient(context.Background(), id); outcomeOf(err) != oauthvocab.OutcomeInvalidClient {
			t.Fatalf("client id %q: %v, want invalid_client", id, err)
		}
	}
}

func TestVerifyPKCES256(t *testing.T) {
	verifier, challenge := pkce(t)
	if !VerifyPKCES256(verifier, challenge) {
		t.Fatal("matching verifier refused")
	}
	for _, bad := range []string{"", strings.Repeat("v", 42), strings.Repeat("v", 129), strings.Repeat("v", 49) + "!", strings.Repeat("w", 50)} {
		if VerifyPKCES256(bad, challenge) {
			t.Fatalf("verifier %q accepted", bad)
		}
	}
	// A verifier outside 43..128 characters is refused even when its digest
	// matches the challenge the client chose.
	for _, weak := range []string{"short", strings.Repeat("v", 42), strings.Repeat("v", 129)} {
		digest := sha256.Sum256([]byte(weak))
		if VerifyPKCES256(weak, base64.RawURLEncoding.EncodeToString(digest[:])) {
			t.Fatalf("verifier of length %d accepted", len(weak))
		}
	}
	if VerifyPKCES256(verifier, "") || VerifyPKCES256(verifier, challenge[:42]) {
		t.Fatal("malformed challenge accepted")
	}
}

func TestResourceAdmitted(t *testing.T) {
	for _, tc := range []struct {
		bound     string
		presented []string
		want      bool
	}{
		{"", nil, true},
		{"", []string{testResource}, true},
		{testResource, []string{testResource}, true},
		{testResource, nil, false},
		{testResource, []string{""}, false},
		{testResource, []string{"https://other.example.test/mcp"}, false},
		{testResource, []string{testResource, testResource}, false},
		{testResource, []string{testResource + "/"}, false},
	} {
		if got := resourceAdmitted(tc.bound, tc.presented); got != tc.want {
			t.Errorf("resourceAdmitted(%q, %q) = %v, want %v", tc.bound, tc.presented, got, tc.want)
		}
	}
}

func TestNewOAuthServiceRefusesBadConfiguration(t *testing.T) {
	h := newOAuthHarness(t)
	store := memory.NewOAuthStore(time.Now)
	for name, cfg := range map[string]OAuthConfig{
		"no issuer":          {Resources: []string{testResource}},
		"issuer with path":   {Issuer: testIssuer + "/x", Resources: []string{testResource}},
		"issuer trailing /":  {Issuer: testIssuer + "/", Resources: []string{testResource}},
		"http issuer":        {Issuer: "http://acr.example.test", Resources: []string{testResource}},
		"no resources":       {Issuer: testIssuer},
		"http resource":      {Issuer: testIssuer, Resources: []string{"http://mcp.example.test/mcp"}},
		"duplicate resource": {Issuer: testIssuer, Resources: []string{testResource, testResource}},
	} {
		if _, err := NewOAuthService(store, h.devices, cfg); !errors.Is(err, ErrInvalidOAuthConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidOAuthConfig", name, err)
		}
	}
}

func TestRedeemForResourceRefusesAnUnapprovedAuthorization(t *testing.T) {
	h := newOAuthHarness(t)
	started, err := h.devices.StartForOAuth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.devices.RedeemForResource(context.Background(), started.DeviceCodeHash, testResource, []string{ScopeContextRead}); !errors.Is(err, ErrOAuthDeviceNotApproved) {
		t.Fatalf("pending authorization: err = %v, want ErrOAuthDeviceNotApproved", err)
	}
	h.approve(t, started.UserCode, []string{"org/repo"})
	h.now = h.now.Add(storage.DeviceAuthorizationTTL + time.Second)
	if state, err := h.devices.StateForOAuth(context.Background(), started.DeviceCodeHash); err != nil || state != OAuthDeviceStateExpired {
		t.Fatalf("expired approval state = %q, %v, want expired", state, err)
	}
	if _, err := h.devices.RedeemForResource(context.Background(), started.DeviceCodeHash, testResource, []string{ScopeContextRead}); !errors.Is(err, ErrOAuthDeviceNotApproved) {
		t.Fatalf("expired approval: err = %v, want ErrOAuthDeviceNotApproved", err)
	}
}

func TestNormalizeOAuthScope(t *testing.T) {
	for raw, want := range map[string]string{
		"": "context:read evidence:read", "   ": "context:read evidence:read",
		"context:read": "context:read", "evidence:read": "evidence:read",
		"evidence:read context:read": "context:read evidence:read", "context:read context:read": "context:read",
		"context:admin": "!", "context:read episode:write": "!", "CONTEXT:READ": "!", "context:read,evidence:read": "!",
	} {
		got, ok := NormalizeOAuthScope(raw)
		if want == "!" {
			if ok {
				t.Errorf("NormalizeOAuthScope(%q) = %q, want refusal", raw, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("NormalizeOAuthScope(%q) = %q %v, want %q", raw, got, ok, want)
		}
	}
	if !slices.Equal(oauthvocab.ScopeVocabulary(), []string{ScopeContextRead, ScopeEvidenceRead}) {
		t.Fatalf("oauthvocab scopes %v drifted from the auth scope constants", oauthvocab.ScopeVocabulary())
	}
}

func TestOAuthTokenCarriesOnlyTheRequestedScopes(t *testing.T) {
	for requested, want := range map[string][]string{
		"":                           {ScopeContextRead, ScopeEvidenceRead},
		"context:read":               {ScopeContextRead},
		"evidence:read":              {ScopeEvidenceRead},
		"evidence:read context:read": {ScopeContextRead, ScopeEvidenceRead},
	} {
		h := newOAuthHarness(t)
		clientID := h.register(t)
		verifier, challenge := pkce(t)
		authorization, err := h.oauth.Authorize(context.Background(), OAuthAuthorizeRequest{ResponseType: "code", ClientID: clientID, RedirectURI: testRedirect, CodeChallenge: challenge, CodeChallengeMethod: "S256", Scope: requested})
		if err != nil {
			t.Fatal(err)
		}
		h.approve(t, authorization.UserCode, []string{"org/repo"})
		consent, err := h.oauth.Consent(context.Background(), authorization.Handle)
		if err != nil {
			t.Fatal(err)
		}
		token, err := h.oauth.Exchange(context.Background(), OAuthTokenRequest{GrantType: "authorization_code", Code: codeFrom(t, consent.RedirectURL), RedirectURI: testRedirect, ClientID: clientID, CodeVerifier: verifier})
		if err != nil {
			t.Fatal(err)
		}
		if token.Scope != strings.Join(want, " ") || !slices.Equal(token.Issued.Credential.Scopes, want) {
			t.Fatalf("requested %q: token scope %q credential scopes %v, want %v", requested, token.Scope, token.Issued.Credential.Scopes, want)
		}
	}
}

func TestRedeemForResourceNeverWidensTheApproval(t *testing.T) {
	h := newOAuthHarness(t)
	started, err := h.devices.StartForOAuth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.approve(t, started.UserCode, []string{"org/repo"})
	for _, scopes := range [][]string{{ScopeContextAdmin}, {ScopeContextRead, ScopeEpisodeWrite}, {}} {
		if issued, err := h.devices.RedeemForResource(context.Background(), started.DeviceCodeHash, testResource, scopes); err == nil {
			t.Fatalf("scopes %v redeemed as %v", scopes, issued.Credential.Scopes)
		}
	}
}
