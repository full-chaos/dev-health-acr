package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-13562: device approval and consent take up to 256 repositories (the
// decision point's allowlist bound), refuse 257 by name, read the list from
// the body the assertion binds, and take an assertion with or without the
// repository_scopes claim; a present claim must equal the body list.

const chaos13562Bound = 256

// chaos13562Slugs returns n distinct repository keys of 255 bytes each, the
// longest a key may be: two 100-byte groups and a 53-byte name.
func chaos13562Slugs(n int) []string {
	group := strings.Repeat("g", 100)
	slugs := make([]string, n)
	for i := range slugs {
		name := fmt.Sprintf("r%04d", i)
		slugs[i] = group + "/" + group + "/" + name + strings.Repeat("x", 53-len(name))
		if len(slugs[i]) != 255 {
			panic(fmt.Sprintf("slug length %d", len(slugs[i])))
		}
	}
	return slugs
}

type chaos13562Assertion struct {
	key        ed25519.PrivateKey
	now        time.Time
	subject    string
	org        string
	scopes     []string
	omitScopes bool
}

func (a chaos13562Assertion) sign(t *testing.T, method, path string, body []byte, jti string) string {
	t.Helper()
	digest := sha256.Sum256(body)
	claims := map[string]any{
		"iss": "https://web.example.test", "aud": "acr-api", "sub": a.subject, "org_id": a.org,
		"permissions": []string{auth.WebAssertionPermissionCredentialIssue},
		"iat":         a.now.Unix(), "nbf": a.now.Unix(), "exp": a.now.Add(30 * time.Second).Unix(), "jti": jti,
		"method": method, "path": path, "body_sha256": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
	if !a.omitScopes {
		claims["repository_scopes"] = a.scopes
	}
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "current"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(a.key, []byte(input)))
}

type chaos13562DeviceHarness struct {
	app *App
	key ed25519.PrivateKey
	now time.Time
}

func newCHAOS13562DeviceHarness(t *testing.T) chaos13562DeviceHarness {
	t.Helper()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	app, _ := newHostedTestAppWithWebAssertions(t, nil, nil, nil, nil, nil, verifier)
	return chaos13562DeviceHarness{app: app, key: private, now: now}
}

func (h chaos13562DeviceHarness) start(t *testing.T) contractsv1.DeviceAuthorizationResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(recorder, deviceRequest(t, http.MethodPost, "/api/v1/oauth/device_authorization", contractsv1.DeviceAuthorizationRequest{SchemaVersion: contractsv1.DeviceAuthorizationRequestSchema}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var authorization contractsv1.DeviceAuthorizationResponse
	if err := json.NewDecoder(recorder.Body).Decode(&authorization); err != nil {
		t.Fatal(err)
	}
	return authorization
}

// approve sends a device approval whose body names bodyScopes, signed over
// signedBody (the body itself unless a cell changes it after signing).
func (h chaos13562DeviceHarness) approve(t *testing.T, userCode string, bodyScopes []string, assertion chaos13562Assertion, jti string, tamper func([]byte) []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(contractsv1.DeviceApprovalRequest{SchemaVersion: contractsv1.DeviceApprovalRequestSchema, UserCode: userCode, RepositoryScopes: bodyScopes})
	if err != nil {
		t.Fatal(err)
	}
	assertion.key, assertion.now, assertion.subject, assertion.org = h.key, h.now, "user_123", "org_1"
	signed := assertion.sign(t, http.MethodPost, "/api/v1/oauth/device_approval", body, jti)
	if tamper != nil {
		body = tamper(body)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/device_approval", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(auth.WebAssertionHeader, signed)
	recorder := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(recorder, request)
	return recorder
}

func (h chaos13562DeviceHarness) redeem(t *testing.T, deviceCode string) contractsv1.DeviceTokenResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(recorder, deviceTokenRequest(t, deviceCode))
	if recorder.Code != http.StatusOK {
		t.Fatalf("redemption status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var issued contractsv1.DeviceTokenResponse
	if err := json.NewDecoder(recorder.Body).Decode(&issued); err != nil {
		t.Fatal(err)
	}
	if err := issued.Validate(); err != nil {
		t.Fatalf("issued response invalid: %v", err)
	}
	return issued
}

func chaos13562Details(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("error body: %v %s", err, recorder.Body.String())
	}
	if envelope.Error.Code != "invalid_request" {
		t.Fatalf("error code = %q body=%s", envelope.Error.Code, recorder.Body.String())
	}
	return envelope.Error.Details
}

func TestCHAOS13562_DeviceApprovalServesUpTo256MaxLengthRepositories(t *testing.T) {
	for _, count := range []int{100, 101, chaos13562Bound} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			h := newCHAOS13562DeviceHarness(t)
			authorization := h.start(t)
			scopes := chaos13562Slugs(count)
			recorder := h.approve(t, authorization.UserCode, scopes, chaos13562Assertion{scopes: scopes}, "approve", nil)
			if recorder.Code != http.StatusOK {
				t.Fatalf("approval of %d max-length repositories: status = %d body=%.300s", count, recorder.Code, recorder.Body.String())
			}
			issued := h.redeem(t, authorization.DeviceCode)
			if len(issued.Credential.RepositoryScopes) != count {
				t.Fatalf("credential holds %d repositories, want %d", len(issued.Credential.RepositoryScopes), count)
			}
		})
	}
}

