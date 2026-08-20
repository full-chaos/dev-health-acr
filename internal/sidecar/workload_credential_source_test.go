package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

func writeSubjectTokenFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "subject-token")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorkloadCredentialSource_exchangesAndCachesUntilNearExpiry(t *testing.T) {
	validToken, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	var exchangeCount atomic.Int32
	var lastForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchangeCount.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		lastForm = r.PostForm
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema_version": "token_exchange_response.v1", "access_token": validToken,
			"issued_token_type": "urn:ietf:params:oauth:token-type:access_token", "token_type": "Bearer", "expires_in": 600,
		})
	}))
	defer server.Close()

	endpoint, err := url.Parse(server.URL + "/api/v1/oauth/token")
	if err != nil {
		t.Fatal(err)
	}
	subjectTokenPath := writeSubjectTokenFile(t, "the-subject-jwt\n")
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	source, err := NewWorkloadCredentialSource(WorkloadCredentialSourceOptions{
		TokenEndpoint: endpoint, SubjectTokenFile: subjectTokenPath, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := source()
	if err != nil {
		t.Fatal(err)
	}
	if first.Source != "workload_token_exchange" || !auth.IsTokenShapeValid(first.Token) {
		t.Fatalf("first result = %#v", first)
	}
	if lastForm.Get("grant_type") != workloadTokenExchangeGrantType || lastForm.Get("subject_token") != "the-subject-jwt" || lastForm.Get("subject_token_type") != workloadSubjectTokenType {
		t.Fatalf("exchange form = %#v", lastForm)
	}

	// Second call, still within the cache window: no new exchange.
	second, err := source()
	if err != nil {
		t.Fatal(err)
	}
	if second.Token != first.Token {
		t.Fatal("expected the cached token to be reused")
	}
	if exchangeCount.Load() != 1 {
		t.Fatalf("exchange count = %d, want exactly 1 (second call should be cached)", exchangeCount.Load())
	}
}

func TestWorkloadCredentialSource_reExchangesAfterExpiryAndRereadsTheSubjectTokenFile(t *testing.T) {
	validToken, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	var forms []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		forms = append(forms, r.PostForm)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema_version": "token_exchange_response.v1", "access_token": validToken,
			"issued_token_type": "urn:ietf:params:oauth:token-type:access_token", "token_type": "Bearer", "expires_in": 60,
		})
	}))
	defer server.Close()

	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	subjectTokenPath := writeSubjectTokenFile(t, "token-v1")
	current := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	source, err := NewWorkloadCredentialSource(WorkloadCredentialSourceOptions{
		TokenEndpoint: endpoint, SubjectTokenFile: subjectTokenPath, Now: func() time.Time { return current },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source(); err != nil {
		t.Fatal(err)
	}

	// Simulate kubelet rotating the projected subject token in place, and
	// advance past the 30s refresh margin (expires_in=60s).
	if err := os.WriteFile(subjectTokenPath, []byte("token-v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	current = current.Add(45 * time.Second)
	if _, err := source(); err != nil {
		t.Fatal(err)
	}

	if len(forms) != 2 {
		t.Fatalf("exchange count = %d, want 2 (cache expired)", len(forms))
	}
	if forms[0].Get("subject_token") != "token-v1" || forms[1].Get("subject_token") != "token-v2" {
		t.Fatalf("subject tokens across exchanges = %q, %q", forms[0].Get("subject_token"), forms[1].Get("subject_token"))
	}
}

func TestWorkloadCredentialSource_rejectsAResponseShapedLikeALicenseKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "not-an-acr-token", "expires_in": 600})
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewWorkloadCredentialSource(WorkloadCredentialSourceOptions{
		TokenEndpoint: endpoint, SubjectTokenFile: writeSubjectTokenFile(t, "jwt"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source(); err != ErrCredentialShapeInvalid {
		t.Fatalf("error = %v, want ErrCredentialShapeInvalid", err)
	}
}

func TestWorkloadCredentialSource_serverErrorPropagates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": "oauth_token_exchange_error.v1", "error": "invalid_grant"})
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewWorkloadCredentialSource(WorkloadCredentialSourceOptions{
		TokenEndpoint: endpoint, SubjectTokenFile: writeSubjectTokenFile(t, "jwt"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source(); err == nil {
		t.Fatal("expected an error for a non-200 token exchange response")
	}
}

func TestNewWorkloadCredentialSource_requiresEndpointAndSubjectTokenFile(t *testing.T) {
	endpoint, err := url.Parse("https://acr-api.internal.example/api/v1/oauth/token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewWorkloadCredentialSource(WorkloadCredentialSourceOptions{SubjectTokenFile: "/tmp/x"}); err == nil {
		t.Fatal("expected an error for a missing TokenEndpoint")
	}
	if _, err := NewWorkloadCredentialSource(WorkloadCredentialSourceOptions{TokenEndpoint: endpoint}); err == nil {
		t.Fatal("expected an error for a missing SubjectTokenFile")
	}
}
