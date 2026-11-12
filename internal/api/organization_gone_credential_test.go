package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// A credential whose organization Dev Health no longer has (the coded 404)
// is refused at authentication; an outage never reads as a deletion.

type orgOpsMode int32

const (
	orgOpsLive orgOpsMode = iota
	orgOpsGone
	orgOpsServerError
	orgOpsUncodedNotFound
	orgOpsTimeout
	orgOpsDisabled
)

var orgOpsOutages = map[string]orgOpsMode{
	"5xx": orgOpsServerError, "uncoded 404": orgOpsUncodedNotFound, "timeout": orgOpsTimeout,
}

// fakeOrgOps stands in for Dev Health's entitlement route; the real
// entitlements client reads it.
type fakeOrgOps struct {
	mode   atomic.Int32
	hits   atomic.Int32
	client *entitlements.Client
	// elapsed moves the entitlement cache clock.
	elapsed atomic.Int64
}

func newFakeOrgOps(t *testing.T, mode orgOpsMode) *fakeOrgOps {
	t.Helper()
	ops := &fakeOrgOps{}
	ops.mode.Store(int32(mode))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		org, ok := strings.CutPrefix(r.URL.Path, "/api/v1/internal/acr/entitlements/")
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		ops.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch orgOpsMode(ops.mode.Load()) {
		case orgOpsLive:
			_, _ = w.Write([]byte(`{"schema_version":"acr_entitlement.v1","org_id":"` + org + `","agent_context_runtime":true}`))
		case orgOpsGone:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not found","code":"acr_organization_unknown"}`))
		case orgOpsDisabled:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not found","code":"acr_organization_disabled"}`))
		case orgOpsServerError:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"detail":"unavailable"}`))
		case orgOpsUncodedNotFound:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not found"}`))
		case orgOpsTimeout:
			// Abort, never return: a return sends an implicit 200 that a client
			// closing its TLS connection can still read.
			<-r.Context().Done()
			panic(http.ErrAbortHandler)
		}
	}))
	t.Cleanup(server.Close)
	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("svc_acr_standin_token_0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops.client, err = entitlements.New(entitlements.Config{
		BaseURL: baseURL, TokenFile: tokenFile, Timeout: time.Second, MaxResponseBytes: 16 << 10,
		PositiveCacheTTL: 30 * time.Second, NegativeCacheTTL: time.Second, CacheCapacity: 4,
		Now: func() time.Time { return orgOpsEpoch.Add(time.Duration(ops.elapsed.Load())) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ops.client.Close() })
	return ops
}

func (o *fakeOrgOps) set(mode orgOpsMode) { o.mode.Store(int32(mode)) }

func (o *fakeOrgOps) advance(d time.Duration) { o.elapsed.Add(int64(d)) }

var orgOpsEpoch = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// orgGoneTestOrg is the harness organization: a canonical UUID, as Dev Health's ids are.
const orgGoneTestOrg = "9b2f3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"

// orgGoneApp is an app whose entitlements are the real client over
// fakeOrgOps, holding one native-device (user-class) credential issued by the
// device flow.
type orgGoneApp struct {
	app          *App
	ops          *fakeOrgOps
	access       *fakePrincipalAccess
	credentials  *storage.CredentialLifecycle
	rotations    atomic.Int32
	token        string
	credentialID string
	orgID        string
	now          time.Time
	private      ed25519.PrivateKey
}

