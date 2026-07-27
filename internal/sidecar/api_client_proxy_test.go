package sidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestNewClient_rejects_explicit_proxy_when_API_is_insecure_loopback(t *testing.T) {
	// Given
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer proxy.Close()
	cfg := newFixtureConfig(t, target)
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProxyURL = proxyURL

	// When
	_, err = NewClient(cfg, fixedCredentialSource(testBearerCanary))

	// Then
	if err == nil {
		t.Fatal("an explicit proxy was accepted for an insecure loopback API URL")
	}
}

func TestTransport_bypasses_all_proxies_when_API_is_insecure_loopback(t *testing.T) {
	// Given
	var targetCalls, proxyCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer "+testBearerCanary {
			t.Errorf("direct request authorization = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	var proxyAuthorization string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		proxyAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()
	cfg := newFixtureConfig(t, target)
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProxyURL = proxyURL
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	transport, err := buildTransport(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if transport.Proxy != nil {
		t.Fatal("insecure loopback transport retained ambient proxy resolution")
	}

	// When
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testBearerCanary)
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then
	if proxyCalls.Load() != 0 {
		t.Fatalf("proxy received %d request(s), including authorization %q", proxyCalls.Load(), proxyAuthorization)
	}
	if targetCalls.Load() != 1 {
		t.Fatalf("direct loopback target received %d request(s)", targetCalls.Load())
	}
}

func TestTransport_uses_explicit_proxy_when_API_is_HTTPS(t *testing.T) {
	// Given
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	proxyURL, err := url.Parse("http://proxy.example.test:3128")
	if err != nil {
		t.Fatal(err)
	}
	cfg := newFixtureConfig(t, target)
	cfg.ProxyURL = proxyURL

	// When
	transport, err := buildTransport(cfg)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, target.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := transport.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if got == nil || got.String() != proxyURL.String() {
		t.Fatalf("proxy = %v, want %v", got, proxyURL)
	}
}
