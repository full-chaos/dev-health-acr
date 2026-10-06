package mcp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
)

var loopbackProxies = []string{"127.0.0.0/8", "::1/128"}

// sharedAddressStack is acr-mcp in front of a fixture acr-api that runs the
// real per-address limiter, both trusting the loopback proxy, so callers are
// keyed on their forwarded address at both gates.
func sharedAddressStack(t *testing.T) (*endpoint, *hostedAPI) {
	t.Helper()
	limiter := auth.NewBoundedMemoryLimiter(auth.MemoryLimiterOptions{Window: time.Minute, FailureLimit: 20, MaxTrackedKeys: 64})
	hosted := newHostedAPIWith(t, hostedOptions{Limiter: limiter, TrustedProxyCIDRs: loopbackProxies})
	return newEndpointWithGate(t, hosted, acrmcp.EdgeGateOptions{TrustedProxyCIDRs: loopbackProxies}), hosted
}

func TestValidBearerAfterNoCredentialRequestsFromItsAddressIsServed(t *testing.T) {
	e, hosted := sharedAddressStack(t)
	valid := hosted.issue(readScopes, []string{repoPlain}, nil)
	const address = "203.0.113.40"
	burst(t, e, 25, func(int) string { return "" }, func(int) string { return address })
	if status := gateStatus(t, e, valid.token, address); status != http.StatusOK {
		t.Fatalf("valid bearer after 25 no-credential requests from its address = %d, want 200", status)
	}
}

func TestValidBearerDuringRejectedCredentialFloodFromItsAddressIsServed(t *testing.T) {
	expiredAt := time.Now().Add(-time.Hour)
	for name, rejected := range map[string]func(*hostedAPI) string{
		"malformed": func(*hostedAPI) string { return "not-an-acr-bearer-token" },
		"unknown":   func(*hostedAPI) string { return unknownWellFormedToken(t) },
		"revoked": func(h *hostedAPI) string {
			c := h.issue(readScopes, []string{repoPlain}, nil)
			h.revoke(c)
			return c.token
		},
		"expired": func(h *hostedAPI) string { return h.issue(readScopes, []string{repoPlain}, &expiredAt).token },
	} {
		t.Run(name, func(t *testing.T) {
			e, hosted := sharedAddressStack(t)
			valid := hosted.issue(readScopes, []string{repoPlain}, nil)
			const address = "203.0.113.41"
			bad := rejected(hosted)
			burst(t, e, 25, func(int) string { return bad }, func(int) string { return address })
			if status := gateStatus(t, e, valid.token, address); status != http.StatusOK {
				t.Fatalf("valid bearer after 25 %s credentials from its address = %d, want 200", name, status)
			}
		})
	}
}

func TestNoCredentialRequestGetsTheDiscoveryChallengeWhileTheAddressIsOverBudget(t *testing.T) {
	e, _ := sharedAddressStack(t)
	const address = "203.0.113.42"
	first := noCredentialResponse(t, e, address)
	burst(t, e, 25, func(int) string { return "junk" }, func(int) string { return address })
	later := noCredentialResponse(t, e, address)
	if first.status != http.StatusUnauthorized || first.header.Get("WWW-Authenticate") == "" {
		t.Fatalf("no-credential request = %d %q, want 401 with a challenge", first.status, first.header.Get("WWW-Authenticate"))
	}
	if !first.equal(later) {
		t.Fatalf("no-credential answer while over budget = %+v, want %+v", later, first)
	}
}

// Unknown, revoked and expired bearers get the same answer at the edge, under
// the failure budget and over it: the answer never says whether a credential
// exists.
func TestEdgeRejectedCredentialClassesAreAnsweredAlike(t *testing.T) {
	expiredAt := time.Now().Add(-time.Hour)
	classes := map[string]func(*hostedAPI) string{
		"unknown": func(*hostedAPI) string { return unknownWellFormedToken(t) },
		"revoked": func(h *hostedAPI) string {
			c := h.issue(readScopes, []string{repoPlain}, nil)
			h.revoke(c)
			return c.token
		},
		"expired": func(h *hostedAPI) string { return h.issue(readScopes, []string{repoPlain}, &expiredAt).token },
	}
	for _, phase := range []struct {
		name    string
		prefill int
		want    int
	}{{"under budget", 0, http.StatusUnauthorized}, {"over budget", 20, http.StatusTooManyRequests}} {
		answers := map[string]answer{}
		for name, credential := range classes {
			e, hosted := sharedAddressStack(t)
			const address = "203.0.113.43"
			burst(t, e, phase.prefill, func(int) string { return "junk" }, func(int) string { return address })
			header := http.Header{}
			header.Set("X-Forwarded-For", address)
			header.Set("Authorization", "Bearer "+credential(hosted))
			answers[name] = readAnswer(postMCP(t, e, http.MethodPost, rawToolsList(), header))
		}
		if answers["unknown"].status != phase.want {
			t.Fatalf("%s: unknown = %d, want %d", phase.name, answers["unknown"].status, phase.want)
		}
		for name, got := range answers {
			if !got.equal(answers["unknown"]) {
				t.Fatalf("%s: %s answer = %+v, unknown = %+v", phase.name, name, got, answers["unknown"])
			}
		}
	}
}