func newOrgGoneApp(t *testing.T, mode orgOpsMode) *orgGoneApp {
	t.Helper()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	h := &orgGoneApp{ops: newFakeOrgOps(t, orgOpsLive), access: &fakePrincipalAccess{}}
	h.access.set(grantedAccess(), nil)
	audit := memory.NewAuditStore()
	credentials, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: audit, Now: clock, Observe: func(op string) {
		if op == "rotateCredential" {
			h.rotations.Add(1)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	h.credentials = credentials
	devices, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentials, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := limits.NewManager(limits.Options{Now: clock, PerOrgConcurrency: 4, Policies: limits.PolicySet{
		Auth:     limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 100},
		Context:  limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 100, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}},
		Evidence: limits.EvidencePolicy{Window: time.Minute, PerOrgLimit: 100, Resources: limits.ResourceBudget{MaxItems: 1, MaxTokens: 16_000, MaxBytes: 1 << 20}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	h.app, err = NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: 5 * time.Second}, Dependencies{
		Capabilities: StaticCapabilitiesProvider{Now: clock, Value: hostedCapabilities()}, Limits: manager, Now: clock, WebAssertions: verifier,
		Runtime: &RuntimeDependencies{
			Credentials: credentials, Audit: audit, Entitlements: h.ops.client, PrincipalAccess: h.access,
			Assembler: noopAssembler{}, Evidence: noopEvidenceStore{},
			DeviceAuthorizations: devices, DeviceVerificationURL: "https://verify.example.test/device",
			DeviceAuthorizationLimiter: NewDeviceAuthorizationLimiter(ClockFunc(clock)),
			ReadinessChecks:            exactRuntimeChecks(), DataStoreChecks: exactDataStoreChecks(),
		},
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	deviceApprovalExtraClaims = map[string]any{"org_id": orgGoneTestOrg}
	t.Cleanup(func() { deviceApprovalExtraClaims = nil })
	h.now, h.private = now, private
	h.token, h.credentialID, h.orgID = h.issueDeviceCredential(t, now, private)
	// The issuance read the organization fresh (approval, redemption); the cells count only their own reads.
	h.ops.hits.Store(0)
	h.ops.set(mode)
	return h
}

func (h *orgGoneApp) issueDeviceCredential(t *testing.T, now time.Time, private ed25519.PrivateKey) (string, string, string) {
	t.Helper()
	created := h.serve(deviceRequest(t, http.MethodPost, "/api/v1/oauth/device_authorization", contractsv1.DeviceAuthorizationRequest{SchemaVersion: contractsv1.DeviceAuthorizationRequestSchema}))
	if created.Code != http.StatusOK {
		t.Fatalf("device authorization: %d %s", created.Code, created.Body.String())
	}
	var authorization contractsv1.DeviceAuthorizationResponse
	if err := json.NewDecoder(created.Body).Decode(&authorization); err != nil {
		t.Fatal(err)
	}
	approval := contractsv1.DeviceApprovalRequest{SchemaVersion: contractsv1.DeviceApprovalRequestSchema, UserCode: authorization.UserCode, RepositoryScopes: []string{"*"}}
	if approved := h.serve(deviceApprovalRequest(t, now, private, approval, "approval_org_gone")); approved.Code != http.StatusOK {
		t.Fatalf("device approval: %d %s", approved.Code, approved.Body.String())
	}
	redeemed := h.serve(deviceTokenRequest(t, authorization.DeviceCode))
	if redeemed.Code != http.StatusOK {
		t.Fatalf("device redeem: %d %s", redeemed.Code, redeemed.Body.String())
	}
	var issued contractsv1.DeviceTokenResponse
	if err := json.NewDecoder(redeemed.Body).Decode(&issued); err != nil {
		t.Fatal(err)
	}
	stored, err := h.credentials.FindByTokenHash(context.Background(), auth.HashToken(issued.AccessToken))
	if err != nil || stored.Provenance.ClientClass != string(storage.CredentialClientClassNativeDevice) || stored.Provenance.ApprovingUserID == "" {
		t.Fatalf("issued credential provenance = %+v (%v), want a native_device user credential", stored.Provenance, err)
	}
	return issued.AccessToken, stored.CredentialID, stored.OrgID
}

func (h *orgGoneApp) serve(request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(recorder, request)
	return recorder
}

func (h *orgGoneApp) capabilities(token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	return h.serve(request)
}

func (h *orgGoneApp) rotate(t *testing.T, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := deviceRequest(t, http.MethodPost, "/api/v1/auth/credentials/self/rotate", contractsv1.CredentialRotateRequest{SchemaVersion: contractsv1.CredentialRotateRequestSchema})
	request.Header.Set("Authorization", "Bearer "+token)
	return h.serve(request)
}

func (h *orgGoneApp) ack(t *testing.T, token, credentialID string) *httptest.ResponseRecorder {
	t.Helper()
	return h.serve(ackRequest(t, token, credentialID))
}

func (h *orgGoneApp) revoke(t *testing.T, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := deviceRequest(t, http.MethodPost, "/api/v1/auth/credentials/self/revoke", contractsv1.CredentialRevokeRequest{SchemaVersion: contractsv1.CredentialRevokeRequestSchema})
	request.Header.Set("Authorization", "Bearer "+token)
	return h.serve(request)
}

func (h *orgGoneApp) storedCredential(t *testing.T) contractsv1.ClientCredential {
	t.Helper()
	credential, err := h.credentials.GetByID(context.Background(), h.orgID, h.credentialID)
	if err != nil {
		t.Fatal(err)
	}
	return credential
}

// assertOrganizationUnknownRefusal checks a 401 whose audit names the gone
// organization.
func (h *orgGoneApp) assertOrganizationUnknownRefusal(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	assertErrorResponse(t, response, http.StatusUnauthorized, "invalid_token")
	for _, event := range h.app.runtime.Audit.(*memory.AuditStore).Events() {
		if event.Action == "credential_auth_denied" && event.ResourceID == h.credentialID && event.Metadata["reason"] == "organization_unknown" {
			return
		}
	}
	t.Fatal("no credential_auth_denied audit with reason organization_unknown")
}

// A: a gone organization's user credential on a data route is 401
// organization_unknown at authentication, before the principal access read.
func TestOrganizationGone_capabilitiesRefusesTheCredential(t *testing.T) {
	h := newOrgGoneApp(t, orgOpsGone)
	h.access.set(entitlements.PrincipalAccess{}, entitlements.ErrPrincipalUnknown)

	response := h.capabilities(h.token)

	h.assertOrganizationUnknownRefusal(t, response)
	if calls := h.access.reset(); len(calls) != 0 {
		t.Fatalf("principal access read %d times, want 0 (refused at authentication)", len(calls))
	}
}

// B: no successor is minted for a gone organization.
func TestOrganizationGone_selfRotateRefusesWithoutARotation(t *testing.T) {
	h := newOrgGoneApp(t, orgOpsGone)

	response := h.rotate(t, h.token)

	h.assertOrganizationUnknownRefusal(t, response)
	if got := h.rotations.Load(); got != 0 {
		t.Fatalf("store Rotate calls = %d, want 0", got)
	}
}

// C: the ack that would keep a successor is refused.
func TestOrganizationGone_ackRefuses(t *testing.T) {
	h := newOrgGoneApp(t, orgOpsGone)

	response := h.ack(t, h.token, h.credentialID)

	h.assertOrganizationUnknownRefusal(t, response)
}

// D: self revoke stays open: it only removes the orphan.
func TestOrganizationGone_selfRevokeStillRevokes(t *testing.T) {
	h := newOrgGoneApp(t, orgOpsGone)

	response := h.revoke(t, h.token)

	if response.Code != http.StatusOK {
		t.Fatalf("self revoke: %d %s, want 200", response.Code, response.Body.String())
	}
	if h.storedCredential(t).RevokedAt == nil {
		t.Fatal("the credential is not revoked")
	}
}

// E: an outage (5xx, timeout, a 404 without the code) is never a deletion:
// capabilities passes authentication, and rotate and ack fail closed with a
// retryable 503 and no rotation.
func TestOrganizationGone_outageIsNeverGone(t *testing.T) {
	for name, mode := range orgOpsOutages {
		t.Run(name, func(t *testing.T) {
			h := newOrgGoneApp(t, mode)

			capabilities := h.capabilities(h.token)
			if capabilities.Code == http.StatusUnauthorized {
				t.Fatalf("capabilities in an outage: 401 %s", capabilities.Body.String())
			}
			if calls := h.access.reset(); len(calls) != 1 {
				t.Fatalf("capabilities in an outage: principal access read %d times, want 1 (past authentication); %d %s", len(calls), capabilities.Code, capabilities.Body.String())
			}
			for step, response := range map[string]*httptest.ResponseRecorder{
				"rotate": h.rotate(t, h.token),
				"ack":    h.ack(t, h.token, h.credentialID),
			} {
				if response.Code != http.StatusServiceUnavailable {
					t.Fatalf("%s in an outage: %d %s, want 503", step, response.Code, response.Body.String())
				}
				assertErrorResponse(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
				var envelope contractsv1.ErrorEnvelope
				if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || !envelope.Error.Retryable {
					t.Fatalf("%s in an outage: retryable = false (%v)", step, err)
				}
			}
			if got := h.rotations.Load(); got != 0 {
				t.Fatalf("store Rotate calls in an outage = %d, want 0", got)
			}
			if h.storedCredential(t).RevokedAt != nil {
				t.Fatal("an outage revoked the credential")
			}
		})
	}
}

// F: a live organization is answered as before on every route.
func TestOrganizationGone_liveOrganizationIsUnchanged(t *testing.T) {
	h := newOrgGoneApp(t, orgOpsLive)

	if response := h.capabilities(h.token); response.Code != http.StatusOK {
		t.Fatalf("capabilities: %d %s", response.Code, response.Body.String())
	}
	if response := h.ack(t, h.token, h.credentialID); response.Code != http.StatusOK {
		t.Fatalf("ack: %d %s", response.Code, response.Body.String())
	}
	rotated := h.rotate(t, h.token)
	if rotated.Code != http.StatusOK || h.rotations.Load() != 1 {
		t.Fatalf("rotate: %d %s (rotations %d)", rotated.Code, rotated.Body.String(), h.rotations.Load())
	}
	var successor contractsv1.CredentialRotateResponse
	if err := json.Unmarshal(rotated.Body.Bytes(), &successor); err != nil {
		t.Fatal(err)
	}
	if response := h.capabilities(successor.AccessToken); response.Code != http.StatusOK {
		t.Fatalf("capabilities with the successor: %d %s", response.Code, response.Body.String())
	}
	if response := h.revoke(t, successor.AccessToken); response.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", response.Code, response.Body.String())
	}
}

// G: the organization check passed from its cache, then the principal access
// read sees the coded 404: still the 403 of the access read.
func TestOrganizationGone_cachedOrganizationThenUnknownPrincipalIs403(t *testing.T) {
	h := newOrgGoneApp(t, orgOpsLive)
	if response := h.capabilities(h.token); response.Code != http.StatusOK {
		t.Fatalf("warm capabilities: %d %s", response.Code, response.Body.String())
	}
	h.ops.set(orgOpsGone)
	h.access.set(entitlements.PrincipalAccess{}, entitlements.ErrPrincipalUnknown)

	response := h.capabilities(h.token)

	assertErrorResponse(t, response, http.StatusForbidden, contractsv1.ErrorCodeAccessDenied)
}

// countingRefreshStore counts the rotations the refresh grant asks for.
type countingRefreshStore struct {
	storage.BrowserRefreshStore
	rotations atomic.Int32
	mints     atomic.Int32
}

func (s *countingRefreshStore) RotateBrowserRefresh(ctx context.Context, rotation storage.BrowserRefreshRotation) (storage.BrowserRefreshOutcome, error) {
	s.rotations.Add(1)
	outcome, err := s.BrowserRefreshStore.RotateBrowserRefresh(ctx, rotation)
	if err == nil && (outcome.Decision == storage.BrowserRefreshRotated || outcome.Decision == storage.BrowserRefreshGraceReplay) {
		s.mints.Add(1)
	}
	return outcome, err
}

func (s *countingRefreshStore) minted() int32 { return s.mints.Load() }

type orgGoneBrowser struct {
	app     *App
	logs    *bytes.Buffer
	ops     *fakeOrgOps
	refresh *countingRefreshStore
	login   oauthTokenAnswer
}

func newOrgGoneBrowser(t *testing.T) *orgGoneBrowser {
	t.Helper()
	b := &orgGoneBrowser{ops: newFakeOrgOps(t, orgOpsLive), refresh: &countingRefreshStore{}}
	app, logs, err := newOAuthTestAppWith(t, &OAuthRuntime{
		Issuer: oauthTestIssuer, Resources: []string{oauthTestResource, oauthTestIssuer}, DevBrowserRedirectURIs: []string{oauthTestRedirect},
		BrowserClients: []string{orgGoneOtherBrowser}, ClientMetadata: auth.NewClientMetadataFetcher(http.DefaultClient),
	}, true, func(deps *Dependencies) {
		deps.Runtime.Entitlements = b.ops.client
		b.refresh.BrowserRefreshStore = deps.Runtime.OAuth.BrowserRefresh
		deps.Runtime.OAuth.BrowserRefresh = b.refresh
	})
	if err != nil {
		t.Fatal(err)
	}
	b.app, b.logs = app, logs
	t.Cleanup(func() { oauthConsentExtraClaims = nil })
	all := []string{"*"}
	oauthConsentExtraClaims = map[string]any{"sid": "sess_1", "org_id": orgGoneTestOrg}
	_, body := serveConsent(app, consentRequest(t, map[string]any{"action": "approve", "handle": authorizeToConsent(t, app, oauthTestAuthorizeQuery(auth.DevBrowserClientID)), "repository_scopes": all}, all, "r1"))
	redirect, _ := url.Parse(body["redirect_url"].(string))
	status, login, raw := postOAuthForm(t, app, OAuthTokenPath, url.Values{"grant_type": {"authorization_code"}, "code": {redirect.Query().Get("code")},
		"redirect_uri": {oauthTestRedirect}, "client_id": {auth.DevBrowserClientID}, "code_verifier": {strings.Repeat("v", 50)}})
	if status != http.StatusOK || !auth.ValidRefreshTokenShape(login.RefreshToken) {
		t.Fatalf("login: %d %s", status, raw)
	}
	b.login = login
	// The login read the organization fresh (consent, redemption); the cells count only their own reads.
	b.ops.hits.Store(0)
	return b
}

const orgGoneOtherBrowser = "https://www.example.test/acr/client-metadata.json"

func (b *orgGoneBrowser) refreshWith(t *testing.T, refreshToken string) (int, oauthTokenAnswer, string) {
	t.Helper()
	return b.refreshAs(t, refreshToken, auth.DevBrowserClientID)
}

func (b *orgGoneBrowser) refreshAs(t *testing.T, refreshToken, clientID string) (int, oauthTokenAnswer, string) {
	t.Helper()
	return postOAuthForm(t, b.app, OAuthTokenPath, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}, "client_id": {clientID}})
}

func (b *orgGoneBrowser) read(accessToken string) int {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set(auth.ResourceHeader, oauthTestIssuer)
	request.Header.Set("X-ACR-Client-Version", "0.1.0")
	recorder := httptest.NewRecorder()
	b.app.Handler().ServeHTTP(recorder, request)
	return recorder.Code
}

// I: a gone organization's browser family cannot refresh: invalid_grant
// organization_unknown, no rotation, and the family is revoked.
func TestOrganizationGone_browserRefreshRevokesTheFamily(t *testing.T) {
	b := newOrgGoneBrowser(t)
	b.ops.set(orgOpsGone)

	status, answer, raw := b.refreshWith(t, b.login.RefreshToken)

	if status != http.StatusBadRequest || answer.Error != "invalid_grant" {
		t.Fatalf("refresh of a gone organization: %d %s, want 400 invalid_grant", status, raw)
	}
	if !strings.Contains(b.logs.String(), `"step":"token","outcome":"organization_unknown"`) {
		t.Fatalf("no token step line with outcome organization_unknown:\n%s", b.logs.String())
	}
	if got := b.refresh.rotations.Load(); got != 0 {
		t.Fatalf("RotateBrowserRefresh calls = %d, want 0", got)
	}
	b.ops.set(orgOpsLive)
	if code := b.read(b.login.AccessToken); code != http.StatusUnauthorized {
		t.Fatalf("the family's access token after the refusal: %d, want 401 (family revoked)", code)
	}
	if status, answer, raw := b.refreshWith(t, b.login.RefreshToken); status != http.StatusBadRequest || answer.Error != "invalid_grant" ||
		!strings.Contains(b.logs.String(), `"step":"token","outcome":"family_revoked"`) {
		t.Fatalf("refresh once the organization answers again: %d %s, want invalid_grant family_revoked", status, raw)
	}
}

// J: an outage fails the refresh closed with a 503 and no rotation, and the
// same refresh token works once the outage ends.
func TestOrganizationGone_browserRefreshOutageKeepsTheToken(t *testing.T) {
	for name, mode := range orgOpsOutages {
		t.Run(name, func(t *testing.T) {
			b := newOrgGoneBrowser(t)
			b.ops.set(mode)

			status, _, raw := b.refreshWith(t, b.login.RefreshToken)

			if status != http.StatusServiceUnavailable {
				t.Fatalf("refresh in an outage: %d %s, want 503", status, raw)
			}
			if got := b.refresh.rotations.Load(); got != 0 {
				t.Fatalf("RotateBrowserRefresh calls in an outage = %d, want 0", got)
			}
			b.ops.set(orgOpsLive)
			status, rotated, raw := b.refreshWith(t, b.login.RefreshToken)
			if status != http.StatusOK || !auth.ValidRefreshTokenShape(rotated.RefreshToken) || rotated.RefreshToken == b.login.RefreshToken {
				t.Fatalf("refresh after the outage: %d %s, want a rotation", status, raw)
			}
			if code := b.read(rotated.AccessToken); code != http.StatusOK {
				t.Fatalf("the rotated access token: %d, want 200", code)
			}
		})
	}
}

// K: with the organization gone, an unknown refresh token and another browser
// client's attempt keep their own answers: no organization_unknown, nothing
// minted, and the family is not revoked.
func TestOrganizationGone_browserRefreshUnknownOrForeignTokenKeepsItsAnswer(t *testing.T) {
	for name, tc := range map[string]struct {
		token           func(*orgGoneBrowser) string
		client, outcome string
	}{
		"unknown token":          {func(*orgGoneBrowser) string { return auth.RefreshTokenPrefix + strings.Repeat("A", 43) }, auth.DevBrowserClientID, "invalid_grant"},
		"another browser client": {func(b *orgGoneBrowser) string { return b.login.RefreshToken }, orgGoneOtherBrowser, "client_mismatch"},
	} {
		t.Run(name, func(t *testing.T) {
			b := newOrgGoneBrowser(t)
			b.ops.set(orgOpsGone)

			status, answer, raw := b.refreshAs(t, tc.token(b), tc.client)

			if status != http.StatusBadRequest || answer.Error != "invalid_grant" {
				t.Fatalf("refresh: %d %s, want 400 invalid_grant", status, raw)
			}
			if !strings.Contains(b.logs.String(), `"step":"token","outcome":"`+tc.outcome+`"`) || strings.Contains(b.logs.String(), `"outcome":"organization_unknown"`) {
				t.Fatalf("want token outcome %s and no organization_unknown:\n%s", tc.outcome, b.logs.String())
			}
			if got := b.refresh.minted(); got != 0 {
				t.Fatalf("successors minted = %d, want 0", got)
			}
			b.ops.set(orgOpsLive)
			if code := b.read(b.login.AccessToken); code != http.StatusOK {
				t.Fatalf("the family's access token afterwards: %d, want 200 (family not revoked)", code)
			}
		})
	}
}

// The mint paths (self rotate, self ack, browser refresh) read the
// organization fresh on every call: a warm, successful call does not let a
// deletion or an outage through the entitlement cache.

func (h *orgGoneApp) rotatedToken(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var rotated contractsv1.CredentialRotateResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &rotated) != nil {
		t.Fatalf("warm rotate: %d %s", response.Code, response.Body.String())
	}
	return rotated.AccessToken
}

