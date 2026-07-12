package sidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestNewClientClonesAPIBaseURLAgainstLaterCallerMutation proves a Client's
// origin is fixed at construction time: mutating the *url.URL the caller
// passed inside Config, after NewClient has already validated and started
// using it, must not change which origin the running Client talks to.
func TestNewClientClonesAPIBaseURLAgainstLaterCallerMutation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONFixture(t, w, http.StatusOK, validCapabilitiesFixture())
	}))
	defer server.Close()
	cfg := newFixtureConfig(t, server)
	originalHost := cfg.APIBaseURL.Host
	client, err := NewClient(cfg, fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	if client.baseURL == cfg.APIBaseURL {
		t.Fatal("Client.baseURL aliases the caller's Config.APIBaseURL pointer")
	}
	cfg.APIBaseURL.Host = "evil.invalid:1"
	if client.baseURL.Host != originalHost {
		t.Fatalf("mutating the caller's Config.APIBaseURL changed the Client's origin: got %q, want %q", client.baseURL.Host, originalHost)
	}
	if _, err := client.Capabilities(context.Background()); err != nil {
		t.Fatalf("client no longer reached the original origin after caller mutation: %v", err)
	}
}

// TestNewClientClonesProxyURLAgainstLaterCallerMutation is the same proof
// as above for ProxyURL: buildTransport's proxy closure must resolve to a
// clone, not the caller's own *url.URL, or a later mutation of that URL
// would silently redirect proxied traffic after the Client was validated.
func TestNewClientClonesProxyURLAgainstLaterCallerMutation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg := newFixtureConfig(t, server)
	proxyURL, err := url.Parse("http://proxy.internal:3128")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProxyURL = proxyURL
	client, err := NewClient(cfg, fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	if client.cfg.ProxyURL == proxyURL {
		t.Fatal("Client retained the caller's ProxyURL pointer")
	}
	originalProxyHost := client.cfg.ProxyURL.Host
	proxyURL.Host = "evil.invalid:1"
	transport, ok := client.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type: %T", client.http.Transport)
	}
	req, err := http.NewRequest(http.MethodGet, "https://acr.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := transport.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Host != originalProxyHost {
		t.Fatalf("mutating the caller's ProxyURL changed the Client's resolved proxy: got %q, want %q", resolved.Host, originalProxyHost)
	}
}