// answer is a response with the per-request headers removed.
type answer struct {
	status int
	header http.Header
	body   string
}

func (a answer) equal(b answer) bool {
	if a.status != b.status || a.body != b.body || len(a.header) != len(b.header) {
		return false
	}
	for name, values := range a.header {
		if strings.Join(values, "\x00") != strings.Join(b.header.Values(name), "\x00") {
			return false
		}
	}
	return true
}

func readAnswer(resp *http.Response) answer {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	header := resp.Header.Clone()
	header.Del("Date")
	header.Del("X-Request-Id")
	return answer{status: resp.StatusCode, header: header, body: string(body)}
}

func noCredentialResponse(t *testing.T, e *endpoint, address string) answer {
	t.Helper()
	header := http.Header{}
	header.Set("X-Forwarded-For", address)
	return readAnswer(postMCP(t, e, http.MethodPost, rawToolsList(), header))
}

// requestLines returns the edge request lines for one client address.
func requestLines(t *testing.T, e *endpoint, address string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range bytes.Split(e.logs.Bytes(), []byte("\n")) {
		var line map[string]any
		if json.Unmarshal(raw, &line) != nil || line["msg"] != acrmcp.HTTPRequestLogMessage || line["client_ip"] != address {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func reasonCounts(lines []map[string]any) map[string]int {
	counts := map[string]int{}
	for _, line := range lines {
		counts[line["gate_decision"].(string)+"/"+line["gate_reason"].(string)]++
	}
	return counts
}

// An address over its edge failure limit verifies at most one well-formed
// bearer at a time: a concurrent flood of guesses makes one acr-api call,
// the rest are refused unverified, a valid bearer from another address is
// served while the slot is held, and the address's own valid bearer is
// served once the slot is free.
func TestEdgeGateOverBudgetAddressVerifiesOneBearerAtATime(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpointWithGate(t, hosted, acrmcp.EdgeGateOptions{FailureLimit: 3, TrustedProxyCIDRs: loopbackProxies})
	valid := hosted.issue(readScopes, []string{repoPlain}, nil)
	const attacker, other = "203.0.113.50", "203.0.113.51"
	burst(t, e, 3, func(int) string { return "junk" }, func(int) string { return attacker })
	guess := unknownWellFormedToken(t)
	hold := newBearerHold(guess)
	hosted.bearerHold.Store(hold)

	const flood = 30
	var refused atomic.Int64
	var wg sync.WaitGroup
	codes := make([]int, flood)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = gateStatus(t, e, guess, attacker)
			if codes[i] == http.StatusTooManyRequests {
				refused.Add(1)
			}
		}()
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		parked, _, _ := hold.counts()
		if parked == 1 && refused.Load() == flood-1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("flood did not settle: parked=%d refused=%d", parked, refused.Load())
		}
		time.Sleep(time.Millisecond)
	}
	if status := gateStatus(t, e, valid.token, other); status != http.StatusOK {
		t.Fatalf("valid bearer from another address while the slot is held = %d, want 200", status)
	}
	busyHeader := http.Header{}
	busyHeader.Set("Authorization", "Bearer "+valid.token)
	busyHeader.Set("X-Forwarded-For", attacker)
	busy := readAnswer(postMCP(t, e, http.MethodPost, rawToolsList(), busyHeader))
	if busy.status != http.StatusTooManyRequests {
		t.Fatalf("valid bearer from the flooding address while its slot is held = %d, want 429 (accepted residual)", busy.status)
	}
	if seconds, err := strconv.Atoi(busy.header.Get("Retry-After")); err != nil || seconds < 1 {
		t.Fatalf("slot-busy refusal Retry-After = %q, want a positive number of seconds", busy.header.Get("Retry-After"))
	}
	close(hold.release)
	wg.Wait()
	// The held attempt's request line is written after its slot is released,
	// so its presence means the slot is free; the clients see their answers
	// before that.
	settled := time.Now().Add(10 * time.Second)
	for reasonCounts(requestLines(t, e, attacker))["failure_budget/rejected_counted"] != 1 {
		if time.Now().After(settled) {
			t.Fatal("held attempt never finished releasing its slot")
		}
		time.Sleep(time.Millisecond)
	}
	for i, code := range codes {
		if code != http.StatusTooManyRequests {
			t.Fatalf("over-budget guess %d = %d, want 429", i, code)
		}
	}
	if _, maxSeen, arrivals := hold.counts(); maxSeen != 1 || arrivals != 1 {
		t.Fatalf("over-budget flood: acr-api calls %d, most at once %d; want 1 and 1", arrivals, maxSeen)
	}
	if status := gateStatus(t, e, valid.token, attacker); status != http.StatusOK {
		t.Fatalf("valid bearer from the flooding address after the slot is free = %d, want 200", status)
	}
	if status := noCredentialResponse(t, e, attacker).status; status != http.StatusUnauthorized {
		t.Fatalf("no-credential request from the flooding address = %d, want 401", status)
	}

	got := reasonCounts(requestLines(t, e, attacker))
	want := map[string]int{
		"admitted/rejected_counted":          3,
		"in_flight/verification_slot_busy":   flood - 1 + 1,
		"failure_budget/rejected_counted":    1,
		"admitted/verified_over_budget":      1,
		"admitted/no_credential_not_counted": 1,
	}
	if len(got) != len(want) {
		t.Fatalf("gate decision/reason counts = %v, want %v", got, want)
	}
	for key, n := range want {
		if got[key] != n {
			t.Fatalf("gate decision/reason counts = %v, want %v", got, want)
		}
	}
	if lines := requestLines(t, e, other); len(lines) != 1 || lines[0]["gate_reason"] != acrmcp.HTTPGateReasonVerified {
		t.Fatalf("other address lines = %v, want one verified", lines)
	}
}

