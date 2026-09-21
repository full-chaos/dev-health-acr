package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var readScopes = []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}

// A client connecting with the SDK's default (newest) revision is answered
// through server/discover on 2026-07-28, sees the server revision, and is
// never issued a session: every response of the exchange lacks
// Mcp-Session-Id.
func TestHTTPDiscoverNegotiates20260728WithoutASession(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpoint(t, hosted)
	caller := hosted.issue(readScopes, []string{repoPlain}, nil)
	rt := &headerTransport{bearer: caller.token}

	session := connectClient(t, e, rt)
	init := session.InitializeResult()
	if init.ProtocolVersion != "2026-07-28" {
		t.Fatalf("negotiated %q, want 2026-07-28", init.ProtocolVersion)
	}
	if init.ServerInfo == nil || init.ServerInfo.Version != testIdentity.Version {
		t.Fatalf("server revision %+v, want %s", init.ServerInfo, testIdentity.Version)
	}
	// Two consecutive requests after discover: each is answered on its own.
	for range 2 {
		if names := toolNames(t, session); len(names) == 0 {
			t.Fatal("tools/list returned no tools")
		}
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.responses < 3 {
		t.Fatalf("observed %d responses, want >= 3", rt.responses)
	}
	if len(rt.sessionIDs) != 0 {
		t.Fatalf("server issued Mcp-Session-Id %v", rt.sessionIDs)
	}
}

// A request carrying a session id it was never given is served exactly like
// one without: the endpoint neither requires nor honours a session.
func TestHTTPIgnoresAnUnissuedSessionID(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpoint(t, hosted)
	caller := hosted.issue(readScopes, []string{repoPlain}, nil)
	for _, header := range []http.Header{bearerHeader(caller.token), withSession(bearerHeader(caller.token), "not-issued")} {
		resp := postMCP(t, e, http.MethodPost, rawToolsList(), header)
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Mcp-Session-Id") != "" {
			t.Fatalf("status %d session %q body %s", resp.StatusCode, resp.Header.Get("Mcp-Session-Id"), body)
		}
		mustContain(t, string(body), `"source_evidence"`)
		// The catalogue is this credential's own: no shared cache may keep it.
		mustContain(t, string(body), `"cacheScope":"private"`)
	}
}

