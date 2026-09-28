package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-7073: the sidecar client for the direct facts route.

func TestReadFactsPostsTheBodyUnchangedAndReturnsTheRawResponse(t *testing.T) {
	const requestBody = `{"kinds":["health"],"subjects":[{"kind":"team","canonical_id":"team-a"}],"max_bytes":4096}`
	const responseBody = `{"contract_version":"acr-data.v1","tool":"read_facts","big":"9007199254740993"}`
	var gotMethod, gotPath, gotAuth, gotBody, gotType string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotMethod, gotPath, gotAuth, gotBody, gotType = r.Method, r.URL.Path, r.Header.Get("Authorization"), string(raw), r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.ReadDirectFacts(context.Background(), json.RawMessage(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/context-fabric/data/facts" || gotBody != requestBody || gotType != "application/json" {
		t.Fatalf("request = %s %s %q (%s)", gotMethod, gotPath, gotBody, gotType)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Fatalf("no bearer sent: %q", gotAuth)
	}
	if string(response) != responseBody {
		t.Fatalf("response rewritten: %s", response)
	}
}

func TestReadFactsMapsHostedErrorsToTypedSidecarErrors(t *testing.T) {
	cases := []struct {
		status    int
		code      string
		retryable bool
		want      error
	}{
		{http.StatusBadRequest, "invalid_request", false, ErrInvalidRequest},
		{http.StatusServiceUnavailable, "upstream_unavailable", true, ErrUpstreamUnavailable},
		{http.StatusForbidden, "insufficient_scope", false, ErrInsufficientScope},
		{http.StatusForbidden, "feature_not_enabled", false, ErrFeatureNotEnabled},
		{http.StatusTooManyRequests, "rate_limited", true, ErrRateLimited},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(contractsv1.ErrorEnvelope{
					SchemaVersion: contractsv1.ErrorSchema, RequestID: "req_1",
					Error: contractsv1.ErrorDetail{Code: tc.code, Message: "m", HTTPStatus: tc.status, Retryable: tc.retryable},
				})
			}))
			defer server.Close()
			client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.ReadDirectFacts(context.Background(), json.RawMessage(`{}`))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestReadFactsRefusesLocallyWhatCannotBeSent(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	cfg := newFixtureConfig(t, server)
	client, err := NewClient(cfg, fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadDirectFacts(context.Background(), json.RawMessage(`{not json`)); err == nil {
		t.Fatal("invalid JSON must be refused before any call")
	}
	huge := json.RawMessage(`{"pad":"` + strings.Repeat("x", int(cfg.MaxRequestBodyBytes)+1) + `"}`)
	if _, err := client.ReadDirectFacts(context.Background(), huge); !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("oversized body error = %v, want ErrRequestTooLarge", err)
	}
	if calls != 0 {
		t.Fatalf("hosted was called %d times", calls)
	}
}

func TestReadFactsRefusesAMalformedResponse(t *testing.T) {
	for name, body := range map[string]string{"trailing json": `{"a":1}{"b":2}`, "not json": `<html>`} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.ReadDirectFacts(context.Background(), json.RawMessage(`{}`)); err == nil {
				t.Fatal("a malformed response reached the caller")
			}
		})
	}
}
