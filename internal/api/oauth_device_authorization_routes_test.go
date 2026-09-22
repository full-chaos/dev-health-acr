package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// oauthDeviceAuthorizationRequest builds a form-encoded POST
// OAuthDeviceAuthorizationPath request (RFC 8628 §3.1).
func oauthDeviceAuthorizationRequest(form url.Values) *http.Request {
	request := httptest.NewRequest(http.MethodPost, OAuthDeviceAuthorizationPath, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

// oauthDeviceTokenRequest builds a form-encoded POST OAuthTokenPath device-code
// poll (RFC 8628 §3.4).
func oauthDeviceTokenRequest(deviceCode, clientID string) *http.Request {
	form := url.Values{"grant_type": {auth.OAuthDeviceCodeGrantType}, "device_code": {deviceCode}}
	if clientID != "" {
		form.Set("client_id", clientID)
	}
	request := httptest.NewRequest(http.MethodPost, OAuthTokenPath, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

// oauthDeviceApprovalRequest signs a web assertion deciding or previewing
// userCode at POST /api/v1/oauth/device_approval, the same route the
// typed-user-code approval page and OAuth authorization-code browser consent
// both use.
func oauthDeviceApprovalRequest[T contractsv1.DeviceApprovalRequest | contractsv1.DeviceApprovalPreviewRequest](t *testing.T, approval T, repositoryScopes []string, jti string) *http.Request {
	t.Helper()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	body, err := json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/device_approval", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	digest := sha256.Sum256(body)
	claims := map[string]any{
		"iss": "https://web.example.test", "aud": "acr-api", "sub": "user_123", "org_id": oauthTestOrg,
		"repository_scopes": repositoryScopes, "permissions": []string{auth.WebAssertionPermissionCredentialIssue},
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(30 * time.Second).Unix(), "jti": jti,
		"method": request.Method, "path": request.URL.EscapedPath(), "body_sha256": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "current"})
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	request.Header.Set(auth.WebAssertionHeader, input+"."+base64.RawURLEncoding.EncodeToString(ed25519.Sign(oauthTestWebKey, []byte(input))))
	return request
}

// startOAuthDeviceAuthorization registers a client and starts a device grant
// for it, returning the decoded response and the client ID.
func startOAuthDeviceAuthorization(t *testing.T, app *App, extra url.Values) (map[string]any, string) {
	t.Helper()
	clientID := registerOAuthTestClient(t, app, "cfa login")
	form := url.Values{"client_id": {clientID}}
	for key, values := range extra {
		form[key] = values
	}
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, oauthDeviceAuthorizationRequest(form))
	if recorder.Code != http.StatusOK {
		t.Fatalf("device_authorization: %d %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body, clientID
}

func TestOAuthDeviceAuthorizationFullFlow(t *testing.T) {
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	started, clientID := startOAuthDeviceAuthorization(t, app, nil)
	for _, key := range []string{"device_code", "user_code", "verification_uri", "verification_uri_complete", "expires_in", "interval"} {
		if started[key] == nil || started[key] == "" {
			t.Fatalf("device_authorization response missing %s: %v", key, started)
		}
	}
	if started["verification_uri"] != "https://web.example.test/acr/device" {
		t.Fatalf("verification_uri = %v, want the configured device verification URL", started["verification_uri"])
	}
	deviceCode := started["device_code"].(string)
	userCode := started["user_code"].(string)
	if want := "https://web.example.test/acr/device?user_code=" + url.QueryEscape(userCode); started["verification_uri_complete"] != want {
		t.Fatalf("verification_uri_complete = %v, want %s", started["verification_uri_complete"], want)
	}

	// A poll before approval is authorization_pending, and a poll immediately
	// after that (same clock tick) is slow_down with Retry-After set -- the
	// device authorization's own poll-interval enforcement, not a new limiter.
	pendingResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(pendingResponse, oauthDeviceTokenRequest(deviceCode, clientID))
	assertOAuthTokenError(t, pendingResponse, "authorization_pending")

	slowDownResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(slowDownResponse, oauthDeviceTokenRequest(deviceCode, clientID))
	assertOAuthTokenError(t, slowDownResponse, "slow_down")
	if slowDownResponse.Header().Get("Retry-After") == "" {
		t.Fatal("slow_down response missing Retry-After")
	}

	approval := contractsv1.DeviceApprovalRequest{SchemaVersion: contractsv1.DeviceApprovalRequestSchema, UserCode: userCode, RepositoryScopes: []string{"*"}}
	approvalResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(approvalResponse, oauthDeviceApprovalRequest(t, approval, []string{"*"}, "approve_1"))
	if approvalResponse.Code != http.StatusOK {
		t.Fatalf("approval: %d %s", approvalResponse.Code, approvalResponse.Body.String())
	}

	// The interval keeps governing polling after approval too: this app's
	// test clock is fixed, so a poll immediately after the one above is
	// still too soon -- the SAME interval gate, not something approval
	// bypasses. Successful redemption once the interval elapses (a clock
	// that advances) is proven at the auth-service unit layer,
	// TestOAuthServiceDeviceGrantFullFlow, which also proves the redeemed
	// record then answers invalid_grant, exactly the shape
	// mapDevicePollOutcome/writeOAuthJSON put on the wire here.
	tooSoonAfterApproval := httptest.NewRecorder()
	app.Handler().ServeHTTP(tooSoonAfterApproval, oauthDeviceTokenRequest(deviceCode, clientID))
	assertOAuthTokenError(t, tooSoonAfterApproval, "slow_down")

	if !strings.Contains(logs.String(), `"step":"device_authorization","outcome":"ok"`) {
		t.Fatal("missing device_authorization ok telemetry line")
	}
}

// TestOAuthDeviceCodeCannotRedeemViaLegacyEndpoint pins the fix for a real
// P1 (found by codex round cf-6233-r1, executed and confirmed): a device
// code started by POST /device_authorization was, before this fix,
// redeemable through the legacy JSON device-flow endpoint
// (POST /api/v1/oauth/token, device_routes.go's handleDeviceCodeToken via
// DeviceFlowService.Poll), which mints a credential with neither the OAuth
// request's resource binding nor its requested-scope subset -- a narrower
// OAuth request could get a BROADER, unbound credential just by hitting the
// wrong endpoint with the same device_code.
func TestOAuthDeviceCodeCannotRedeemViaLegacyEndpoint(t *testing.T) {
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	started, _ := startOAuthDeviceAuthorization(t, app, url.Values{"scope": {"context:read"}})
	deviceCode := started["device_code"].(string)

	legacyResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(legacyResponse, deviceTokenRequest(t, deviceCode))
	if legacyResponse.Code != http.StatusBadRequest {
		t.Fatalf("legacy endpoint status = %d, want 400 (%s)", legacyResponse.Code, legacyResponse.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(legacyResponse.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != string(contractsv1.OAuthDeviceErrorInvalidGrant) {
		t.Fatalf("legacy endpoint error = %v, want %s -- an OAuth device code must never be redeemable through the legacy JSON grant with no resource/scope binding", body["error"], contractsv1.OAuthDeviceErrorInvalidGrant)
	}
	if !strings.Contains(logs.String(), `"step":"token","outcome":"invalid_grant"`) {
		t.Fatal("the legacy-endpoint conflict refusal must still emit the acr-api oauth step token/invalid_grant line -- otherwise the refusal is invisible in the OAuth login's own telemetry")
	}
}

// failingDeviceGrantLookupStore makes GetDeviceGrant fail with a generic
// (non-ErrNotFound) error, for TestOAuthDeviceCodeConflictFailsClosed below.
type failingDeviceGrantLookupStore struct{ storage.OAuthStore }

var errDeviceGrantLookupDown = errors.New("device grant lookup unavailable")

func (s failingDeviceGrantLookupStore) GetDeviceGrant(ctx context.Context, hash storage.DeviceCodeHash) (storage.OAuthDeviceGrant, error) {
	return storage.OAuthDeviceGrant{}, errDeviceGrantLookupDown
}

// TestOAuthDeviceCodeConflictFailsClosed pins the CHAOS-6233 fix for a real
// P1 (found by codex round cf-6233-r2, executed and confirmed): the legacy
// endpoint's OAuth-device-grant conflict check treated ANY lookup error --
// not just "no such grant" -- as "not OAuth-bound," so a storage outage let
// the legacy endpoint mint an unbound credential instead of refusing. The
// check must fail CLOSED (refuse) on an unconfirmed lookup, and open only on
// a definite storage.ErrNotFound.
func TestOAuthDeviceCodeConflictFailsClosed(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{
		Issuer: oauthTestIssuer, Resources: []string{oauthTestResource},
		Store: failingDeviceGrantLookupStore{OAuthStore: memory.NewOAuthStore(clock)},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	started, _ := startOAuthDeviceAuthorization(t, app, nil)
	deviceCode := started["device_code"].(string)

	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, deviceTokenRequest(t, deviceCode))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("legacy endpoint on a lookup failure: status = %d, want 503 (fail closed) -- body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "access_token") {
		t.Fatal("legacy endpoint minted a credential despite an unconfirmed OAuth-device-grant lookup")
	}
	if !strings.Contains(logs.String(), `"step":"token","outcome":"unavailable"`) {
		t.Fatal("the fail-closed refusal must still emit the acr-api oauth step token/unavailable line")
	}
}

// TestOAuthRegisterDeviceCodeOnlyClientHTTP pins the CHAOS-6233 fix for a
// second real P1 (found by codex round cf-6233-r2, executed and confirmed):
// a headless client registering with ONLY the RFC 8628 device-code grant
// type and no redirect_uris (which the device grant never uses) was refused
// invalid_client_metadata -- the registration allowlist accepted only
// authorization_code/refresh_token, so a device-only client had no way to
// self-register as what it actually is.
func TestOAuthRegisterDeviceCodeOnlyClientHTTP(t *testing.T) {
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	registration, _ := json.Marshal(map[string]any{"client_name": "cfa login", "grant_types": []string{auth.OAuthDeviceCodeGrantType}})
	request := httptest.NewRequest(http.MethodPost, OAuthRegisterPath, bytes.NewReader(registration))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("device-only registration: %d %s", recorder.Code, recorder.Body.String())
	}
	var registered map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &registered); err != nil {
		t.Fatal(err)
	}
	if redirects := registered["redirect_uris"]; redirects != nil {
		if list, ok := redirects.([]any); !ok || len(list) != 0 {
			t.Fatalf("device-only client redirect_uris = %v, want none", redirects)
		}
	}
	grantTypes, _ := registered["grant_types"].([]any)
	if len(grantTypes) != 1 || grantTypes[0] != auth.OAuthDeviceCodeGrantType {
		t.Fatalf("device-only client grant_types = %v, want [%s]", grantTypes, auth.OAuthDeviceCodeGrantType)
	}
	responseTypes, _ := registered["response_types"].([]any)
	if len(responseTypes) != 0 {
		t.Fatalf("device-only client response_types = %v, want empty", responseTypes)
	}

	// That client can then start a device authorization.
	clientID, _ := registered["client_id"].(string)
	started, _ := startOAuthDeviceAuthorization(t, app, url.Values{"client_id": {clientID}})
	if started["device_code"] == nil || started["device_code"] == "" {
		t.Fatalf("device-only client could not start a device authorization: %v", started)
	}
}

// TestOAuthDeviceCodeConflictNormalizesBeforeHashing pins the CHAOS-6233 fix
// for a real P1 (found by codex round cf-6233-r3, executed and confirmed):
// oauthDeviceCodeConflict hashed the RAW presented device_code, while
// DeviceFlowService.Poll normalizes (trims whitespace) before hashing --  a
// whitespace-padded device_code missed the OAuth-grant lookup here but still
// resolved at Poll, letting the legacy endpoint redeem an OAuth device code
// after all with no resource/scope binding.
func TestOAuthDeviceCodeConflictNormalizesBeforeHashing(t *testing.T) {
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	started, _ := startOAuthDeviceAuthorization(t, app, nil)
	deviceCode := started["device_code"].(string)

	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, deviceTokenRequest(t, "  "+deviceCode+"  "))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("legacy endpoint with a whitespace-padded OAuth device_code: status = %d, want 400 (%s)", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "access_token") {
		t.Fatal("legacy endpoint minted a credential for a whitespace-padded OAuth device_code")
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != string(contractsv1.OAuthDeviceErrorInvalidGrant) {
		t.Fatalf("legacy endpoint error = %v, want %s", body["error"], contractsv1.OAuthDeviceErrorInvalidGrant)
	}
}

func TestDeviceVerificationURIComplete(t *testing.T) {
	for name, tc := range map[string]struct {
		verificationURI, userCode, want string
	}{
		"plain URL":                {"https://web.example.test/acr/device", "ABCD-EFGH", "https://web.example.test/acr/device?user_code=ABCD-EFGH"},
		"existing query preserved": {"https://web.example.test/acr/device?ref=cli", "ABCD-EFGH", "https://web.example.test/acr/device?ref=cli&user_code=ABCD-EFGH"},
		"unparseable":              {"://not a url", "ABCD-EFGH", ""},
	} {
		t.Run(name, func(t *testing.T) {
			got := deviceVerificationURIComplete(tc.verificationURI, tc.userCode)
			if got != tc.want {
				t.Fatalf("deviceVerificationURIComplete(%q, %q) = %q, want %q", tc.verificationURI, tc.userCode, got, tc.want)
			}
		})
	}
}

// TestOAuthDeviceAuthorizationPreviewShowsHints confirms the device grant's
// device_authorizations row previews exactly like the legacy machine-auth
// flow's -- no OAuth client name, resource or scope (CHAOS-6233's known
// follow-up: the preview does not yet carry OAuth context; see PICKUP.md).
func TestOAuthDeviceAuthorizationPreviewShowsHints(t *testing.T) {
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	started, _ := startOAuthDeviceAuthorization(t, app, nil)
	userCode := started["user_code"].(string)
	previewRequestBody := contractsv1.DeviceApprovalPreviewRequest{SchemaVersion: contractsv1.DeviceApprovalPreviewRequestSchema, UserCode: userCode}
	previewRequest := oauthDeviceApprovalRequest(t, previewRequestBody, []string{"*"}, "preview_1")
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, previewRequest)
	if recorder.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", recorder.Code, recorder.Body.String())
	}
	var preview contractsv1.DeviceApprovalPreviewResponse
	if err := json.NewDecoder(recorder.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	if preview.OrganizationIDHint != "" || len(preview.RepositoryHints) != 0 {
		t.Fatalf("device grant preview hints = %+v, want empty (no org/repo hint was set at StartDeviceAuthorization)", preview)
	}
}

func TestOAuthDeviceAuthorizationRefusals(t *testing.T) {
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	clientID := registerOAuthTestClient(t, app, "cfa login")
	for name, tc := range map[string]struct {
		build     func() *http.Request
		wantCode  int
		wantError string
	}{
		"unknown client": {
			build: func() *http.Request {
				return oauthDeviceAuthorizationRequest(url.Values{"client_id": {"acrc_" + strings.Repeat("0", 32)}})
			},
			wantCode:  http.StatusBadRequest,
			wantError: "invalid_client",
		},
		"invalid scope": {
			build: func() *http.Request {
				return oauthDeviceAuthorizationRequest(url.Values{"client_id": {clientID}, "scope": {"not_a_scope"}})
			},
			wantCode:  http.StatusBadRequest,
			wantError: "invalid_scope",
		},
		"unsupported resource": {
			build: func() *http.Request {
				return oauthDeviceAuthorizationRequest(url.Values{"client_id": {clientID}, "resource": {"https://other.example.test/mcp"}})
			},
			wantCode:  http.StatusBadRequest,
			wantError: "invalid_target",
		},
		"json body": {
			build: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, OAuthDeviceAuthorizationPath, strings.NewReader(`{"client_id":"x"}`))
				r.Header.Set("Content-Type", "application/json")
				return r
			},
			wantCode:  http.StatusBadRequest,
			wantError: "invalid_request",
		},
		"repeated client_id": {
			build: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, OAuthDeviceAuthorizationPath, strings.NewReader("client_id=a&client_id=b"))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return r
			},
			wantCode:  http.StatusBadRequest,
			wantError: "invalid_request",
		},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, tc.build())
			if recorder.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (%s)", recorder.Code, tc.wantCode, recorder.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["error"] != tc.wantError {
				t.Fatalf("error = %v, want %s", body["error"], tc.wantError)
			}
		})
	}
}

func TestOAuthDeviceTokenRefusals(t *testing.T) {
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	started, clientID := startOAuthDeviceAuthorization(t, app, nil)
	deviceCode := started["device_code"].(string)

	unknownCode := httptest.NewRecorder()
	app.Handler().ServeHTTP(unknownCode, oauthDeviceTokenRequest("bogus", clientID))
	assertOAuthTokenError(t, unknownCode, "invalid_grant")

	wrongClient := httptest.NewRecorder()
	app.Handler().ServeHTTP(wrongClient, oauthDeviceTokenRequest(deviceCode, registerOAuthTestClient(t, app, "someone else")))
	assertOAuthTokenError(t, wrongClient, "invalid_grant")

	missingClient := httptest.NewRecorder()
	app.Handler().ServeHTTP(missingClient, oauthDeviceTokenRequest(deviceCode, ""))
	assertOAuthTokenError(t, missingClient, "invalid_grant")
}

func assertOAuthTokenError(t *testing.T, recorder *httptest.ResponseRecorder, wantError string) {
	t.Helper()
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != wantError {
		t.Fatalf("error = %v, want %s", body["error"], wantError)
	}
}