// The configured body bound is enforced by the transport for an admitted
// caller, and the request line records the rejection.
func TestHTTPBodyLimitIsEnforced(t *testing.T) {
	hosted := newHostedAPI(t)
	logs := &syncBuffer{}
	cfg, err := acrmcp.NewHTTPProcessConfig(hosted.sidecarConfig(), testIdentity, logs)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := acrmcp.NewHTTPHandler(cfg, acrmcp.HTTPHandlerOptions{BasePath: "/mcp", Identity: testIdentity, MaxRequestBodyBytes: 512, ResolveTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	e := &endpoint{handler: handler, logs: logs, server: httptest.NewServer(handler)}
	t.Cleanup(e.server.Close)
	caller := hosted.issue(readScopes, []string{repoPlain}, nil)
	big := rawToolsCall("source_evidence", map[string]any{"evidence_ref_id": strings.Repeat("e", 600)})
	resp := postMCP(t, e, http.MethodPost, big, withRequestID(bearerHeader(caller.token), "big"))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", resp.StatusCode)
	}
	if _, err := certify.Certify(parseLog(t, e), certify.Assertion{Event: eventspec.MCPHTTPRequest, Want: map[string]any{
		"request_id": "big", "auth_outcome": "admitted", "result_class": "transport_rejected", "status": 413,
	}}); err != nil {
		t.Fatal(err)
	}
	small := postMCP(t, e, http.MethodPost, rawToolsList(), bearerHeader(caller.token))
	if small.StatusCode != http.StatusOK {
		t.Fatalf("a request within the bound: %d", small.StatusCode)
	}
}

func withSession(h http.Header, id string) http.Header {
	out := h.Clone()
	out.Set("Mcp-Session-Id", id)
	return out
}

// tools/list describes what the calling credential can do: the hosted API
// advertises the answer tools to one credential and not the other.
func TestHTTPToolsListFollowsEachCredential(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpoint(t, hosted)
	answers := hosted.issue(readScopes, []string{repoAnswers}, nil)
	plain := hosted.issue(readScopes, []string{repoPlain}, nil)

	answerTools := toolNames(t, connectClient(t, e, &headerTransport{bearer: answers.token}))
	plainTools := toolNames(t, connectClient(t, e, &headerTransport{bearer: plain.token}))
	if len(answerTools) == 0 || len(plainTools) == 0 {
		t.Fatalf("empty catalogue: %v / %v", answerTools, plainTools)
	}
	if want := []string{"context_for_task", "investigate_question", "investigation_result", "source_evidence"}; !slices.Equal(answerTools, want) {
		t.Fatalf("answers credential tools %v, want %v", answerTools, want)
	}
	if want := []string{"context_for_task", "source_evidence"}; !slices.Equal(plainTools, want) {
		t.Fatalf("plain credential tools %v, want %v", plainTools, want)
	}
}

// Every credential the hosted Authenticator refuses, and every bearer the
// endpoint cannot read, is refused with its mapped status BEFORE the SDK
// handler runs: the counter wrapped around the SDK handler stays at zero for
// every refused row and moves only for the admitted one.
func TestHTTPAuthMatrixFailsClosedBeforeTheSDKHandler(t *testing.T) {
	hosted := newHostedAPI(t)
	valid := hosted.issue(readScopes, []string{repoPlain}, nil)
	expiredAt := time.Now().Add(-time.Hour)
	expired := hosted.issue(readScopes, []string{repoPlain}, &expiredAt)
	revoked := hosted.issue(readScopes, []string{repoPlain}, nil)
	hosted.revoke(revoked)
	noContextRead := hosted.issue([]string{auth.ScopeEvidenceRead}, []string{repoPlain}, nil)
	noEvidenceRead := hosted.issue([]string{auth.ScopeContextRead}, []string{repoPlain}, nil)
	incompatible := hosted.issue(readScopes, []string{repoIncompatible}, nil)
	upgrade := hosted.issue(readScopes, []string{repoUpgrade}, nil)
	unknown := hosted.issue(readScopes, []string{repoPlain}, nil)
	unknown.token = unknown.token[:len(unknown.token)-4] + "AAAA"

	type row struct {
		name      string
		header    http.Header
		setup     func()
		status    int
		outcome   string
		challenge string
	}
	rows := []row{
		{name: "missing", header: http.Header{}, status: 401, outcome: acrmcp.HTTPAuthMissingBearer, challenge: "Bearer"},
		{name: "wrong_scheme", header: http.Header{"Authorization": {"Basic " + valid.token}}, status: 401, outcome: acrmcp.HTTPAuthMalformedBearer, challenge: `Bearer error="invalid_token"`},
		{name: "two_headers", header: http.Header{"Authorization": {"Bearer " + valid.token, "Bearer " + valid.token}}, status: 401, outcome: acrmcp.HTTPAuthMalformedBearer, challenge: `Bearer error="invalid_token"`},
		{name: "empty_token", header: http.Header{"Authorization": {"Bearer "}}, status: 401, outcome: acrmcp.HTTPAuthMalformedBearer, challenge: `Bearer error="invalid_token"`},
		{name: "malformed_token", header: bearerHeader("fcacr_short"), status: 401, outcome: acrmcp.HTTPAuthMalformedBearer, challenge: `Bearer error="invalid_token"`},
		{name: "license_key_shape", header: bearerHeader("dh_live_0123456789abcdef0123456789abcdef"), status: 401, outcome: acrmcp.HTTPAuthMalformedBearer, challenge: `Bearer error="invalid_token"`},
		{name: "unknown", header: bearerHeader(unknown.token), status: 401, outcome: acrmcp.HTTPAuthInvalidCredential, challenge: `Bearer error="invalid_token"`},
		{name: "expired", header: bearerHeader(expired.token), status: 401, outcome: acrmcp.HTTPAuthInvalidCredential, challenge: `Bearer error="invalid_token"`},
		{name: "revoked", header: bearerHeader(revoked.token), status: 401, outcome: acrmcp.HTTPAuthInvalidCredential, challenge: `Bearer error="invalid_token"`},
		{name: "insufficient_scope", header: bearerHeader(noContextRead.token), status: 403, outcome: acrmcp.HTTPAuthInsufficientScope, challenge: `Bearer error="insufficient_scope"`},
		{name: "insufficient_entitlement", header: bearerHeader(noEvidenceRead.token), status: 403, outcome: acrmcp.HTTPAuthInsufficientEntitlement, challenge: `Bearer error="insufficient_scope"`},
		{name: "upstream_incompatible", header: bearerHeader(incompatible.token), status: 502, outcome: acrmcp.HTTPAuthUpstreamIncompatible},
		{name: "upstream_version_mismatch", header: bearerHeader(upgrade.token), status: 502, outcome: acrmcp.HTTPAuthUpstreamIncompatible},
		{name: "rate_limited", header: bearerHeader(valid.token), setup: func() { hosted.blocked.Store(true) }, status: 429, outcome: acrmcp.HTTPAuthRateLimited},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			e := newEndpoint(t, hosted)
			hosted.blocked.Store(false)
			if r.setup != nil {
				r.setup()
			}
			resp := postMCP(t, e, http.MethodPost, rawToolsList(), r.header)
			hosted.blocked.Store(false)
			var body struct {
				Error string `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != r.status || body.Error != r.outcome {
				t.Fatalf("status %d error %q, want %d %q", resp.StatusCode, body.Error, r.status, r.outcome)
			}
			if got := resp.Header.Get("WWW-Authenticate"); got != r.challenge {
				t.Fatalf("WWW-Authenticate %q, want %q", got, r.challenge)
			}
			if r.outcome == acrmcp.HTTPAuthRateLimited && resp.Header.Get("Retry-After") != "7" {
				t.Fatalf("Retry-After %q, want 7", resp.Header.Get("Retry-After"))
			}
			if hits := e.sdkHits.Load(); hits != 0 {
				t.Fatalf("a refused request reached the SDK handler %d times", hits)
			}
		})
	}
	t.Run("upstream_unavailable", func(t *testing.T) {
		down := newHostedAPI(t)
		e := newEndpoint(t, down)
		down.server.Close()
		resp := postMCP(t, e, http.MethodPost, rawToolsList(), bearerHeader(valid.token))
		if resp.StatusCode != http.StatusServiceUnavailable || e.sdkHits.Load() != 0 {
			t.Fatalf("status %d, sdk hits %d", resp.StatusCode, e.sdkHits.Load())
		}
	})
	t.Run("admitted", func(t *testing.T) {
		e := newEndpoint(t, hosted)
		resp := postMCP(t, e, http.MethodPost, rawToolsList(), bearerHeader(valid.token))
		if resp.StatusCode != http.StatusOK || e.sdkHits.Load() != 1 {
			t.Fatalf("status %d, sdk hits %d", resp.StatusCode, e.sdkHits.Load())
		}
	})
	// Every refused outcome in the vocabulary has a row above.
	covered := map[string]bool{acrmcp.HTTPAuthUpstreamUnavailable: true, acrmcp.HTTPAuthAdmitted: true}
	for _, r := range rows {
		covered[r.outcome] = true
	}
	for _, outcome := range acrmcp.HTTPAuthOutcomeVocabulary() {
		if !covered[outcome] {
			t.Errorf("auth outcome %q has no executed row", outcome)
		}
	}
	if len(rows) != 14 {
		t.Fatalf("matrix has %d rows, want 14", len(rows))
	}
}

// Two callers, interleaved on one endpoint and holding requests open at the
// same time, each receive only their own answer, and the hosted API sees
// each caller's requests arrive on that caller's own credential.
func TestHTTPConcurrentCallersNeverShareIdentity(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpoint(t, hosted)
	a := hosted.issue(readScopes, []string{repoPlain}, nil)
	b := hosted.issue(readScopes, []string{repoPlain}, nil)
	sessions := map[string]*mcpsdk.ClientSession{
		a.credentialID: connectClient(t, e, &headerTransport{bearer: a.token}),
		b.credentialID: connectClient(t, e, &headerTransport{bearer: b.token}),
	}
	const rounds = 6
	for round := range rounds {
		gate := newBarrier(2)
		hosted.evidenceWait.Store(gate)
		var wg sync.WaitGroup
		got := map[string]string{}
		var mu sync.Mutex
		for id, session := range sessions {
			wg.Add(1)
			go func() {
				defer wg.Done()
				result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
					Name: "source_evidence", Arguments: map[string]any{"evidence_ref_id": fmt.Sprintf("evidence_%04d", round)},
				})
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				got[id] = expandedEvidenceExcerpt(t, result)
				mu.Unlock()
			}()
		}
		wg.Wait()
		if gate.maxSeen != 2 {
			t.Fatalf("round %d: the two calls were not in flight together (max %d)", round, gate.maxSeen)
		}
		for id := range sessions {
			if got[id] != "excerpt-for-"+id {
				t.Fatalf("round %d: caller %s received %q", round, id, got[id])
			}
		}
	}
	hosted.evidenceWait.Store(nil)
	for id := range sessions {
		evidenceCalls := 0
		for _, path := range hosted.pathsSeenBy(id) {
			if len(path) > len("/api/v1/agent-context/evidence/") && path[:len("/api/v1/agent-context/evidence/")] == "/api/v1/agent-context/evidence/" {
				evidenceCalls++
			}
		}
		if evidenceCalls != rounds {
			t.Fatalf("hosted API saw %d evidence calls on credential %s, want %d", evidenceCalls, id, rounds)
		}
	}
	if n := e.handler.InFlight(); n != 0 {
		t.Fatalf("in-flight gauge %d after every request completed, want 0", n)
	}
	lines := parseLog(t, e)
	maxInFlight := 0
	for _, line := range lines.LinesWithMsg(eventspec.MCPHTTPRequest.Msg) {
		if v, ok := line["in_flight"].(float64); ok && int(v) > maxInFlight {
			maxInFlight = int(v)
		}
	}
	if maxInFlight != 2 {
		t.Fatalf("max in_flight on request lines %d, want 2", maxInFlight)
	}
}

// /healthz answers while the process is up, whatever the hosted API does;
// /readyz answers ready only while the hosted liveness route does, and logs
// each change of state once.
func TestHTTPHealthAndReadiness(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpoint(t, hosted)
	get := func(path string) int {
		resp, err := http.Get(e.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	steps := []struct {
		live       int
		wantHealth int
		wantReady  int
	}{
		{live: 200, wantHealth: 200, wantReady: 200},
		{live: 200, wantHealth: 200, wantReady: 200},
		{live: 503, wantHealth: 200, wantReady: 503},
		{live: 200, wantHealth: 200, wantReady: 200},
	}
	for i, step := range steps {
		hosted.liveStatus.Store(int32(step.live))
		if got := get("/healthz"); got != step.wantHealth {
			t.Fatalf("step %d: /healthz %d, want %d", i, got, step.wantHealth)
		}
		if got := get("/readyz"); got != step.wantReady {
			t.Fatalf("step %d: /readyz %d, want %d", i, got, step.wantReady)
		}
	}
	hosted.server.Close()
	if got := get("/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("/readyz with the hosted API down: %d, want 503", got)
	}
	if got := get("/healthz"); got != http.StatusOK {
		t.Fatalf("/healthz with the hosted API down: %d, want 200", got)
	}

	log := parseLog(t, e)
	want := []map[string]any{
		{"sequence": 1, "state": "ready", "previous_state": "unknown", "failure_class": "none"},
		{"sequence": 2, "state": "not_ready", "previous_state": "ready", "failure_class": "not_live"},
		{"sequence": 3, "state": "ready", "previous_state": "not_ready", "failure_class": "none"},
		{"sequence": 4, "state": "not_ready", "previous_state": "ready", "failure_class": "unreachable"},
	}
	if got := len(log.LinesWithMsg(eventspec.MCPHTTPReadiness.Msg)); got != len(want) {
		t.Fatalf("%d readiness lines, want %d (one per change, none for a repeated state)", got, len(want))
	}
	for _, w := range want {
		w["transport"] = "http"
		if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.MCPHTTPReadiness, Want: w}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(log.LinesWithMsg(eventspec.MCPHTTPRequest.Msg)); n != 0 {
		t.Fatalf("probe routes wrote %d request lines, want 0", n)
	}
}

func parseLog(t *testing.T, e *endpoint) *certify.Log {
	t.Helper()
	log, err := certify.Parse(e.logs.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return log
}
