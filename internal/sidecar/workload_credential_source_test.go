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

func TestWorkloadCredentialSource_rejectsAPlainHTTPNonLoopbackEndpoint(t *testing.T) {
	endpoint, err := url.Parse("http://acr-api.example.com/api/v1/oauth/token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewWorkloadCredentialSource(WorkloadCredentialSourceOptions{
		TokenEndpoint: endpoint, SubjectTokenFile: writeSubjectTokenFile(t, "jwt"),
	}); err == nil {
		t.Fatal("expected an error for a plain-http, non-loopback token endpoint -- the subject JWT must never be sendable in plaintext to an arbitrary host")
	}
}

func TestWorkloadCredentialSource_refusesARedirectResponse(t *testing.T) {
	// Codex round 1 finding: a redirect must never be followed, since that
	// could retarget where the subject JWT is delivered.
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the redirect target must never be reached: CheckRedirect should refuse the redirect first")
	}))
	defer redirectTarget.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	endpoint, err := url.Parse(origin.URL)
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
		t.Fatal("expected an error: the redirect must be refused, not followed")
	}
}

func TestWorkloadCredentialSource_timesOutAnUnresponsiveEndpoint(t *testing.T) {
	// Codex round 2 finding: an unresponsive endpoint must not hold this
	// source's mutex (and every concurrent caller behind it) forever.
	unblock := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-unblock
	}))
	// Unblock the handler BEFORE server.Close(): Close() waits (via an
	// internal WaitGroup) for every in-flight handler to return, and this
	// handler never returns until unblock is closed. Deferred calls run
	// LIFO, so the LAST defer here runs FIRST -- close(unblock) must be
	// deferred after server.Close() to unblock it before Close() waits.
	defer server.Close()
	defer close(unblock)

	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewWorkloadCredentialSource(WorkloadCredentialSourceOptions{
		TokenEndpoint: endpoint, SubjectTokenFile: writeSubjectTokenFile(t, "jwt"), Timeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := source(); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exchange did not respect the configured Timeout")
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
