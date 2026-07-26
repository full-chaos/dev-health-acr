package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestDeviceRoutes_HTTPFlowApprovesAndRedeemsOnlyOnce(t *testing.T) {
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
	created := deviceRequest(t, http.MethodPost, "/api/v1/oauth/device_authorization", contractsv1.DeviceAuthorizationRequest{SchemaVersion: contractsv1.DeviceAuthorizationRequestSchema})
	createdResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(createdResponse, created)
	if createdResponse.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var authorization contractsv1.DeviceAuthorizationResponse
	if err := json.NewDecoder(createdResponse.Body).Decode(&authorization); err != nil {
		t.Fatal(err)
	}
	if err := authorization.Validate(); err != nil {
		t.Fatalf("authorization response invalid: %v", err)
	}

	approval := contractsv1.DeviceApprovalRequest{SchemaVersion: contractsv1.DeviceApprovalRequestSchema, UserCode: authorization.UserCode, RepositoryScopes: []string{hostedTestRepository}}
	approvalRequest := deviceApprovalRequest(t, now, private, approval, "approval_1")
	approvalResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(approvalResponse, approvalRequest)
	if approvalResponse.Code != http.StatusOK {
		t.Fatalf("approval status = %d body=%s", approvalResponse.Code, approvalResponse.Body.String())
	}

	redeemedResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(redeemedResponse, deviceTokenRequest(t, authorization.DeviceCode))
	if redeemedResponse.Code != http.StatusOK {
		t.Fatalf("redemption status = %d body=%s", redeemedResponse.Code, redeemedResponse.Body.String())
	}
	var issued contractsv1.DeviceTokenResponse
	if err := json.NewDecoder(redeemedResponse.Body).Decode(&issued); err != nil {
		t.Fatal(err)
	}
	if err := issued.Validate(); err != nil {
		t.Fatalf("issued response invalid: %v", err)
	}
	if !auth.IsTokenShapeValid(issued.AccessToken) {
		t.Fatal("device flow did not return an ACR credential")
	}

	againResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(againResponse, deviceTokenRequest(t, authorization.DeviceCode))
	assertOAuthDeviceError(t, againResponse, contractsv1.OAuthDeviceErrorInvalidGrant)

	pendingResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(pendingResponse, deviceRequest(t, http.MethodPost, "/api/v1/oauth/device_authorization", contractsv1.DeviceAuthorizationRequest{SchemaVersion: contractsv1.DeviceAuthorizationRequestSchema}))
	if pendingResponse.Code != http.StatusOK {
		t.Fatalf("second create status = %d body=%s", pendingResponse.Code, pendingResponse.Body.String())
	}
	var pending contractsv1.DeviceAuthorizationResponse
	if err := json.NewDecoder(pendingResponse.Body).Decode(&pending); err != nil {
		t.Fatal(err)
	}
	pendingPoll := httptest.NewRecorder()
	app.Handler().ServeHTTP(pendingPoll, deviceTokenRequest(t, pending.DeviceCode))
	assertOAuthDeviceError(t, pendingPoll, contractsv1.OAuthDeviceErrorAuthorizationPending)
}

func TestDeviceApproval_requiresOnlyBoundCredentialIssueAssertion(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	app, bearer := newHostedTestAppWithWebAssertions(t, nil, nil, nil, nil, nil, verifier)
	request := deviceApprovalRequest(t, now, private, contractsv1.DeviceApprovalRequest{SchemaVersion: contractsv1.DeviceApprovalRequestSchema, UserCode: "ABCDEFGH", RepositoryScopes: []string{hostedTestRepository}}, "approval_bearer")
	request.Header.Set("Authorization", "Bearer "+bearer)
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, request)

	assertErrorResponse(t, response, http.StatusUnauthorized, "invalid_token")
}

func TestDeviceRoutes_returnUnavailableWithoutWebAssertions(t *testing.T) {
	// Given
	app, _ := newHostedTestApp(t, nil, nil, nil, nil, nil)
	request := deviceRequest(t, http.MethodPost, "/api/v1/oauth/device_authorization", contractsv1.DeviceAuthorizationRequest{SchemaVersion: contractsv1.DeviceAuthorizationRequestSchema})
	response := httptest.NewRecorder()

	// When
	app.Handler().ServeHTTP(response, request)

	// Then
	assertErrorResponse(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
}

func TestSelfLifecycleRoutes_rejectDuplicateAuthorizationHeaders(t *testing.T) {
	// Given
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, nil, nil)
	request := deviceRequest(t, http.MethodPost, "/api/v1/auth/credentials/self/revoke", contractsv1.CredentialRevokeRequest{SchemaVersion: contractsv1.CredentialRevokeRequestSchema})
	request.Header["Authorization"] = []string{"Bearer " + token, "Bearer fcacr_abcdefghijklmnopqrstuvwxyz0123456789"}
	response := httptest.NewRecorder()

	// When
	app.Handler().ServeHTTP(response, request)

	// Then
	assertErrorResponse(t, response, http.StatusUnauthorized, "invalid_token")
}