// L: warm, then the organization is deleted: the next mint call is refused.
func TestOrganizationGone_mintAfterAWarmCallSeesTheDeletion(t *testing.T) {
	t.Run("rotate", func(t *testing.T) {
		h := newOrgGoneApp(t, orgOpsLive)
		successor := h.rotatedToken(t, h.rotate(t, h.token))
		h.ops.set(orgOpsGone)

		response := h.rotate(t, successor)

		assertErrorResponse(t, response, http.StatusUnauthorized, "invalid_token")
		if got := h.ops.hits.Load(); got != 2 {
			t.Fatalf("endpoint_hits = %d, want 2 (one fresh read per rotate)", got)
		}
		if got := h.rotations.Load(); got != 1 {
			t.Fatalf("store Rotate calls = %d, want 1 (the warm one)", got)
		}
	})
	t.Run("ack", func(t *testing.T) {
		h := newOrgGoneApp(t, orgOpsLive)
		if warm := h.ack(t, h.token, h.credentialID); warm.Code != http.StatusOK {
			t.Fatalf("warm ack: %d %s", warm.Code, warm.Body.String())
		}
		h.ops.set(orgOpsGone)

		response := h.ack(t, h.token, h.credentialID)

		h.assertOrganizationUnknownRefusal(t, response)
		if got := h.ops.hits.Load(); got != 2 {
			t.Fatalf("endpoint_hits = %d, want 2 (one fresh read per ack)", got)
		}
	})
	t.Run("refresh", func(t *testing.T) {
		b := newOrgGoneBrowser(t)
		status, warm, raw := b.refreshWith(t, b.login.RefreshToken)
		if status != http.StatusOK {
			t.Fatalf("warm refresh: %d %s", status, raw)
		}
		b.ops.set(orgOpsGone)

		status, answer, raw := b.refreshWith(t, warm.RefreshToken)

		if status != http.StatusBadRequest || answer.Error != "invalid_grant" || !strings.Contains(b.logs.String(), `"step":"token","outcome":"organization_unknown"`) {
			t.Fatalf("refresh after the deletion: %d %s, want invalid_grant organization_unknown", status, raw)
		}
		if got := b.ops.hits.Load(); got != 2 {
			t.Fatalf("endpoint_hits = %d, want 2 (one fresh read per refresh)", got)
		}
		if got := b.refresh.minted(); got != 1 {
			t.Fatalf("successors minted = %d, want 1 (the warm one)", got)
		}
		b.ops.set(orgOpsLive)
		if code := b.read(warm.AccessToken); code != http.StatusUnauthorized {
			t.Fatalf("the family's access token after the refusal: %d, want 401 (family revoked)", code)
		}
	})
}