func TestCHAOS13562_DeviceApprovalRefuses257RepositoriesByName(t *testing.T) {
	h := newCHAOS13562DeviceHarness(t)
	authorization := h.start(t)
	scopes := chaos13562Slugs(chaos13562Bound + 1)
	recorder := h.approve(t, authorization.UserCode, scopes, chaos13562Assertion{scopes: scopes}, "approve", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%.300s, want 400", recorder.Code, recorder.Body.String())
	}
	details := chaos13562Details(t, recorder)
	if details["reason"] != "too_many_repositories" || details["max_repository_scopes"] != float64(chaos13562Bound) {
		t.Fatalf("details = %v, want reason too_many_repositories and max_repository_scopes 256", details)
	}
	if !contractsv1.IsInvalidRequestReason("too_many_repositories") {
		t.Fatal("too_many_repositories is not in the closed invalid_request vocabulary")
	}
}

func TestCHAOS13562_DeviceApprovalWithoutTheClaimTakesTheBodyList(t *testing.T) {
	for _, scopes := range [][]string{{"*"}, {hostedTestRepository}, chaos13562Slugs(chaos13562Bound)} {
		t.Run(fmt.Sprint(len(scopes)), func(t *testing.T) {
			h := newCHAOS13562DeviceHarness(t)
			authorization := h.start(t)
			recorder := h.approve(t, authorization.UserCode, scopes, chaos13562Assertion{omitScopes: true}, "approve", nil)
			if recorder.Code != http.StatusOK {
				t.Fatalf("claim-less approval: status = %d body=%.300s", recorder.Code, recorder.Body.String())
			}
			issued := h.redeem(t, authorization.DeviceCode)
			if fmt.Sprint(issued.Credential.RepositoryScopes) != fmt.Sprint(scopes) {
				t.Fatalf("credential scopes = %.200v, want the body list", issued.Credential.RepositoryScopes)
			}
		})
	}
}

func TestCHAOS13562_DeviceApprovalRefusesAClaimThatDiffersFromTheBody(t *testing.T) {
	cells := []struct {
		name        string
		claim, body []string
	}{
		{"claim wider than body", []string{"example-org/a", "example-org/b"}, []string{"example-org/a"}},
		{"claim narrower than body", []string{"example-org/a"}, []string{"example-org/a", "example-org/b"}},
		{"claim organization-wide, body named", []string{"*"}, []string{"example-org/a"}},
		{"claim named, body organization-wide", []string{"example-org/a"}, []string{"*"}},
		{"same size, other repository", []string{"example-org/b"}, []string{"example-org/a"}},
	}
	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			h := newCHAOS13562DeviceHarness(t)
			authorization := h.start(t)
			recorder := h.approve(t, authorization.UserCode, cell.body, chaos13562Assertion{scopes: cell.claim}, "approve", nil)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("claim %v body %v: status = %d body=%s, want 400", cell.claim, cell.body, recorder.Code, recorder.Body.String())
			}
			pending := httptest.NewRecorder()
			h.app.Handler().ServeHTTP(pending, deviceTokenRequest(t, authorization.DeviceCode))
			assertOAuthDeviceError(t, pending, contractsv1.OAuthDeviceErrorAuthorizationPending)
		})
	}
}

func TestCHAOS13562_DeviceApprovalRefusesABodyChangedAfterSigning(t *testing.T) {
	for _, omit := range []bool{false, true} {
		t.Run(fmt.Sprintf("claim omitted %v", omit), func(t *testing.T) {
			h := newCHAOS13562DeviceHarness(t)
			authorization := h.start(t)
			signedScopes := []string{"example-org/a"}
			recorder := h.approve(t, authorization.UserCode, signedScopes, chaos13562Assertion{scopes: signedScopes, omitScopes: omit}, "approve", func(body []byte) []byte {
				return bytes.Replace(body, []byte(`"example-org/a"`), []byte(`"example-org/b"`), 1)
			})
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("changed body: status = %d body=%s, want 401", recorder.Code, recorder.Body.String())
			}
			pending := httptest.NewRecorder()
			h.app.Handler().ServeHTTP(pending, deviceTokenRequest(t, authorization.DeviceCode))
			assertOAuthDeviceError(t, pending, contractsv1.OAuthDeviceErrorAuthorizationPending)
		})
	}
}

