package mcp_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
)

// gateStatus posts one tools/list with the given bearer and X-Forwarded-For
// (either may be empty) and returns the HTTP status.
func gateStatus(t *testing.T, e *endpoint, bearer, xff string) int {
	t.Helper()
	header := http.Header{}
	if bearer != "" {
		header.Set("Authorization", "Bearer "+bearer)
	}
	if xff != "" {
		header.Set("X-Forwarded-For", xff)
	}
	resp := postMCP(t, e, http.MethodPost, rawToolsList(), header)
	return resp.StatusCode
}

// burst sends n requests and returns how many got each status.
func burst(t *testing.T, e *endpoint, n int, bearer func(i int) string, xff func(i int) string) map[int]int {
	t.Helper()
	counts := map[int]int{}
	for i := 0; i < n; i++ {
		counts[gateStatus(t, e, bearer(i), xff(i))]++
	}
	return counts
}

func unknownWellFormedToken(t *testing.T) string {
	t.Helper()
	token, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func noXFF(int) string { return "" }

func TestEdgeGateMalformedBearersAreCountedAndRefused(t *testing.T) {
	e := newEndpoint(t, newHostedAPI(t))
	got := burst(t, e, 25, func(int) string { return "not-an-acr-bearer-token" }, noXFF)
	if got[http.StatusUnauthorized] != 20 || got[http.StatusTooManyRequests] != 5 {
		t.Fatalf("malformed x25 = %v, want 20 x 401 then 5 x 429", got)
	}
}

func TestEdgeGateMissingBearersAreCountedAndRefused(t *testing.T) {
	e := newEndpoint(t, newHostedAPI(t))
	got := burst(t, e, 25, func(int) string { return "" }, noXFF)
	if got[http.StatusUnauthorized] != 20 || got[http.StatusTooManyRequests] != 5 {
		t.Fatalf("missing x25 = %v, want 20 x 401 then 5 x 429", got)
	}
}

func TestEdgeGateWellFormedUnknownBearersAreCountedAndRefused(t *testing.T) {
	e := newEndpoint(t, newHostedAPI(t))
	got := burst(t, e, 25, func(int) string { return unknownWellFormedToken(t) }, noXFF)
	if got[http.StatusUnauthorized] != 20 || got[http.StatusTooManyRequests] != 5 {
		t.Fatalf("unknown x25 = %v, want 20 x 401 then 5 x 429", got)
	}
}

// The failure is counted at the edge: the refusal at request 21 never reaches
// acr-api, so the fixture API saw exactly 20 capability calls.
func TestEdgeGateRefusalDoesNotReachTheHostedAPI(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpoint(t, hosted)
	burst(t, e, 25, func(int) string { return unknownWellFormedToken(t) }, noXFF)
	if n := len(hosted.forwardedFor()); n != 20 {
		t.Fatalf("hosted API saw %d calls, want 20", n)
	}
}

// Two callers behind a trusted proxy: A's failures must not block valid B,
// either at the edge (bucket per forwarded address) or at acr-api (the
// forwarded address keys acr-api's own bucket). The edge limit is raised so
// the acr-api gate is the one observed.
func TestTwoAddressesHaveSeparateBucketsThroughTheMCPHop(t *testing.T) {
	limiter := auth.NewBoundedMemoryLimiter(auth.MemoryLimiterOptions{Window: time.Minute, FailureLimit: 20, MaxTrackedKeys: 64})
	hosted := newHostedAPIWith(t, hostedOptions{Limiter: limiter, TrustedProxyCIDRs: []string{"127.0.0.0/8", "::1/128"}})
	e := newEndpointWithGate(t, hosted, acrmcp.EdgeGateOptions{FailureLimit: 1000, TrustedProxyCIDRs: []string{"127.0.0.0/8", "::1/128"}})
	valid := hosted.issue(readScopes, []string{repoPlain}, nil)

	a, b := "203.0.113.10", "203.0.113.11"
	got := burst(t, e, 25, func(int) string { return unknownWellFormedToken(t) }, func(int) string { return a })
	if got[http.StatusUnauthorized] != 20 || got[http.StatusTooManyRequests] != 5 {
		t.Fatalf("A unknown x25 = %v, want 20 x 401 then 5 x 429 (acr-api gate keyed on A)", got)
	}
	if status := gateStatus(t, e, valid.token, a); status != http.StatusTooManyRequests {
		t.Fatalf("valid token from locked-out A = %d, want 429", status)
	}
	if status := gateStatus(t, e, valid.token, b); status != http.StatusOK {
		t.Fatalf("valid token from B = %d, want 200: A's failures must not block B", status)
	}
	for _, seen := range hosted.forwardedFor() {
		if seen != a && seen != b {
			t.Fatalf("acr-api saw X-Forwarded-For %q, want only the resolved caller addresses", seen)
		}
	}
}

func TestEdgeGateTwoAddressesHaveSeparateEdgeBuckets(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpointWithGate(t, hosted, acrmcp.EdgeGateOptions{TrustedProxyCIDRs: []string{"127.0.0.0/8", "::1/128"}})
	valid := hosted.issue(readScopes, []string{repoPlain}, nil)
	a, b := "203.0.113.20", "203.0.113.21"
	got := burst(t, e, 25, func(int) string { return "junk" }, func(int) string { return a })
	if got[http.StatusTooManyRequests] != 5 {
		t.Fatalf("A malformed x25 = %v, want 5 x 429", got)
	}
	if status := gateStatus(t, e, valid.token, b); status != http.StatusOK {
		t.Fatalf("valid token from B = %d, want 200", status)
	}
}

// X-Forwarded-For from a peer that is not a trusted proxy is ignored: a
// caller cannot mint fresh buckets by varying the header.
func TestEdgeGateIgnoresForwardedForFromUntrustedPeer(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpointWithGate(t, hosted, acrmcp.EdgeGateOptions{TrustedProxyCIDRs: []string{"10.0.0.0/8"}})
	got := burst(t, e, 25, func(int) string { return "junk" }, func(i int) string { return "198.51.100." + string(rune('0'+i%10)) })
	if got[http.StatusUnauthorized] != 20 || got[http.StatusTooManyRequests] != 5 {
		t.Fatalf("spoofed XFF x25 = %v, want the peer bucket to refuse after 20", got)
	}
}

func TestEdgeGateWithNoTrustedProxiesIgnoresForwardedFor(t *testing.T) {
	e := newEndpoint(t, newHostedAPI(t))
	got := burst(t, e, 25, func(int) string { return "junk" }, func(i int) string { return "198.51.100." + string(rune('0'+i%10)) })
	if got[http.StatusTooManyRequests] != 5 {
		t.Fatalf("XFF with no trusted proxies = %v, want 5 x 429", got)
	}
}

// acr-mcp states the resolved caller, never the caller's own header: a
// forged inbound X-Forwarded-For chain is replaced, not relayed.
func TestForwardedClientIsTheResolvedAddressNotTheInboundChain(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpointWithGate(t, hosted, acrmcp.EdgeGateOptions{TrustedProxyCIDRs: []string{"127.0.0.0/8", "::1/128"}})
	valid := hosted.issue(readScopes, []string{repoPlain}, nil)
	if status := gateStatus(t, e, valid.token, "6.6.6.6, 203.0.113.30"); status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	seen := hosted.forwardedFor()
	if len(seen) != 1 || seen[0] != "203.0.113.30" {
		t.Fatalf("acr-api saw X-Forwarded-For %v, want [203.0.113.30]", seen)
	}
}

// A success does not consume the budget: many valid requests from one
// address are never refused.
func TestEdgeGateValidRequestsDoNotConsumeTheBudget(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpoint(t, hosted)
	valid := hosted.issue(readScopes, []string{repoPlain}, nil)
	for i := 0; i < 60; i++ {
		if status := gateStatus(t, e, valid.token, ""); status != http.StatusOK {
			t.Fatalf("valid request %d = %d, want 200", i, status)
		}
	}
}

// countingLimiter records reservations still open.
type countingLimiter struct {
	auth.AttemptLimiter
	mu   sync.Mutex
	open int
	seen []int
}

func (c *countingLimiter) BeginAttempt(key string, now time.Time) (func(), bool) {
	release, ok := c.AttemptLimiter.BeginAttempt(key, now)
	if !ok {
		return release, ok
	}
	c.mu.Lock()
	c.open++
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			c.open--
			c.mu.Unlock()
			release()
		})
	}, true
}

