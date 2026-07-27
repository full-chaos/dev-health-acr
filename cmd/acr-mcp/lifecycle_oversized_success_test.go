package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

const oversizedLifecycleResponseLimit = 8192

func TestLoginReportsManualRevocation_whenSuccessfulTokenResponseExceedsLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("credential persistence preflight is unsupported on Windows")
	}

	// Given
	token := validDoctorToken(106)
	body := oversizedDeviceTokenResponse(t, token)
	issued := 0
	revocations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/oauth/device_authorization":
			writeLifecycleJSON(t, w, contractsv1.DeviceAuthorizationResponse{
				SchemaVersion:   contractsv1.DeviceAuthorizationResponseSchema,
				DeviceCode:      strings.Repeat("d", 32),
				UserCode:        "ABCDEFGH",
				VerificationURI: deviceVerificationURI,
				ExpiresIn:       600,
				Interval:        5,
			})
		case "/api/v1/oauth/token":
			issued++
			_, _ = w.Write(body)
		case "/api/v1/auth/credentials/self/revoke":
			revocations++
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	tokenPath := filepath.Join(t.TempDir(), "token")
	configureOversizedLifecycleTest(t, server.URL, tokenPath)
	originalWait := lifecycleWait
	lifecycleWait = func(_ context.Context, _ time.Duration) error { return nil }
	t.Cleanup(func() { lifecycleWait = originalWait })

	// When
	code, stderr := captureStderr(t, func() int { return runCLI([]string{"login", "--no-browser"}) })

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("login exit code = %d, want %d; stderr=%s", code, lifecycleExitFailure, stderr)
	}
	if issued != 1 || revocations != 0 {
		t.Fatalf("issued=%d revocations=%d, want one possible issuance and no false revocation", issued, revocations)
	}
	if !strings.Contains(stderr, "credential may have been issued") || !strings.Contains(stderr, "revoke it in the dashboard") {
		t.Fatalf("login stderr = %q, want manual dashboard revocation guidance", stderr)
	}
	if strings.Contains(stderr, token) || strings.Contains(stderr, "fcacr_") {
		t.Fatal("login stderr leaked credential material")
	}
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential persisted after oversized response: %v", err)
	}
}

func TestRefreshReportsManualRevocation_whenSuccessfulRotationResponseExceedsLimit(t *testing.T) {
	// Given
	original := validDoctorToken(107)
	successor := validDoctorToken(108)
	body := oversizedRotationResponse(t, successor)
	issued := 0
	revocations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/credentials/self/rotate":
			if r.Header.Get("Authorization") != "Bearer "+original {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			issued++
			_, _ = w.Write(body)
		case "/api/v1/auth/credentials/self/revoke":
			revocations++
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(original+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configureOversizedLifecycleTest(t, server.URL, tokenPath)

	// When
	code, stderr := captureStderr(t, func() int { return runCLI([]string{"login", "--refresh"}) })

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("refresh exit code = %d, want %d; stderr=%s", code, lifecycleExitFailure, stderr)
	}
	if issued != 1 || revocations != 0 {
		t.Fatalf("issued=%d revocations=%d, want one possible issuance and no false revocation", issued, revocations)
	}
	if !strings.Contains(stderr, "successor credential may have been issued") || !strings.Contains(stderr, "revoke it in the dashboard") {
		t.Fatalf("refresh stderr = %q, want manual dashboard revocation guidance", stderr)
	}
	if strings.Contains(stderr, successor) || strings.Contains(stderr, "fcacr_") {
		t.Fatal("refresh stderr leaked credential material")
	}
	contents, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != original+"\n" {
		t.Fatalf("local credential = %q, want original unchanged", contents)
	}
}

func configureOversizedLifecycleTest(t *testing.T, serverURL, tokenPath string) {
	t.Helper()
	t.Setenv(sidecar.APIURLEnvironment, serverURL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.MaxResponseBytesEnvironment, "8192")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, tokenPath)
}

func oversizedDeviceTokenResponse(t *testing.T, token string) []byte {
	t.Helper()
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	credential := lifecycleCredential(createdAt, "credential-oversized", nil)
	expiresAt := createdAt.Add(30 * 24 * time.Hour)
	credential.ExpiresAt = &expiresAt
	credential.RepositoryScopes = oversizedRepositoryScopes()
	response := contractsv1.DeviceTokenResponse{
		SchemaVersion: contractsv1.DeviceTokenResponseSchema,
		AccessToken:   token,
		TokenType:     "Bearer",
		ExpiresIn:     30 * 24 * 60 * 60,
		Credential:    credential,
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("oversized device-token fixture is not contract-valid: %v", err)
	}
	return marshalOversizedLifecycleResponse(t, response)
}

func oversizedRotationResponse(t *testing.T, token string) []byte {
	t.Helper()
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	credential := lifecycleCredential(createdAt, "credential-replacement", nil)
	credential.RepositoryScopes = oversizedRepositoryScopes()
	response := contractsv1.CredentialRotateResponse{
		SchemaVersion: contractsv1.CredentialRotateResponseSchema,
		AccessToken:   token,
		Credential:    credential,
		Receipt: contractsv1.CredentialRotationReceipt{
			SourceCredentialID:      "credential-original",
			ReplacementCredentialID: credential.CredentialID,
			RollbackUntil:           createdAt.Add(15 * time.Minute),
		},
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("oversized rotation fixture is not contract-valid: %v", err)
	}
	return marshalOversizedLifecycleResponse(t, response)
}

func oversizedRepositoryScopes() []string {
	repositories := make([]string, 100)
	for index := range repositories {
		repositories[index] = fmt.Sprintf("owner/repository-%03d-%s", index, strings.Repeat("a", 80))
	}
	return repositories
}

func marshalOversizedLifecycleResponse(t *testing.T, response any) []byte {
	t.Helper()
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= oversizedLifecycleResponseLimit || len(body) >= 2*oversizedLifecycleResponseLimit {
		t.Fatalf("oversized fixture body = %d bytes, want between %d and %d", len(body), oversizedLifecycleResponseLimit, 2*oversizedLifecycleResponseLimit)
	}
	return body
}
