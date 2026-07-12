package sidecar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestClientEvidenceRejectsEmptyIDWithoutNetworkCall(t *testing.T) {
	base, err := url.Parse("https://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{
		baseURL:    base,
		cfg:        Config{APIBaseURL: base, Timeout: 5 * time.Second, MaxRequestBodyBytes: defaultMaxRequestBodyBytes, MaxResponseBytes: defaultMaxResponseBytes},
		credential: fixedCredentialSource(testBearerCanary),
		http:       http.DefaultClient,
	}
	start := time.Now()
	if _, err := client.Evidence(context.Background(), "   "); err != errEmptyEvidenceReferenceID {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("empty evidence id validation attempted a network call: took %s", elapsed)
	}
}

func TestClientRequestTooLargeNeverReachesNetwork(t *testing.T) {
	var handlerCalls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg := newFixtureConfig(t, server)
	cfg.MaxRequestBodyBytes = minRequestBodyBytes
	client, err := NewClient(cfg, fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	huge := make([]string, 0, 200)
	for i := range 200 {
		huge = append(huge, strings.Repeat("x", 2000)+strings.Repeat("y", i%10+1))
	}
	request := validContextPacketRequest()
	request.Scope.Files = huge[:1] // one file is enough once MaxRequestBodyBytes is tiny
	if _, err := client.ContextPacket(context.Background(), request); err == nil || !strings.Contains(err.Error(), "exceeds the configured limit") {
		t.Fatalf("expected a request-too-large error, got %v", err)
	}
	if handlerCalls != 0 {
		t.Fatalf("server should never have been contacted, got %d calls", handlerCalls)
	}
}

func TestClientContextCancellationIsReported(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Capabilities(ctx); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestClientTimeoutIsReported(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	// Bypass Config.Validate's minimum (production floor for real network
	// jitter) so this test observes a real deadline without a slow test run.
	client.cfg.Timeout = 10 * time.Millisecond
	if _, err := client.Capabilities(context.Background()); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
}