// The in-flight reservation is released when the credential is decided, not
// when the response ends: at the moment the MCP handler runs it is gone.
func TestEdgeGateReleasesTheReservationBeforeTheMCPHandlerRuns(t *testing.T) {
	hosted := newHostedAPI(t)
	limiter := &countingLimiter{AttemptLimiter: auth.NewBoundedMemoryLimiter(auth.MemoryLimiterOptions{Window: time.Minute, FailureLimit: 20, MaxTrackedKeys: 64})}
	e := newEndpointWithGate(t, hosted, acrmcp.EdgeGateOptions{Limiter: limiter})
	acrmcp.WrapHTTPHandlerSDKForTest(e.handler, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limiter.mu.Lock()
			limiter.seen = append(limiter.seen, limiter.open)
			limiter.mu.Unlock()
			next.ServeHTTP(w, r)
		})
	})
	valid := hosted.issue(readScopes, []string{repoPlain}, nil)
	if status := gateStatus(t, e, valid.token, ""); status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if len(limiter.seen) != 1 || limiter.seen[0] != 0 {
		t.Fatalf("open reservations at handler time = %v, want [0]", limiter.seen)
	}
	if limiter.open != 0 {
		t.Fatalf("open reservations after the request = %d", limiter.open)
	}
}

// Every refusal is one Info line naming the address and the bound.
func TestEdgeGateLogsTheRefusalDecisionAndAddress(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpointWithGate(t, hosted, acrmcp.EdgeGateOptions{TrustedProxyCIDRs: []string{"127.0.0.0/8", "::1/128"}})
	burst(t, e, 21, func(int) string { return "junk" }, func(int) string { return "203.0.113.40" })
	var last map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(e.logs.Bytes()), []byte("\n")) {
		var entry map[string]any
		if json.Unmarshal(line, &entry) == nil && entry["msg"] == acrmcp.HTTPRequestLogMessage {
			last = entry
		}
	}
	if last == nil {
		t.Fatalf("no request line in %s", e.logs.Bytes())
	}
	if last["gate_decision"] != "failure_budget" || last["client_ip"] != "203.0.113.40" || last["auth_outcome"] != "rate_limited" || last["level"] != "INFO" {
		t.Fatalf("request line = %v", last)
	}
}

func TestEdgeGateWarnsAtStartupWhenNoProxyIsTrusted(t *testing.T) {
	e := newEndpoint(t, newHostedAPI(t))
	if !strings.Contains(string(e.logs.Bytes()), "edge gate keys on the peer address") {
		t.Fatalf("no startup warning in %s", e.logs.Bytes())
	}
}
