package entitlements

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClientHasEntitlement_uses_fixed_path_and_bounded_cache(t *testing.T) {
	// Given
	var calls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got, want := r.URL.Path, "/api/v1/internal/acr/entitlements/org-1"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true}`))
	}))
	defer server.Close()
	client := newClientForBoundaryTest(t, server)
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	client.now = func() time.Time { return now }

	// When
	first, firstErr := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime")
	second, secondErr := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime")
	now = now.Add(31 * time.Second)
	third, thirdErr := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime")

	// Then
	if firstErr != nil || secondErr != nil || thirdErr != nil || !first || !second || !third || calls != 2 {
		t.Fatalf("results = %t/%t/%t, calls = %d; want cached then expired", first, second, third, calls)
	}
}

func TestClientNew_rejects_insecure_token_file_permissions(t *testing.T) {
	// Given
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	config := newClientConfigForBoundaryTest(t, server)
	if err := os.Chmod(config.TokenFile, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	// When
	_, err := New(config)

	// Then
	if err == nil || strings.Contains(err.Error(), config.TokenFile) {
		t.Fatalf("New() error = %v; want redacted permission failure", err)
	}
}

func TestClientNew_ignores_environment_proxy_policy(t *testing.T) {
	// Given
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true}`))
	}))
	defer server.Close()
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	client := newClientForBoundaryTest(t, server)

	// When
	entitled, err := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime")

	// Then
	if err != nil || !entitled {
		t.Fatalf("HasEntitlement() = %t, %v; want environment proxy ignored", entitled, err)
	}
}

func TestCache_evicts_oldest_entry_at_capacity(t *testing.T) {
	// Given
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	cache := newCache(2)
	cache.put("oldest", true, now.Add(time.Second))
	cache.put("newer", true, now.Add(2*time.Second))

	// When
	cache.put("latest", false, now.Add(3*time.Second))

	// Then
	if _, found := cache.get("oldest", now); found {
		t.Fatal("cache retained oldest entry beyond its capacity")
	}
	if len(cache.items) != 2 {
		t.Fatalf("cache cardinality = %d, want 2", len(cache.items))
	}
}

func newClientForBoundaryTest(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := New(newClientConfigForBoundaryTest(t, server))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func newClientConfigForBoundaryTest(t *testing.T, server *httptest.Server) Config {
	t.Helper()
	certificate, err := x509.ParseCertificate(server.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate() error = %v", err)
	}
	directory := t.TempDir()
	caFile := filepath.Join(directory, "server.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o600); err != nil {
		t.Fatalf("WriteFile(CA) error = %v", err)
	}
	tokenFile := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenFile, []byte("boundary-test-token"), 0o600); err != nil {
		t.Fatalf("WriteFile(token) error = %v", err)
	}
	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	return Config{
		BaseURL: baseURL, TokenFile: tokenFile, CACertPath: caFile, Timeout: time.Second,
		MaxResponseBytes: 2048, PositiveCacheTTL: 30 * time.Second,
		NegativeCacheTTL: time.Second, CacheCapacity: 2,
	}
}
