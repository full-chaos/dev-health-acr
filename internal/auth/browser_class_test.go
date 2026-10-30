package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12123: the first-party browser client gets the browser class; every
// other client keeps what it had.

func browserLogin(t *testing.T, h *oauthHarness, clientID string) OAuthToken {
	t.Helper()
	verifier, challenge := pkce(t)
	token, err := h.oauth.Exchange(context.Background(), OAuthTokenRequest{
		GrantType: "authorization_code", Code: h.login(t, clientID, challenge, ""),
		RedirectURI: testRedirect, ClientID: clientID, CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestBrowserClient_exchangeIssuesTheBrowserClass(t *testing.T) {
	h, clientID := browserHarness(t)

	token := browserLogin(t, h, clientID)

	credential := token.Issued.Credential
	provenance := credential.Provenance
	if provenance.ClientClass != string(storage.CredentialClientClassBrowser) || provenance.ClientID != DevBrowserClientID ||
		provenance.ApprovingUserID != "user_1" || provenance.SessionID != "sid_1" {
		t.Fatalf("provenance = %+v, want browser / dev client / user_1 / sid_1", provenance)
	}
	if !strings.HasPrefix(provenance.FamilyID, "fam_") || len(provenance.FamilyID) != len("fam_")+22 {
		t.Fatalf("family id = %q, want fam_ + 22 base64url characters", provenance.FamilyID)
	}
	if credential.ExpiresAt == nil || !credential.ExpiresAt.Equal(h.now.Add(10*time.Minute)) || token.ExpiresIn != 10*time.Minute {
		t.Fatalf("expires at %v (in %s), want %v", credential.ExpiresAt, token.ExpiresIn, h.now.Add(10*time.Minute))
	}
	if provenance.FamilyExpiresAt == nil || !provenance.FamilyExpiresAt.Equal(h.now.Add(12*time.Hour)) {
		t.Fatalf("family expires at %v, want %v", provenance.FamilyExpiresAt, h.now.Add(12*time.Hour))
	}
	if credential.Resource != testIssuer {
		t.Fatalf("resource = %q, want the acr-api resource %q", credential.Resource, testIssuer)
	}
	stored, err := h.creds.FindByTokenHash(context.Background(), HashToken(token.Issued.Token))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Provenance.ClientClass != provenance.ClientClass || stored.Provenance.FamilyID != provenance.FamilyID || stored.Provenance.SessionID != "sid_1" {
		t.Fatalf("stored provenance = %+v", stored.Provenance)
	}
	// Two logins start two families.
	second := browserLogin(t, h, clientID)
	if second.Issued.Credential.Provenance.FamilyID == provenance.FamilyID {
		t.Fatal("two browser logins share a token family")
	}
}

func TestBrowserClient_otherClientsKeepTheirClassAndResource(t *testing.T) {
	h, _ := browserHarness(t)
	dynamic := h.register(t)
	verifier, challenge := pkce(t)

	// A client naming no resource binds the MCP one, as before the acr-api
	// resource was added beside it.
	token, err := h.oauth.Exchange(context.Background(), OAuthTokenRequest{
		GrantType: "authorization_code", Code: h.login(t, dynamic, challenge, ""),
		RedirectURI: testRedirect, ClientID: dynamic, CodeVerifier: verifier,
	})

	if err != nil {
		t.Fatal(err)
	}
	provenance := token.Issued.Credential.Provenance
	if provenance.ClientClass != string(storage.CredentialClientClassOAuthUser) || provenance.SessionID != "" || provenance.FamilyID != "" || provenance.FamilyExpiresAt != nil {
		t.Fatalf("provenance = %+v, want oauth_user without session or family", provenance)
	}
	if token.Issued.Credential.Resource != testResource || token.ExpiresIn != DeviceCredentialLifetime {
		t.Fatalf("resource %q, life %s; want %q and %s", token.Issued.Credential.Resource, token.ExpiresIn, testResource, DeviceCredentialLifetime)
	}
	// It may not bind the acr-api resource.
	_, err = h.oauth.Authorize(context.Background(), OAuthAuthorizeRequest{
		ResponseType: "code", ClientID: dynamic, RedirectURI: testRedirect, CodeChallenge: challenge,
		CodeChallengeMethod: "S256", Resource: testIssuer, State: "s1",
	})
	if got := outcomeOf(err); got != oauthvocab.OutcomeInvalidTarget {
		t.Fatalf("a dynamic client naming the acr-api resource: outcome %s, want %s", got, oauthvocab.OutcomeInvalidTarget)
	}
}

func TestBrowserClient_authorizeIsExact(t *testing.T) {
	h, clientID := browserHarness(t)
	_, challenge := pkce(t)
	for name, tc := range map[string]struct {
		redirect, resource, want string
	}{
		"another loopback port":     {"http://127.0.0.1:4712/callback", "", oauthvocab.OutcomeInvalidRedirectURI},
		"another path":              {"http://127.0.0.1:4711/other", "", oauthvocab.OutcomeInvalidRedirectURI},
		"the MCP resource":          {testRedirect, testResource, oauthvocab.OutcomeInvalidTarget},
		"the acr-api resource":      {testRedirect, testIssuer, "nil"},
		"no resource (the acr-api)": {testRedirect, "", "nil"},
	} {
		_, err := h.oauth.Authorize(context.Background(), OAuthAuthorizeRequest{
			ResponseType: "code", ClientID: clientID, RedirectURI: tc.redirect, CodeChallenge: challenge,
			CodeChallengeMethod: "S256", Resource: tc.resource, State: "s1",
		})
		if got := outcomeOf(err); got != tc.want {
			t.Errorf("%s: outcome %s, want %s", name, got, tc.want)
		}
	}
}

func browserAuthorization(t *testing.T, h *oauthHarness, clientID string) OAuthAuthorization {
	t.Helper()
	_, challenge := pkce(t)
	authorization, err := h.oauth.Authorize(context.Background(), OAuthAuthorizeRequest{
		ResponseType: "code", ClientID: clientID, RedirectURI: testRedirect, CodeChallenge: challenge,
		CodeChallengeMethod: "S256", State: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return authorization
}

func TestBrowserClient_consentNeedsTheWebSession(t *testing.T) {
	h, clientID := browserHarness(t)
	authorization := browserAuthorization(t, h, clientID)

	_, err := h.oauth.ApproveConsent(context.Background(), authorization.Handle, webPrincipal([]string{"org/repo"}), []string{"org/repo"})

	if got := outcomeOf(err); got != oauthvocab.OutcomeSessionRequired {
		t.Fatalf("browser consent without sid: outcome %s, want %s", got, oauthvocab.OutcomeSessionRequired)
	}
	// Nothing was approved: the same user approves with its session.
	principal := webPrincipal([]string{"org/repo"})
	principal.SessionID = "sid_1"
	if _, err := h.oauth.ApproveConsent(context.Background(), authorization.Handle, principal, []string{"org/repo"}); err != nil {
		t.Fatalf("approval with the session after a refusal: %v", err)
	}
	// A third-party client still needs no session.
	other := newOAuthHarness(t)
	verifier, challenge := pkce(t)
	dynamic := other.register(t)
	if _, err := other.oauth.Exchange(context.Background(), OAuthTokenRequest{
		GrantType: "authorization_code", Code: other.login(t, dynamic, challenge, testResource),
		RedirectURI: testRedirect, ClientID: dynamic, CodeVerifier: verifier, Resource: testResource,
	}); err != nil {
		t.Fatalf("a dynamic client's login without a session: %v", err)
	}
}

func TestBrowserClient_everyApprovalIsRefusedWhileImpersonating(t *testing.T) {
	h, clientID := browserHarness(t)
	impersonating := webPrincipal([]string{"org/repo"})
	impersonating.SessionID = "sid_1"
	impersonating.Impersonating = true

	browser := browserAuthorization(t, h, clientID)
	_, err := h.oauth.ApproveConsent(context.Background(), browser.Handle, impersonating, []string{"org/repo"})
	if got := outcomeOf(err); got != oauthvocab.OutcomeImpersonating {
		t.Fatalf("browser consent while impersonating: outcome %s, want %s", got, oauthvocab.OutcomeImpersonating)
	}
	dynamic := h.register(t)
	_, challenge := pkce(t)
	other, err := h.oauth.Authorize(context.Background(), OAuthAuthorizeRequest{
		ResponseType: "code", ClientID: dynamic, RedirectURI: testRedirect, CodeChallenge: challenge, CodeChallengeMethod: "S256", State: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.oauth.ApproveConsent(context.Background(), other.Handle, impersonating, []string{"org/repo"})
	if got := outcomeOf(err); got != oauthvocab.OutcomeImpersonating {
		t.Fatalf("dynamic client consent while impersonating: outcome %s, want %s", got, oauthvocab.OutcomeImpersonating)
	}
	// The typed-code approval and the consent authority refuse it too.
	start, err := h.devices.Start(context.Background(), DeviceAuthorizationHints{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.devices.Approve(context.Background(), DeviceApprovalRequest{Principal: impersonating, UserCode: start.UserCode, RepositoryScopes: []string{"org/repo"}})
	if !errors.Is(err, ErrImpersonatedApproval) || !errors.Is(err, ErrInvalidDeviceFlow) {
		t.Fatalf("typed-code approval while impersonating: %v, want ErrImpersonatedApproval", err)
	}
	record, err := h.store.GetByDeviceCodeHash(context.Background(), storage.HashDeviceCode(start.DeviceCode))
	if err != nil || record.State != storage.DeviceAuthorizationStatePending {
		t.Fatalf("record after the refused approval = %v, %v; want pending", record.State, err)
	}
}

func TestBrowserClient_redeemWithoutAnApprovalSessionIsRefused(t *testing.T) {
	h, _ := browserHarness(t)
	authorization, err := h.devices.StartForOAuth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.approveDevice(t, authorization.DeviceCodeHash, []string{"org/repo"}) // no sid

	_, err = h.devices.RedeemBrowserForResource(context.Background(), authorization.DeviceCodeHash, DevBrowserClientID, testIssuer, oauthDefaultApprovalScopes)

	if !errors.Is(err, ErrBrowserSessionRequired) {
		t.Fatalf("browser redeem of a sessionless approval: %v, want ErrBrowserSessionRequired", err)
	}
	record, err := h.store.GetByDeviceCodeHash(context.Background(), authorization.DeviceCodeHash)
	if err != nil || record.State != storage.DeviceAuthorizationStateApproved {
		t.Fatalf("record = %v, %v; want approved", record.State, err)
	}
}

func TestBrowserClient_deviceGrantIsRefused(t *testing.T) {
	h, clientID := browserHarness(t)

	_, err := h.oauth.StartDeviceAuthorization(context.Background(), OAuthDeviceAuthorizationRequest{ClientID: clientID})

	if got := outcomeOf(err); got != oauthvocab.OutcomeUnauthorizedClient {
		t.Fatalf("browser client device grant: outcome %s, want %s", got, oauthvocab.OutcomeUnauthorizedClient)
	}
}

func TestBrowserClient_rotationIsRefused(t *testing.T) {
	h, clientID := browserHarness(t)
	token := browserLogin(t, h, clientID)
	service, err := NewService(h.creds, ServiceOptions{Now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatal(err)
	}
	principal := storage.Principal{
		AuthenticationMethod: storage.AuthenticationMethodCredential, Subject: token.Issued.Credential.CredentialID,
		CredentialID: token.Issued.Credential.CredentialID, OrgID: token.Issued.Credential.OrgID,
		RepositoryScopes: token.Issued.Credential.RepositoryScopes, Permissions: token.Issued.Credential.Scopes,
	}

	_, selfErr := service.RotateSelf(context.Background(), principal)
	_, operatorErr := service.Rotate(context.Background(), RotateCredentialRequest{
		OrgID: token.Issued.Credential.OrgID, CredentialID: token.Issued.Credential.CredentialID, CreatedBy: "operator",
	})

	for name, err := range map[string]error{"self-rotate": selfErr, "operator rotate": operatorErr} {
		if !errors.Is(err, ErrBrowserCredentialRotation) || !errors.Is(err, ErrInvalidCredential) {
			t.Errorf("%s of a browser credential: %v, want ErrBrowserCredentialRotation", name, err)
		}
	}
	credentials, err := h.creds.List(context.Background(), token.Issued.Credential.OrgID)
	if err != nil || len(credentials) != 1 {
		t.Fatalf("credentials after the refused rotations = %d, %v; want the one browser credential", len(credentials), err)
	}
}

func TestBrowserClient_configurationIsChecked(t *testing.T) {
	devices := newOAuthHarness(t).devices
	store := newOAuthHarness(t).oauth.store
	meta := &fakeMetadata{documents: map[string]OAuthClientMetadata{}}
	const browserClient = "https://www.example.test/acr/client-metadata.json"
	for name, tc := range map[string]struct {
		cfg    OAuthConfig
		wantOK bool
	}{
		"metadata browser client":    {OAuthConfig{Resources: []string{testResource, testIssuer}, ClientMetadata: meta, BrowserClients: []string{browserClient}}, true},
		"dev browser client":         {OAuthConfig{Resources: []string{testResource, testIssuer}, DevBrowserRedirectURIs: []string{"http://localhost:3000/acr/callback"}}, true},
		"no metadata documents":      {OAuthConfig{Resources: []string{testResource, testIssuer}, BrowserClients: []string{browserClient}}, false},
		"issuer not a resource":      {OAuthConfig{Resources: []string{testResource}, ClientMetadata: meta, BrowserClients: []string{browserClient}}, false},
		"an MCP alias of the issuer": {OAuthConfig{Resources: []string{testIssuer + "/mcp", testIssuer}, ClientMetadata: meta, BrowserClients: []string{browserClient}}, false},
		"http browser client":        {OAuthConfig{Resources: []string{testResource, testIssuer}, ClientMetadata: meta, BrowserClients: []string{"http://www.example.test/acr/client.json"}}, false},
		"repeated browser client":    {OAuthConfig{Resources: []string{testResource, testIssuer}, ClientMetadata: meta, BrowserClients: []string{browserClient, browserClient}}, false},
		"the dev client id listed":   {OAuthConfig{Resources: []string{testResource, testIssuer}, ClientMetadata: meta, BrowserClients: []string{DevBrowserClientID}}, false},
		"dev redirect on https":      {OAuthConfig{Resources: []string{testResource, testIssuer}, DevBrowserRedirectURIs: []string{"https://localhost:3000/acr/callback"}}, false},
		"dev redirect off loopback":  {OAuthConfig{Resources: []string{testResource, testIssuer}, DevBrowserRedirectURIs: []string{"http://web.example.test:3000/acr/callback"}}, false},
		"dev redirect without port":  {OAuthConfig{Resources: []string{testResource, testIssuer}, DevBrowserRedirectURIs: []string{"http://localhost/acr/callback"}}, false},
		"dev redirect without path":  {OAuthConfig{Resources: []string{testResource, testIssuer}, DevBrowserRedirectURIs: []string{"http://localhost:3000"}}, false},
	} {
		tc.cfg.Issuer = testIssuer
		_, err := NewOAuthService(store, devices, tc.cfg)
		if tc.wantOK != (err == nil) {
			t.Errorf("%s: NewOAuthService error = %v, want ok=%v", name, err, tc.wantOK)
		}
	}
}

func TestBrowserClient_metadataDocumentClientIsABrowserClient(t *testing.T) {
	const browserClient = "https://www.example.test/acr/client-metadata.json"
	h := newOAuthHarnessWith(t, func(cfg *OAuthConfig) {
		cfg.Resources = []string{testResource, testIssuer}
		cfg.BrowserClients = []string{browserClient}
	})
	h.sessionID = "sid_2"
	h.meta.documents[browserClient] = OAuthClientMetadata{ClientID: browserClient, ClientName: "Dev Health", RedirectURIs: []string{testRedirect}, TokenEndpointAuthMethod: "none"}

	token := browserLogin(t, h, browserClient)

	if got := token.Issued.Credential.Provenance; got.ClientClass != string(storage.CredentialClientClassBrowser) || got.ClientID != browserClient || got.SessionID != "sid_2" {
		t.Fatalf("provenance = %+v, want browser / %s / sid_2", got, browserClient)
	}
}

func TestWebAssertionVerifier_readsTheSessionAndImpersonation(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewWebAssertionVerifier(WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeTestJWKS(t, "current", public), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{}`)
	base := httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/context-packets", bytes.NewReader(body))
	verify := func(mutate func(map[string]any)) (storage.Principal, error) {
		claims := cloneWebClaims(t, webAssertionClaims(now, base, body))
		jti := make([]byte, 8)
		if _, err := rand.Read(jti); err != nil {
			t.Fatal(err)
		}
		claims["jti"] = fmt.Sprintf("jti-%x", jti)
		mutate(claims)
		request := base.Clone(base.Context())
		request.Body = ioNopCloser{bytes.NewReader(body)}
		request.Header.Set(WebAssertionHeader, signTestWebAssertion(t, private, "current", claims))
		return verifier.Verify(request)
	}

	principal, err := verify(func(claims map[string]any) { claims["sid"] = "sess_123"; claims["impersonating"] = true })
	if err != nil || principal.SessionID != "sess_123" || !principal.Impersonating {
		t.Fatalf("principal = %+v, %v; want sid sess_123 impersonating", principal, err)
	}
	principal, err = verify(func(map[string]any) {})
	if err != nil || principal.SessionID != "" || principal.Impersonating {
		t.Fatalf("principal without the claims = %+v, %v; want none", principal, err)
	}
	if _, err := verify(func(claims map[string]any) { claims["sid"] = strings.Repeat("s", 257) }); err == nil {
		t.Fatal("a 257-byte sid was accepted")
	}
	if _, err := verify(func(claims map[string]any) { claims["sid"] = "   " }); err == nil {
		t.Fatal("a blank sid was accepted")
	}
}

// A code started under one browser configuration is not exchanged under
// another: the class and the acr-api resource go together (CHAOS-12123).
func TestBrowserClient_exchangeUnderAChangedConfigurationIsRefused(t *testing.T) {
	const browserClient = "https://www.example.test/acr/client-metadata.json"
	document := OAuthClientMetadata{ClientID: browserClient, RedirectURIs: []string{testRedirect}, TokenEndpointAuthMethod: "none"}
	exchange := func(service *OAuthService, code, verifier string) error {
		_, err := service.Exchange(context.Background(), OAuthTokenRequest{
			GrantType: "authorization_code", Code: code, RedirectURI: testRedirect, ClientID: browserClient, CodeVerifier: verifier,
		})
		return err
	}

	// Started as the browser client, exchanged after the client was removed.
	h := newOAuthHarnessWith(t, func(cfg *OAuthConfig) {
		cfg.Resources = []string{testResource, testIssuer}
		cfg.BrowserClients = []string{browserClient}
	})
	h.sessionID = "sid_1"
	h.meta.documents[browserClient] = document
	removed, err := NewOAuthService(h.oauth.store, h.devices, OAuthConfig{Issuer: testIssuer, Resources: []string{testResource, testIssuer}, ClientMetadata: h.meta, Now: h.oauth.now})
	if err != nil {
		t.Fatal(err)
	}
	verifier, challenge := pkce(t)
	if got := outcomeOf(exchange(removed, h.login(t, browserClient, challenge, ""), verifier)); got != oauthvocab.OutcomeResourceMismatch {
		t.Fatalf("browser code exchanged without the browser client: outcome %s, want %s", got, oauthvocab.OutcomeResourceMismatch)
	}

	// Started as an ordinary client on the MCP resource, exchanged after the
	// client was made the browser client.
	plain := newOAuthHarnessWith(t, func(cfg *OAuthConfig) { cfg.Resources = []string{testResource, testIssuer} })
	plain.meta.documents[browserClient] = document
	added, err := NewOAuthService(plain.oauth.store, plain.devices, OAuthConfig{Issuer: testIssuer, Resources: []string{testResource, testIssuer}, ClientMetadata: plain.meta, Now: plain.oauth.now, BrowserClients: []string{browserClient}})
	if err != nil {
		t.Fatal(err)
	}
	verifier, challenge = pkce(t)
	if got := outcomeOf(exchange(added, plain.login(t, browserClient, challenge, ""), verifier)); got != oauthvocab.OutcomeResourceMismatch {
		t.Fatalf("MCP code exchanged as the browser client: outcome %s, want %s", got, oauthvocab.OutcomeResourceMismatch)
	}
}

// No other client binds the acr-api resource, even with no browser client
// configured.
func TestBrowserClient_theIssuerResourceIsReservedWithTheClassOff(t *testing.T) {
	h := newOAuthHarnessWith(t, func(cfg *OAuthConfig) { cfg.Resources = []string{testResource, testIssuer} })
	dynamic := h.register(t)
	_, challenge := pkce(t)
	for resource, want := range map[string]string{testIssuer: oauthvocab.OutcomeInvalidTarget, "": "nil", testResource: "nil"} {
		_, err := h.oauth.Authorize(context.Background(), OAuthAuthorizeRequest{
			ResponseType: "code", ClientID: dynamic, RedirectURI: testRedirect, CodeChallenge: challenge,
			CodeChallengeMethod: "S256", Resource: resource, State: "s1",
		})
		if got := outcomeOf(err); got != want {
			t.Errorf("resource %q: outcome %s, want %s", resource, got, want)
		}
	}
	// Nor through a same-host alias of an MCP resource on the issuer's host.
	alias := newOAuthHarnessWith(t, func(cfg *OAuthConfig) { cfg.Resources = []string{testIssuer + "/mcp"} })
	aliasClient := alias.register(t)
	_, err := alias.oauth.Authorize(context.Background(), OAuthAuthorizeRequest{
		ResponseType: "code", ClientID: aliasClient, RedirectURI: testRedirect, CodeChallenge: challenge,
		CodeChallengeMethod: "S256", Resource: testIssuer, State: "s1",
	})
	if got := outcomeOf(err); got != oauthvocab.OutcomeInvalidTarget {
		t.Errorf("the issuer as an alias of %s/mcp: outcome %s, want %s", testIssuer, got, oauthvocab.OutcomeInvalidTarget)
	}
}

// A device grant started for a client that later became the browser client
// is not exchanged (CHAOS-12123).
func TestBrowserClient_aDeviceGrantOfANowBrowserClientIsRefused(t *testing.T) {
	const browserClient = "https://www.example.test/acr/client-metadata.json"
	plain := newOAuthHarnessWith(t, func(cfg *OAuthConfig) { cfg.Resources = []string{testResource, testIssuer} })
	plain.meta.documents[browserClient] = OAuthClientMetadata{ClientID: browserClient, RedirectURIs: []string{testRedirect}, TokenEndpointAuthMethod: "none"}
	start, err := plain.oauth.StartDeviceAuthorization(context.Background(), OAuthDeviceAuthorizationRequest{ClientID: browserClient})
	if err != nil {
		t.Fatal(err)
	}
	added, err := NewOAuthService(plain.oauth.store, plain.devices, OAuthConfig{Issuer: testIssuer, Resources: []string{testResource, testIssuer}, ClientMetadata: plain.meta, Now: plain.oauth.now, BrowserClients: []string{browserClient}})
	if err != nil {
		t.Fatal(err)
	}

	_, err = added.ExchangeDeviceCode(context.Background(), OAuthDeviceTokenRequest{GrantType: OAuthDeviceCodeGrantType, DeviceCode: start.DeviceCode, ClientID: browserClient})

	if got := outcomeOf(err); got != oauthvocab.OutcomeUnauthorizedClient {
		t.Fatalf("device grant exchanged as the browser client: outcome %s, want %s", got, oauthvocab.OutcomeUnauthorizedClient)
	}
}

// A consent resumed after its approval was recorded (the code was never
// attached) is still refused while impersonating (CHAOS-12123).
func TestBrowserClient_aResumedConsentIsRefusedWhileImpersonating(t *testing.T) {
	h, clientID := browserHarness(t)
	authorization := browserAuthorization(t, h, clientID)
	request, err := h.oauth.store.GetAuthorizationRequest(context.Background(), storage.HashOAuthSecret(authorization.Handle))
	if err != nil {
		t.Fatal(err)
	}
	approver := webPrincipal([]string{"org/repo"})
	approver.SessionID = "sid_1"
	if err := h.devices.ApproveForOAuth(context.Background(), approver, request.DeviceCodeHash, []string{"org/repo"}, oauthDefaultApprovalScopes); err != nil {
		t.Fatal(err)
	}
	impersonating := approver
	impersonating.Impersonating = true

	_, err = h.oauth.ApproveConsent(context.Background(), authorization.Handle, impersonating, []string{"org/repo"})

	if got := outcomeOf(err); got != oauthvocab.OutcomeImpersonating {
		t.Fatalf("resumed consent while impersonating: outcome %s, want %s", got, oauthvocab.OutcomeImpersonating)
	}
}

// A browser client's loopback redirect registered without a port is matched
// exactly too: no presented port is admitted (CHAOS-12123).
func TestBrowserClient_aPortlessLoopbackRedirectAdmitsNoPort(t *testing.T) {
	const browserClient = "https://www.example.test/acr/client-metadata.json"
	h := newOAuthHarnessWith(t, func(cfg *OAuthConfig) {
		cfg.Resources = []string{testResource, testIssuer}
		cfg.BrowserClients = []string{browserClient}
	})
	h.meta.documents[browserClient] = OAuthClientMetadata{ClientID: browserClient, RedirectURIs: []string{"http://127.0.0.1/callback"}, TokenEndpointAuthMethod: "none"}
	_, challenge := pkce(t)
	for presented, want := range map[string]string{"http://127.0.0.1:4711/callback": oauthvocab.OutcomeInvalidRedirectURI, "http://127.0.0.1/callback": "nil"} {
		_, err := h.oauth.Authorize(context.Background(), OAuthAuthorizeRequest{
			ResponseType: "code", ClientID: browserClient, RedirectURI: presented, CodeChallenge: challenge, CodeChallengeMethod: "S256", State: "s1",
		})
		if got := outcomeOf(err); got != want {
			t.Errorf("presented %s: outcome %s, want %s", presented, got, want)
		}
	}
}