// A claim-less assertion is only an approval's: the device preview needs the
// claim to filter the hints, and every other route refuses it.
func TestCHAOS13562_ClaimLessAssertionIsRefusedOutsideTheApprovals(t *testing.T) {
	h := newCHAOS13562DeviceHarness(t)
	authorization := h.start(t)
	preview, err := json.Marshal(contractsv1.DeviceApprovalPreviewRequest{SchemaVersion: contractsv1.DeviceApprovalPreviewRequestSchema, UserCode: authorization.UserCode})
	if err != nil {
		t.Fatal(err)
	}
	assertion := chaos13562Assertion{key: h.key, now: h.now, subject: "user_123", org: "org_1", omitScopes: true}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/oauth/device_approval", bytes.NewReader(preview))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(auth.WebAssertionHeader, assertion.sign(t, http.MethodPost, "/api/v1/oauth/device_approval", preview, "preview"))
	recorder := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("claim-less preview: status = %d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
	body := []byte(`{}`)
	packets := httptest.NewRequest(http.MethodPost, "/api/v1/agent-context/context-packets", bytes.NewReader(body))
	packets.Header.Set("Content-Type", "application/json")
	packets.Header.Set(auth.WebAssertionHeader, assertion.sign(t, http.MethodPost, "/api/v1/agent-context/context-packets", body, "packets"))
	recorder = httptest.NewRecorder()
	h.app.Handler().ServeHTTP(recorder, packets)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("claim-less assertion on context-packets: status = %d body=%s, want 401", recorder.Code, recorder.Body.String())
	}

	app, clientID := chaos13562ConsentApp(t)
	form := []byte(oauthTestAuthorizeQuery(clientID).Encode())
	firstParty := httptest.NewRequest(http.MethodPost, OAuthFirstPartyAuthorizePath, bytes.NewReader(form))
	firstParty.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	consentAssertion := chaos13562Assertion{key: oauthTestWebKey, now: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC), subject: oauthConsentSubject, org: oauthTestOrg, omitScopes: true}
	firstParty.Header.Set(auth.WebAssertionHeader, consentAssertion.sign(t, http.MethodPost, OAuthFirstPartyAuthorizePath, form, "first-party"))
	recorder = httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, firstParty)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("claim-less assertion on the first-party authorize: status = %d body=%s, want 401", recorder.Code, recorder.Body.String())
	}
}

func chaos13562ConsentApp(t *testing.T) (*App, string) {
	t.Helper()
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	return app, registerOAuthTestClient(t, app, "c")
}

func chaos13562Consent(t *testing.T, app *App, body []byte, assertion chaos13562Assertion, jti string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	assertion.key, assertion.now, assertion.subject, assertion.org = oauthTestWebKey, time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC), oauthConsentSubject, oauthTestOrg
	request := httptest.NewRequest(http.MethodPost, OAuthConsentPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(auth.WebAssertionHeader, assertion.sign(t, http.MethodPost, OAuthConsentPath, body, jti))
	return serveConsent(app, request)
}

func chaos13562ApproveBody(t *testing.T, handle string, scopes []string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"action": "approve", "handle": handle, "repository_scopes": scopes})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestCHAOS13562_ConsentServesUpTo256MaxLengthRepositories(t *testing.T) {
	for _, count := range []int{100, 101, chaos13562Bound} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			app, clientID := chaos13562ConsentApp(t)
			handle := authorizeToConsent(t, app, oauthTestAuthorizeQuery(clientID))
			scopes := chaos13562Slugs(count)
			body := chaos13562ApproveBody(t, handle, scopes)
			recorder, answer := chaos13562Consent(t, app, body, chaos13562Assertion{scopes: scopes}, "approve")
			if recorder.Code != http.StatusOK || answer["redirect_url"] == nil {
				t.Fatalf("consent of %d max-length repositories (%d-byte body): %d %v", count, len(body), recorder.Code, answer)
			}
			redirect, err := url.Parse(answer["redirect_url"].(string))
			if err != nil {
				t.Fatal(err)
			}
			form := url.Values{"grant_type": {"authorization_code"}, "code": {redirect.Query().Get("code")}, "redirect_uri": {oauthTestRedirect}, "client_id": {clientID}, "code_verifier": {strings.Repeat("v", 50)}}
			token := httptest.NewRequest(http.MethodPost, OAuthTokenPath, strings.NewReader(form.Encode()))
			token.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			tokenRecorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(tokenRecorder, token)
			if tokenRecorder.Code != http.StatusOK || !strings.Contains(tokenRecorder.Body.String(), `"access_token":"fcacr_`) {
				t.Fatalf("token: %d %s", tokenRecorder.Code, tokenRecorder.Body.String())
			}
		})
	}
}

