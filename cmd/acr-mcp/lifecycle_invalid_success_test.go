package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

func TestLoginWarnsAboutManualRevocation_whenTokenEndpointReturnsInvalidSuccessWithoutToken(t *testing.T) {
	fixture := registerLifecycleFixture(t)
	issuedActive := false
	revocations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer recordFixturePanic(fixture, w)
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
			issuedActive = true
			_, _ = w.Write([]byte(`{"schema_version":"unsupported.v2"}`))
		case "/api/v1/auth/credentials/self/revoke":
			revocations++
			w.WriteHeader(http.StatusUnauthorized)
		default:
			fixture.recordProblem("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "token")
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)
	originalWait := lifecycleWait
	lifecycleWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { lifecycleWait = originalWait })

	code, stderr := captureStderr(t, func() int { return runCLI([]string{"login", "--no-browser"}) })

	if code != lifecycleExitFailure {
		t.Fatalf("login exit code = %d, want failure", code)
	}
	if !issuedActive {
		t.Fatal("token endpoint did not issue an active credential")
	}
	if revocations != 0 {
		t.Fatalf("self-revocations = %d, want 0 without a usable token", revocations)
	}
	if !strings.Contains(stderr, "revoke it in the dashboard") {
		t.Fatalf("login stderr = %q, want manual-revocation guidance", stderr)
	}
	if strings.Contains(stderr, "login successful") {
		t.Fatalf("login stderr = %q, must not report success", stderr)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credential persisted after invalid successful token response: %v", err)
	}
}

func TestRefreshWarnsAboutManualRevocation_whenRotateReturnsInvalidSuccessWithoutToken(t *testing.T) {
	original := validDoctorToken(121)
	successorActive := false
	revocations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/credentials/self/rotate":
			if r.Header.Get("Authorization") != "Bearer "+original {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			successorActive = true
			_, _ = w.Write([]byte(`{"schema_version":"unsupported.v2"}`))
		case "/api/v1/auth/credentials/self/revoke":
			revocations++
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(original+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)

	code, stderr := captureStderr(t, func() int { return runCLI([]string{"login", "--refresh"}) })

	if code != lifecycleExitFailure {
		t.Fatalf("refresh exit code = %d, want failure", code)
	}
	if !successorActive {
		t.Fatal("rotation endpoint did not issue an active successor")
	}
	if revocations != 0 {
		t.Fatalf("self-revocations = %d, want 0 without a usable successor token", revocations)
	}
	if !strings.Contains(stderr, "revoke it in the dashboard") {
		t.Fatalf("refresh stderr = %q, want manual-revocation guidance", stderr)
	}
	if strings.Contains(stderr, "credential refreshed successfully") {
		t.Fatalf("refresh stderr = %q, must not report success", stderr)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original credential: %v", err)
	}
	if string(contents) != original+"\n" {
		t.Fatalf("credential after invalid successful rotation = %q, want original", contents)
	}
}

func TestRefreshRevokesShapeValidSuccessor_whenRotateReturnsInvalidSemanticSuccess(t *testing.T) {
	original := validDoctorToken(122)
	successor := validDoctorToken(123)
	createdAt := time.Now().UTC().Truncate(time.Second)
	var mu sync.Mutex
	successorActive := false
	revocations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/credentials/self/rotate":
			if r.Header.Get("Authorization") != "Bearer "+original {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			mu.Lock()
			successorActive = true
			mu.Unlock()
			response := struct {
				SchemaVersion string                       `json:"schema_version"`
				AccessToken   string                       `json:"access_token"`
				Credential    contractsv1.ClientCredential `json:"credential"`
			}{
				SchemaVersion: contractsv1.CredentialRotateResponseSchema,
				AccessToken:   successor,
				Credential:    lifecycleCredential(createdAt, "credential-successor", nil),
			}
			if err := json.NewEncoder(w).Encode(response); err != nil {
				t.Errorf("encode invalid rotation response: %v", err)
			}
		case "/api/v1/auth/credentials/self/revoke":
			if r.Header.Get("Authorization") != "Bearer "+successor {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			mu.Lock()
			revocations++
			successorActive = false
			mu.Unlock()
			revokedAt := createdAt.Add(time.Minute)
			writeLifecycleJSON(t, w, contractsv1.CredentialRevokeResponse{
				SchemaVersion: contractsv1.CredentialRevokeResponseSchema,
				Credential:    lifecycleCredential(createdAt, "credential-successor", &revokedAt),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(original+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	t.Setenv(sidecar.TokenFileEnvironment, path)

	code, stderr := captureStderr(t, func() int { return runCLI([]string{"login", "--refresh"}) })

	if code != lifecycleExitFailure {
		t.Fatalf("refresh exit code = %d, want failure", code)
	}
	mu.Lock()
	active, revokeCount := successorActive, revocations
	mu.Unlock()
	if active {
		t.Fatal("shape-valid successor remained active after invalid semantic rotation response")
	}
	if revokeCount != 1 {
		t.Fatalf("self-revocations = %d, want 1 for the recoverable successor", revokeCount)
	}
	if !strings.Contains(stderr, "invalid refreshed credential response was revoked") {
		t.Fatalf("refresh stderr = %q, want confirmed successor revocation", stderr)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original credential: %v", err)
	}
	if string(contents) != original+"\n" {
		t.Fatalf("credential after invalid semantic rotation = %q, want original", contents)
	}
}
