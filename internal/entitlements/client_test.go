package entitlements_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

const testToken = "service-token-not-for-production"

func TestClientHasEntitlement_returns_contract_flag_when_response_valid(t *testing.T) {
	// Given
	server := newTLSServer(t, http.StatusOK, `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true}`)
	client := newClient(t, server, entitlements.Config{})

	// When
	entitled, err := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime")

	// Then
	if err != nil || !entitled {
		t.Fatalf("HasEntitlement() = %t, %v; want true, nil", entitled, err)
	}
}

func TestClientHasEntitlement_returns_false_when_contract_flag_false(t *testing.T) {
	// Given
	server := newTLSServer(t, http.StatusOK, `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":false}`)
	client := newClient(t, server, entitlements.Config{})

	// When
	entitled, err := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime")

	// Then
	if err != nil || entitled {
		t.Fatalf("HasEntitlement() = %t, %v; want false, nil", entitled, err)
	}
}

func TestClientClose_releasesIdleConnectionsAndIsIdempotent(t *testing.T) {
	// Given
	server := newTLSServer(t, http.StatusOK, `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true}`)
	client := newClient(t, server, entitlements.Config{})
	if _, err := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime"); err != nil {
		t.Fatal(err)
	}

	// When
	firstErr := client.Close()
	secondErr := client.Close()

	// Then
	if firstErr != nil || secondErr != nil {
		t.Fatalf("Close() = (%v, %v), want (nil, nil)", firstErr, secondErr)
	}
}

func TestClientCheck_accepts_only_authenticated_service_health_contract(t *testing.T) {
	// Given
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodGet; got != want {
			t.Errorf("method = %q, want %q", got, want)
		}
		if got, want := r.URL.Path, "/api/v1/internal/acr/health"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer "+testToken; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"schema_version":"acr_service_health.v1","service":"dev-health-ops","status":"ok"}`))
	}))
	defer server.Close()
	client := newClient(t, server, entitlements.Config{})

	// When
	err := client.Check(context.Background())

	// Then
	if err != nil {
		t.Fatalf("Check() error = %v, want nil", err)
	}
}

func TestClientCheck_fails_closed_for_non_exact_health_contract(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":"wrong.v1","service":"dev-health-ops","status":"ok"}`,
		`{"schema_version":"acr_service_health.v1","service":"other","status":"ok"}`,
		`{"schema_version":"acr_service_health.v1","service":"dev-health-ops","status":"not_ok"}`,
		`{"schema_version":"acr_service_health.v1","service":"dev-health-ops","status":"ok","extra":true}`,
		`{"schema_version":"acr_service_health.v1","service":"dev-health-ops","status":"ok"}{}`,
		`{"schema_version":null,"service":"dev-health-ops","status":"ok"}`,
	} {
		t.Run(body, func(t *testing.T) {
			// Given
			server := newTLSServer(t, http.StatusOK, body)
			client := newClient(t, server, entitlements.Config{})

			// When
			err := client.Check(context.Background())

			// Then
			if err == nil {
				t.Fatal("Check() error = nil; want fail-closed contract rejection")
			}
			assertSecretSafe(t, err, testToken, body)
		})
	}
}

func TestClientHasEntitlement_fails_closed_for_untrusted_responses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"wrong org", http.StatusOK, `{"schema_version":"acr_entitlement.v1","org_id":"other","agent_context_runtime":true}`},
		{"wrong schema", http.StatusOK, `{"schema_version":"unknown.v1","org_id":"org-1","agent_context_runtime":true}`},
		{"unknown field", http.StatusOK, `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"extra":true}`},
		{"trailing JSON", http.StatusOK, `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true} {}`},
		{"malformed JSON", http.StatusOK, `{`},
		{"unauthorized", http.StatusUnauthorized, `secret response`},
		{"forbidden", http.StatusForbidden, `secret response`},
		{"not found", http.StatusNotFound, `secret response`},
		{"rate limited", http.StatusTooManyRequests, `secret response`},
		{"upstream failure", http.StatusBadGateway, `secret response`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			server := newTLSServer(t, test.status, test.body)
			client := newClient(t, server, entitlements.Config{})

			// When
			entitled, err := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime")

			// Then
			if err == nil || entitled {
				t.Fatalf("HasEntitlement() = %t, %v; want false and safe error", entitled, err)
			}
			assertSecretSafe(t, err, testToken, test.body)
		})
	}
}