func TestCHAOS13562_ConsentRefuses257RepositoriesByName(t *testing.T) {
	app, clientID := chaos13562ConsentApp(t)
	handle := authorizeToConsent(t, app, oauthTestAuthorizeQuery(clientID))
	scopes := chaos13562Slugs(chaos13562Bound + 1)
	for _, cell := range []struct {
		name      string
		assertion chaos13562Assertion
	}{{"claim present", chaos13562Assertion{scopes: scopes}}, {"claim omitted", chaos13562Assertion{omitScopes: true}}} {
		t.Run(cell.name, func(t *testing.T) {
			recorder, answer := chaos13562Consent(t, app, chaos13562ApproveBody(t, handle, scopes), cell.assertion, "approve-"+cell.name)
			if recorder.Code != http.StatusBadRequest || answer["error"] != "too_many_repositories" {
				t.Fatalf("257 repositories: %d %v, want 400 too_many_repositories", recorder.Code, answer)
			}
		})
	}
	recorder, _ := chaos13562Consent(t, app, chaos13562ApproveBody(t, handle, scopes[:1]), chaos13562Assertion{scopes: scopes[:1]}, "after")
	if recorder.Code != http.StatusOK {
		t.Fatalf("a refused 257 decided the request: the next approve answered %d", recorder.Code)
	}
}

func TestCHAOS13562_ConsentWithoutTheClaimTakesTheBodyList(t *testing.T) {
	for _, scopes := range [][]string{{"*"}, {"org/repo"}, chaos13562Slugs(chaos13562Bound)} {
		t.Run(fmt.Sprint(len(scopes)), func(t *testing.T) {
			app, clientID := chaos13562ConsentApp(t)
			handle := authorizeToConsent(t, app, oauthTestAuthorizeQuery(clientID))
			recorder, answer := chaos13562Consent(t, app, chaos13562ApproveBody(t, handle, scopes), chaos13562Assertion{omitScopes: true}, "approve")
			if recorder.Code != http.StatusOK || answer["redirect_url"] == nil {
				t.Fatalf("claim-less consent: %d %v", recorder.Code, answer)
			}
		})
	}
}

func TestCHAOS13562_ConsentRefusesAClaimThatDiffersFromTheBody(t *testing.T) {
	cells := []struct {
		name        string
		claim, body []string
	}{
		{"claim wider than body", []string{"org/a", "org/b"}, []string{"org/a"}},
		{"claim narrower than body", []string{"org/a"}, []string{"org/a", "org/b"}},
		{"claim organization-wide, body named", []string{"*"}, []string{"org/a"}},
		{"claim named, body organization-wide", []string{"org/a"}, []string{"*"}},
	}
	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			app, clientID := chaos13562ConsentApp(t)
			handle := authorizeToConsent(t, app, oauthTestAuthorizeQuery(clientID))
			recorder, answer := chaos13562Consent(t, app, chaos13562ApproveBody(t, handle, cell.body), chaos13562Assertion{scopes: cell.claim}, "approve")
			if recorder.Code != http.StatusBadRequest || answer["error"] != "invalid_request" {
				t.Fatalf("claim %v body %v: %d %v, want 400 invalid_request", cell.claim, cell.body, recorder.Code, answer)
			}
			recorder, _ = chaos13562Consent(t, app, chaos13562ApproveBody(t, handle, cell.body), chaos13562Assertion{scopes: cell.body}, "again")
			if recorder.Code != http.StatusOK {
				t.Fatalf("a refused mismatch decided the request: the matching approve answered %d", recorder.Code)
			}
		})
	}
}

func TestCHAOS13562_ConsentRefusesABodyChangedAfterSigning(t *testing.T) {
	for _, omit := range []bool{false, true} {
		t.Run(fmt.Sprintf("claim omitted %v", omit), func(t *testing.T) {
			app, clientID := chaos13562ConsentApp(t)
			handle := authorizeToConsent(t, app, oauthTestAuthorizeQuery(clientID))
			signedBody := chaos13562ApproveBody(t, handle, []string{"org/a"})
			assertion := chaos13562Assertion{key: oauthTestWebKey, now: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC), subject: oauthConsentSubject, org: oauthTestOrg, scopes: []string{"org/a"}, omitScopes: omit}
			signed := assertion.sign(t, http.MethodPost, OAuthConsentPath, signedBody, "approve")
			changed := bytes.Replace(signedBody, []byte(`"org/a"`), []byte(`"org/b"`), 1)
			request := httptest.NewRequest(http.MethodPost, OAuthConsentPath, bytes.NewReader(changed))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(auth.WebAssertionHeader, signed)
			recorder, answer := serveConsent(app, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("changed body: %d %v, want 401", recorder.Code, answer)
			}
		})
	}
}
