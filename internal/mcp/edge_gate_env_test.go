package mcp_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
)

func envLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) { v, ok := values[name]; return v, ok }
}

func TestServeEnvironmentEdgeGateSettings(t *testing.T) {
	opts, err := acrmcp.ServeOptionsFromEnvironment(envLookup(map[string]string{
		"ACR_MCP_TRUSTED_PROXY_CIDRS":  " 10.42.0.6/32 , ,10.42.0.1/32 ",
		"ACR_AUTH_FAILURES_PER_WINDOW": "7", "ACR_AUTH_MAX_TRACKED_KEYS": "9", "ACR_AUTH_MAX_IN_FLIGHT": "3",
		"ACR_LIMIT_WINDOW": "2m", "ACR_AUTH_LIMIT_WINDOW": "30s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if opts.AuthFailures != 7 || opts.AuthTrackedKeys != 9 || opts.AuthMaxInFlight != 3 {
		t.Fatalf("limits = %+v", opts)
	}
	if opts.AuthWindow != 30*time.Second {
		t.Fatalf("window = %v, want ACR_AUTH_LIMIT_WINDOW (30s) to win over ACR_LIMIT_WINDOW", opts.AuthWindow)
	}
	if err := opts.Validate(); err != nil {
		t.Fatal(err)
	}
	opts, _ = acrmcp.ServeOptionsFromEnvironment(envLookup(map[string]string{"ACR_LIMIT_WINDOW": "2m"}))
	if opts.AuthWindow != 2*time.Minute {
		t.Fatalf("window = %v, want the ACR_LIMIT_WINDOW fallback", opts.AuthWindow)
	}
	blank, err := acrmcp.ServeOptionsFromEnvironment(envLookup(map[string]string{"ACR_MCP_TRUSTED_PROXY_CIDRS": "  ", "ACR_AUTH_FAILURES_PER_WINDOW": ""}))
	if err != nil || blank.TrustedProxyCIDRs != "" || blank.AuthFailures != 0 {
		t.Fatalf("blank values = %+v, %v; want the defaults", blank, err)
	}
}

// A bad limit is refused at startup naming the setting; it is never silently
// replaced by a default.
func TestServeEnvironmentRefusesBadEdgeGateSettings(t *testing.T) {
	for name, value := range map[string]string{
		"ACR_AUTH_FAILURES_PER_WINDOW": "0", "ACR_AUTH_MAX_TRACKED_KEYS": "-1", "ACR_AUTH_MAX_IN_FLIGHT": "abc",
		"ACR_AUTH_LIMIT_WINDOW": "0s", "ACR_LIMIT_WINDOW": "-5s",
	} {
		_, err := acrmcp.ServeOptionsFromEnvironment(envLookup(map[string]string{name: value}))
		var invalid *acrmcp.ErrServeOptionInvalid
		if !errors.As(err, &invalid) || invalid.Setting != name {
			t.Errorf("%s=%q: err = %v, want ErrServeOptionInvalid naming the setting", name, value, err)
		}
	}
}

func TestServeOptionsRefuseInvalidTrustedProxyCIDRs(t *testing.T) {
	for _, value := range []string{"not-a-cidr", "10.0.0.1", "10.0.0.0/33", "10.42.0.6/32,junk"} {
		opts, err := acrmcp.ServeOptionsFromEnvironment(envLookup(map[string]string{"ACR_MCP_TRUSTED_PROXY_CIDRS": value}))
		if err != nil {
			t.Fatalf("%q: env parse: %v", value, err)
		}
		var invalid *acrmcp.ErrServeOptionInvalid
		if err := opts.Validate(); !errors.As(err, &invalid) || invalid.Setting != "ACR_MCP_TRUSTED_PROXY_CIDRS" {
			t.Errorf("%q: Validate = %v, want ErrServeOptionInvalid naming ACR_MCP_TRUSTED_PROXY_CIDRS", value, err)
		}
	}
}

// The environment values reach the running gate: a limit of 3 refuses the
// fourth failure, and the trusted list separates two forwarded callers.
func TestServeEnvironmentSizesTheRunningEdgeGate(t *testing.T) {
	hosted := newHostedAPI(t)
	serve := acrmcp.DefaultServeOptions()
	serve.Transport = acrmcp.TransportHTTP
	serve.AuthFailures = 3
	serve.TrustedProxyCIDRs = "127.0.0.0/8,::1/128"
	handler, err := acrmcp.NewServeHTTPHandler(hosted.sidecarConfig(), testIdentity, io.Discard, serve)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	e := &endpoint{handler: handler, server: server, logs: &syncBuffer{}}
	got := burst(t, e, 5, func(int) string { return "junk" }, func(int) string { return "203.0.113.70" })
	if got[http.StatusUnauthorized] != 3 || got[http.StatusTooManyRequests] != 2 {
		t.Fatalf("limit 3 x5 = %v, want 3 x 401 then 2 x 429", got)
	}
	if status := gateStatus(t, e, "junk", "203.0.113.71"); status != http.StatusUnauthorized {
		t.Fatalf("second forwarded address = %d, want its own bucket (401)", status)
	}
}