func TestClientHasEntitlement_fails_closed_for_timeout_redirect_and_oversized_body(t *testing.T) {
	for _, test := range []struct {
		name    string
		cfg     entitlements.Config
		handler func(chan<- struct{}) http.Handler
	}{
		{"stalled response", entitlements.Config{Timeout: time.Second}, func(started chan<- struct{}) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })
		}},
		{"redirect", entitlements.Config{}, func(chan<- struct{}) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/target", http.StatusFound) })
		}},
		{"oversized body", entitlements.Config{MaxResponseBytes: 1024}, func(chan<- struct{}) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 4096))) })
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given
			started := make(chan struct{})
			server := httptest.NewTLSServer(test.handler(started))
			defer server.Close()
			client := newClient(t, server, test.cfg)

			// When
			_, err := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime")

			// Then
			if err == nil {
				t.Fatal("HasEntitlement() error = nil; want fail closed")
			}
			assertSecretSafe(t, err, testToken)
			if test.name == "stalled response" {
				select {
				case <-started:
				default:
					t.Fatal("stalled request did not reach the TLS fixture")
				}
			}
		})
	}
}

func TestClientHasEntitlement_does_not_send_request_or_bearer_token_to_redirect_target(t *testing.T) {
	// Given
	var targetCalls int
	var targetAuthorization string
	target := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		targetCalls++
		targetAuthorization = r.Header.Get("Authorization")
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/redirect-target", http.StatusFound)
	}))
	defer source.Close()
	client := newClient(t, source, entitlements.Config{})

	// When
	_, err := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime")

	// Then
	if err == nil {
		t.Fatal("HasEntitlement() error = nil; want redirect rejection")
	}
	if targetCalls != 0 || targetAuthorization != "" {
		t.Fatalf("redirect target calls = %d, Authorization = %q; want no request and no bearer token", targetCalls, targetAuthorization)
	}
}

func TestClientNew_enforces_transport_file_and_cache_boundaries(t *testing.T) {
	// Given
	server := newTLSServer(t, http.StatusOK, `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true}`)
	base := clientConfig(t, server, entitlements.Config{})

	for _, test := range []struct {
		name string
		cfg  entitlements.Config
	}{
		{"rejects unsupported URL scheme", entitlements.Config{BaseURL: mustURL(t, "ftp://example.invalid")}},
		{"rejects unsupported proxy", entitlements.Config{ProxyURL: mustURL(t, "socks5://127.0.0.1:8080")}},
		{"rejects missing token file", entitlements.Config{TokenFile: filepath.Join(t.TempDir(), "missing")}},
		{"rejects token with newline", entitlements.Config{TokenFile: writeToken(t, "bad\nshape\n")}},
		{"rejects too-short cache TTL", entitlements.Config{PositiveCacheTTL: 24 * time.Hour}},
		{"rejects oversized cache", entitlements.Config{CacheCapacity: 1025}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// When
			_, err := entitlements.New(mergeConfig(base, test.cfg))

			// Then
			if err == nil {
				t.Fatal("New() error = nil; want fail closed")
			}
			assertSecretSafe(t, err, testToken)
		})
	}
}

func TestClient_accepts_plain_HTTP_for_private_service_origin(t *testing.T) {
	// Given
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer "+testToken; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true}`))
	}))
	defer server.Close()
	client, err := entitlements.New(entitlements.Config{
		BaseURL:          mustURL(t, server.URL),
		TokenFile:        writeToken(t, testToken),
		Timeout:          time.Second,
		MaxResponseBytes: 2048,
		PositiveCacheTTL: time.Minute,
		NegativeCacheTTL: time.Second,
		CacheCapacity:    2,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// When
	entitled, err := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime")

	// Then
	if err != nil || !entitled {
		t.Fatalf("HasEntitlement() = %t, %v; want true, nil", entitled, err)
	}
}

func TestClientNew_rejects_custom_ca_bundle_with_permissive_permissions(t *testing.T) {
	// Given
	server := newTLSServer(t, http.StatusOK, `{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true}`)
	config := clientConfig(t, server, entitlements.Config{})
	if err := os.Chmod(config.CACertPath, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	// When
	_, err := entitlements.New(config)

	// Then
	if err == nil {
		t.Fatal("New() error = nil; want custom CA permission rejection")
	}
	assertSecretSafe(t, err, testToken, config.CACertPath)
}

func newTLSServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer "+testToken; got != want {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func newClient(t *testing.T, server *httptest.Server, overrides entitlements.Config) *entitlements.Client {
	t.Helper()
	client, err := entitlements.New(mergeConfig(clientConfig(t, server, entitlements.Config{}), overrides))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var _ api.EntitlementProvider = client
	return client
}

func assertSecretSafe(t *testing.T, err error, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q leaked secret %q", err, secret)
		}
	}
}