// A bearer verified in the over-budget slot and rejected after the edge
// window rolled over is still answered as an over-budget rejection (429).
func TestEdgeGateSlotRejectionDecidedAfterWindowRolloverIsStillRefused(t *testing.T) {
	hosted := newHostedAPI(t)
	e, advance := newClockedEndpoint(t, hosted, 3)
	const address = "203.0.113.60"
	burst(t, e, 3, func(int) string { return "junk" }, func(int) string { return address })
	guess := unknownWellFormedToken(t)
	hold := newBearerHold(guess)
	hosted.bearerHold.Store(hold)
	done := make(chan answer)
	go func() {
		header := http.Header{}
		header.Set("Authorization", "Bearer "+guess)
		header.Set("X-Forwarded-For", address)
		done <- readAnswer(postMCP(t, e, http.MethodPost, rawToolsList(), header))
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if parked, _, _ := hold.counts(); parked == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slot verification never reached acr-api")
		}
		time.Sleep(time.Millisecond)
	}
	advance(2 * time.Minute)
	if status := gateStatus(t, e, "junk", address); status != http.StatusUnauthorized {
		t.Fatalf("malformed bearer in the new window = %d, want 401", status)
	}
	close(hold.release)
	if got := <-done; got.status != http.StatusTooManyRequests || got.header.Get("Retry-After") != "60" {
		t.Fatalf("slot rejection decided after the window rolled over = %d Retry-After %q, want 429 with 60 (the rest of the window it was decided in)", got.status, got.header.Get("Retry-After"))
	}
}

// newClockedEndpoint is the hosted MCP endpoint with an edge failure limit,
// a one-minute window and a clock the test advances.
func newClockedEndpoint(t *testing.T, hosted *hostedAPI, failureLimit int) (*endpoint, func(time.Duration)) {
	t.Helper()
	return newClockedEndpointWith(t, hosted, failureLimit, 5*time.Second)
}

func newClockedEndpointWith(t *testing.T, hosted *hostedAPI, failureLimit int, resolveTimeout time.Duration) (*endpoint, func(time.Duration)) {
	t.Helper()
	var clockMu sync.Mutex
	clock := time.Now()
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	logs := &syncBuffer{}
	cfg, err := acrmcp.NewHTTPProcessConfig(hosted.sidecarConfig(), testIdentity, logs)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := acrmcp.NewHTTPHandler(cfg, acrmcp.HTTPHandlerOptions{
		BasePath: "/mcp", Identity: testIdentity, MaxRequestBodyBytes: 1 << 20, ResolveTimeout: resolveTimeout, Now: now,
		EdgeGate: acrmcp.EdgeGateOptions{FailureLimit: failureLimit, Window: time.Minute, TrustedProxyCIDRs: loopbackProxies},
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &endpoint{handler: handler, logs: logs, server: httptest.NewServer(handler)}
	t.Cleanup(e.server.Close)
	return e, func(d time.Duration) {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock = clock.Add(d)
	}
}