func TestSelfLifecycleError_returnsNonRetryableConflictForStaleMutation(t *testing.T) {
	// Given
	app, _ := newHostedTestApp(t, nil, nil, nil, nil, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/credentials/self/rotate", nil)
	response := httptest.NewRecorder()

	// When
	app.writeSelfLifecycleError(response, request, storage.ErrConflict)

	// Then
	assertErrorResponse(t, response, http.StatusConflict, "credential_lifecycle_conflict")
}

func TestSelfLifecycleRoutes_rotateThenRevokeCurrentBearer(t *testing.T) {
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, nil, nil)
	rotate := deviceRequest(t, http.MethodPost, "/api/v1/auth/credentials/self/rotate", contractsv1.CredentialRotateRequest{SchemaVersion: contractsv1.CredentialRotateRequestSchema})
	rotate.Header.Set("Authorization", "Bearer "+token)
	rotatedResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(rotatedResponse, rotate)
	if rotatedResponse.Code != http.StatusOK {
		t.Fatalf("rotate status = %d body=%s", rotatedResponse.Code, rotatedResponse.Body.String())
	}
	var rotated contractsv1.CredentialRotateResponse
	if err := json.NewDecoder(rotatedResponse.Body).Decode(&rotated); err != nil {
		t.Fatal(err)
	}
	if err := rotated.Validate(); err != nil {
		t.Fatalf("rotate response invalid: %v", err)
	}
	revoke := deviceRequest(t, http.MethodPost, "/api/v1/auth/credentials/self/revoke", contractsv1.CredentialRevokeRequest{SchemaVersion: contractsv1.CredentialRevokeRequestSchema})
	revoke.Header.Set("Authorization", "Bearer "+rotated.AccessToken)
	revokedResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(revokedResponse, revoke)
	if revokedResponse.Code != http.StatusOK {
		t.Fatalf("revoke status = %d body=%s", revokedResponse.Code, revokedResponse.Body.String())
	}
	var revoked contractsv1.CredentialRevokeResponse
	if err := json.NewDecoder(revokedResponse.Body).Decode(&revoked); err != nil {
		t.Fatal(err)
	}
	if err := revoked.Validate(); err != nil {
		t.Fatalf("revoke response invalid: %v", err)
	}
}

func deviceRequest(t *testing.T, method, path string, value any) *http.Request {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func deviceTokenRequest(t *testing.T, deviceCode string) *http.Request {
	t.Helper()
	return deviceRequest(t, http.MethodPost, "/api/v1/oauth/token", contractsv1.DeviceTokenRequest{
		SchemaVersion: contractsv1.DeviceTokenRequestSchema, GrantType: contractsv1.DeviceCodeGrantType, DeviceCode: deviceCode,
	})
}

func deviceApprovalRequest(t *testing.T, now time.Time, private ed25519.PrivateKey, approval contractsv1.DeviceApprovalRequest, jti string) *http.Request {
	t.Helper()
	request := deviceRequest(t, http.MethodPost, "/api/v1/oauth/device_approval", approval)
	body, err := json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	claims := map[string]any{
		"iss": "https://web.example.test", "aud": "acr-api", "sub": "user_123", "org_id": "org_1",
		"repository_scopes": []string{hostedTestRepository}, "permissions": []string{auth.WebAssertionPermissionCredentialIssue},
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(30 * time.Second).Unix(), "jti": jti,
		"method": request.Method, "path": request.URL.EscapedPath(), "body_sha256": base64.RawURLEncoding.EncodeToString(digest[:]),
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
	request.Header.Set(auth.WebAssertionHeader, input+"."+base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(input))))
	return request
}

func assertOAuthDeviceError(t *testing.T, response *httptest.ResponseRecorder, want contractsv1.OAuthDeviceErrorCode) {
	t.Helper()
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var actual contractsv1.OAuthDeviceErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&actual); err != nil {
		t.Fatal(err)
	}
	if err := actual.Validate(); err != nil || actual.Error != want {
		t.Fatalf("OAuth error = %#v validation=%v want=%q", actual, err, want)
	}
}
