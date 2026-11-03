package mcp_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if err != nil || blank.TrustedProxyCIDRs != "" || blank.AuthFailures != 20 || blank.AuthWindow != time.Minute {
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
		if !errors.As(err, &invalid) || invalid.Setting != acrmcp.AuthLimitsSetting {
			t.Errorf("%s=%q: err = %v, want ErrServeOptionInvalid naming %s", name, value, err, acrmcp.AuthLimitsSetting)
		}
	}
}

// A handler built with no explicit gate settings takes the shared loader's
// resolution of the process environment: ACR_REQUESTS_PER_MINUTE=5 lowers the
// edge limit to 5 exactly as it lowers acr-api's (CHAOS-7201 r2).
func TestEdgeGateDefaultFollowsTheSharedLimitsLoader(t *testing.T) {
	t.Setenv("ACR_REQUESTS_PER_MINUTE", "5")
	e := newEndpoint(t, newHostedAPI(t))
	got := burst(t, e, 8, func(int) string { return "junk" }, noXFF)
	if got[http.StatusUnauthorized] != 5 || got[http.StatusTooManyRequests] != 3 {
		t.Fatalf("ACR_REQUESTS_PER_MINUTE=5 x8 = %v, want 5 x 401 then 3 x 429", got)
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

// CHAOS-11734: acr-mcp accepts the node exclusions go-api reads, and refuses a
// bad exclusion at start.
func TestServeOptionsAcceptTrustedProxyExclusions(t *testing.T) {
	opts, err := acrmcp.ServeOptionsFromEnvironment(envLookup(map[string]string{"ACR_MCP_TRUSTED_PROXY_CIDRS": "10.42.0.0/24,!10.42.0.0,!10.42.0.1"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := opts.Validate(); err != nil {
		t.Fatalf("Validate = %v, want the exclusion list accepted", err)
	}
	for _, value := range []string{"10.42.0.0/24,!192.0.2.1", "10.42.0.0/24,!10.42.0.0/24"} {
		bad, err := acrmcp.ServeOptionsFromEnvironment(envLookup(map[string]string{"ACR_MCP_TRUSTED_PROXY_CIDRS": value}))
		if err != nil {
			t.Fatalf("%q: env parse: %v", value, err)
		}
		var invalid *acrmcp.ErrServeOptionInvalid
		if err := bad.Validate(); !errors.As(err, &invalid) || invalid.Setting != "ACR_MCP_TRUSTED_PROXY_CIDRS" {
			t.Errorf("%q: Validate = %v, want ErrServeOptionInvalid naming ACR_MCP_TRUSTED_PROXY_CIDRS", value, err)
		}
	}
}

// CHAOS-11734 r1: Validate's refusal carries the parser's diagnostic, which
// names the entry position (entry 2 in every form) and never the value.
func TestServeOptionsTrustedProxyRefusalNamesTheEntryNotTheValue(t *testing.T) {
	for _, c := range []struct{ form, list, address string }{
		{"outside network", "10.42.0.0/24,!192.0.2.1", "192.0.2.1"},
		{"network exclusion", "10.42.0.0/24,!10.42.0.0/24", "10.42.0.0"},
		{"empty exclusion", "10.42.0.0/24,!", ""},
		{"not an address", "10.42.0.0/24,!junkhost", "junkhost"},
		{"zoned address", "fe80::/64,!fe80::1%eth0", "fe80::1"},
		{"IPv4-mapped address", "10.42.0.0/24,!::ffff:10.42.0.1", "10.42.0.1"},
		{"not a network", "10.42.0.0/24,junknet", "junknet"},
	} {
		opts, err := acrmcp.ServeOptionsFromEnvironment(envLookup(map[string]string{"ACR_MCP_TRUSTED_PROXY_CIDRS": c.list}))
		if err != nil {
			t.Fatalf("%s: env parse: %v", c.form, err)
		}
		err = opts.Validate()
		var invalid *acrmcp.ErrServeOptionInvalid
		if !errors.As(err, &invalid) || invalid.Setting != "ACR_MCP_TRUSTED_PROXY_CIDRS" {
			t.Errorf("%s: Validate = %v, want ErrServeOptionInvalid naming ACR_MCP_TRUSTED_PROXY_CIDRS", c.form, err)
			continue
		}
		if !strings.Contains(err.Error(), "entry 2 ") {
			t.Errorf("%s: error %q does not name entry 2", c.form, err)
		}
		if c.address != "" && strings.Contains(err.Error(), c.address) {
			t.Errorf("%s: error %q names the value %q", c.form, err, c.address)
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
