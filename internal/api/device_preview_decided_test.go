package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestDeviceApprovalPreviewOfADecidedCodeAnswersLikeAnUnknownCode(t *testing.T) {
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
	serve := func(request *http.Request) *httptest.ResponseRecorder {
		request.Header.Set("X-Request-ID", "req_0123456789abcdef0123456789abcdef")
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		return response
	}
	jti := 0
	preview := func(userCode string) *httptest.ResponseRecorder {
		jti++
		return serve(deviceApprovalRequest(t, now, private, contractsv1.DeviceApprovalPreviewRequest{
			SchemaVersion: contractsv1.DeviceApprovalPreviewRequestSchema, UserCode: userCode,
		}, "preview_decided_"+string(rune('a'+jti))))
	}

	created := serve(deviceRequest(t, http.MethodPost, "/api/v1/oauth/device_authorization", contractsv1.DeviceAuthorizationRequest{SchemaVersion: contractsv1.DeviceAuthorizationRequestSchema}))
	if created.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", created.Code, created.Body.String())
	}
	var authorization contractsv1.DeviceAuthorizationResponse
	if err := json.NewDecoder(created.Body).Decode(&authorization); err != nil {
		t.Fatal(err)
	}
	unknownCode := "ZZZZZZZZ"
	if authorization.UserCode == unknownCode {
		unknownCode = "YYYYYYYY"
	}
	unknown := preview(unknownCode)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown-code preview status = %d body=%s", unknown.Code, unknown.Body.String())
	}
	if pending := preview(authorization.UserCode); pending.Code != http.StatusOK {
		t.Fatalf("pending preview status = %d body=%s", pending.Code, pending.Body.String())
	}

	approval := serve(deviceApprovalRequest(t, now, private, contractsv1.DeviceApprovalRequest{
		SchemaVersion: contractsv1.DeviceApprovalRequestSchema, UserCode: authorization.UserCode, RepositoryScopes: []string{"*"},
	}, "approval_decided"))
	if approval.Code != http.StatusOK {
		t.Fatalf("approval status = %d body=%s", approval.Code, approval.Body.String())
	}
	assertSameRefusal(t, "approved", unknown, preview(authorization.UserCode))

	redeemed := serve(deviceTokenRequest(t, authorization.DeviceCode))
	if redeemed.Code != http.StatusOK {
		t.Fatalf("redemption status = %d body=%s", redeemed.Code, redeemed.Body.String())
	}
	assertSameRefusal(t, "redeemed", unknown, preview(authorization.UserCode))
}

func assertSameRefusal(t *testing.T, state string, unknown, got *httptest.ResponseRecorder) {
	t.Helper()
	if got.Code != unknown.Code || got.Body.String() != unknown.Body.String() {
		t.Fatalf("%s preview = %d %s, want the unknown-code answer %d %s", state, got.Code, got.Body.String(), unknown.Code, unknown.Body.String())
	}
	if got.Header().Get("Content-Type") != unknown.Header().Get("Content-Type") {
		t.Fatalf("%s preview content type = %q, unknown-code content type = %q", state, got.Header().Get("Content-Type"), unknown.Header().Get("Content-Type"))
	}
}
