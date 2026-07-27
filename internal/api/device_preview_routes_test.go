package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestDeviceApprovalPreview_rejectsExplicitNullRepositoryScopes(t *testing.T) {
	// Given
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
	approval := deviceApprovalRequest(t, now, private, contractsv1.DeviceApprovalRequest{
		SchemaVersion: contractsv1.DeviceApprovalPreviewRequestSchema,
		UserCode:      authorization.UserCode,
	}, "approval_preview_null_scopes")
	approvalBody, err := io.ReadAll(approval.Body)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	request, err := http.NewRequest(http.MethodPost, server.URL+approval.URL.Path, bytes.NewReader(approvalBody))
	if err != nil {
		t.Fatal(err)
	}
	request.Header = approval.Header.Clone()

	// When
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	// Then
	if response.StatusCode != http.StatusBadRequest {
		body, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		t.Fatalf("status = %d body=%s", response.StatusCode, body)
	}
}