// M: warm, then Dev Health cannot answer: the next mint call is a 503 with
// no successor, and the refresh token is not consumed.
func TestOrganizationGone_mintAfterAWarmCallFailsClosedOnAnOutage(t *testing.T) {
	outages := map[string]orgOpsMode{"5xx": orgOpsServerError, "uncoded 404": orgOpsUncodedNotFound}
	for name, mode := range outages {
		t.Run("rotate/"+name, func(t *testing.T) {
			h := newOrgGoneApp(t, orgOpsLive)
			successor := h.rotatedToken(t, h.rotate(t, h.token))
			h.ops.set(mode)

			response := h.rotate(t, successor)

			assertErrorResponse(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
			if got := h.ops.hits.Load(); got != 2 {
				t.Fatalf("endpoint_hits = %d, want 2", got)
			}
			if got := h.rotations.Load(); got != 1 {
				t.Fatalf("store Rotate calls = %d, want 1 (the warm one)", got)
			}
		})
		t.Run("ack/"+name, func(t *testing.T) {
			h := newOrgGoneApp(t, orgOpsLive)
			if warm := h.ack(t, h.token, h.credentialID); warm.Code != http.StatusOK {
				t.Fatalf("warm ack: %d %s", warm.Code, warm.Body.String())
			}
			h.ops.set(mode)

			response := h.ack(t, h.token, h.credentialID)

			assertErrorResponse(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
			if got := h.ops.hits.Load(); got != 2 {
				t.Fatalf("endpoint_hits = %d, want 2", got)
			}
		})
		t.Run("refresh/"+name, func(t *testing.T) {
			b := newOrgGoneBrowser(t)
			status, warm, raw := b.refreshWith(t, b.login.RefreshToken)
			if status != http.StatusOK {
				t.Fatalf("warm refresh: %d %s", status, raw)
			}
			b.ops.set(mode)

			status, _, raw = b.refreshWith(t, warm.RefreshToken)

			if status != http.StatusServiceUnavailable {
				t.Fatalf("refresh in an outage: %d %s, want 503", status, raw)
			}
			if got := b.ops.hits.Load(); got != 2 {
				t.Fatalf("endpoint_hits = %d, want 2", got)
			}
			if got := b.refresh.minted(); got != 1 {
				t.Fatalf("successors minted = %d, want 1 (the warm one)", got)
			}
			b.ops.set(orgOpsLive)
			if status, after, raw := b.refreshWith(t, warm.RefreshToken); status != http.StatusOK || after.RefreshToken == warm.RefreshToken {
				t.Fatalf("the same refresh token after the outage: %d %s, want a rotation", status, raw)
			}
		})
	}
}

// N: bearer authentication on a data route keeps the cached read: inside the
// 30 s cache a deleted organization is still accepted with no Dev Health
// read, and past it the credential is refused.
func TestOrganizationGone_bearerReadIsBoundedByTheCache(t *testing.T) {
	h := newOrgGoneApp(t, orgOpsLive)
	if warm := h.capabilities(h.token); warm.Code != http.StatusOK {
		t.Fatalf("warm capabilities: %d %s", warm.Code, warm.Body.String())
	}
	h.ops.set(orgOpsGone)
	h.ops.advance(29 * time.Second)

	if inside := h.capabilities(h.token); inside.Code != http.StatusOK {
		t.Fatalf("capabilities inside the cache: %d %s, want 200", inside.Code, inside.Body.String())
	}
	if got := h.ops.hits.Load(); got != 1 {
		t.Fatalf("endpoint_hits inside the cache = %d, want 1", got)
	}
	h.ops.advance(2 * time.Second)

	h.assertOrganizationUnknownRefusal(t, h.capabilities(h.token))
	if got := h.ops.hits.Load(); got != 2 {
		t.Fatalf("endpoint_hits past the cache = %d, want 2", got)
	}
}
