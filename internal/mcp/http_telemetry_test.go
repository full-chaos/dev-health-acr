package mcp_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Every auth outcome and every result class is driven through the real
// endpoint by a real request, and its request line is certified against the
// eventspec declaration through the real slog JSON handler. The census at the
// end fails if a vocabulary member has no executed driver.
func TestHTTPRequestLineCertifiesEveryAuthOutcomeAndResultClass(t *testing.T) {
	hosted := newHostedAPI(t)
	valid := hosted.issue(readScopes, []string{repoPlain}, nil)
	expiredAt := time.Now().Add(-time.Hour)
	expired := hosted.issue(readScopes, []string{repoPlain}, &expiredAt)
	noContextRead := hosted.issue([]string{auth.ScopeEvidenceRead}, []string{repoPlain}, nil)
	noEvidenceRead := hosted.issue([]string{auth.ScopeContextRead}, []string{repoPlain}, nil)
	incompatible := hosted.issue(readScopes, []string{repoIncompatible}, nil)

	type driver struct {
		id       string
		method   string
		body     []byte
		header   http.Header
		setup    func()
		down     bool
		want     map[string]any
		bearer   bool
		wantHTTP int
	}
	drivers := []driver{
		{id: "req-ok", body: rawToolsList(), header: bearerHeader(valid.token), bearer: true, wantHTTP: 200,
			want: map[string]any{"auth_outcome": "admitted", "result_class": "ok", "method": "tools/list", "tool": "none", "protocol_revision": "2026-07-28"}},
		{id: "req-tool-ok", body: rawToolsCall("source_evidence", map[string]any{"evidence_ref_id": "evidence_0001"}), header: bearerHeader(valid.token), bearer: true, wantHTTP: 200,
			want: map[string]any{"auth_outcome": "admitted", "result_class": "ok", "method": "tools/call", "tool": "source_evidence", "protocol_revision": "2026-07-28"}},
		{id: "req-tool-error", body: rawToolsCall("source_evidence", map[string]any{"evidence_ref_id": evidenceMissing}), header: bearerHeader(valid.token), bearer: true, wantHTTP: 200,
			want: map[string]any{"auth_outcome": "admitted", "result_class": "tool_error", "method": "tools/call", "tool": "source_evidence"}},
		{id: "req-protocol-error", body: rawToolsCall("no_such_tool", map[string]any{}), header: bearerHeader(valid.token), bearer: true,
			want: map[string]any{"auth_outcome": "admitted", "result_class": "protocol_error", "method": "tools/call", "tool": "other"}},
		{id: "req-transport-rejected", method: http.MethodGet, header: bearerHeader(valid.token), bearer: true, wantHTTP: 405,
			want: map[string]any{"auth_outcome": "admitted", "result_class": "transport_rejected", "method": "none", "tool": "none", "status": 405}},
		{id: "req-missing", body: rawToolsList(), header: http.Header{}, wantHTTP: 401,
			want: map[string]any{"auth_outcome": "missing_bearer", "result_class": "auth_denied", "principal_class": "none", "status": 401, "method": "none"}},
		{id: "req-malformed", body: rawToolsList(), header: bearerHeader("fcacr_short"), wantHTTP: 401,
			want: map[string]any{"auth_outcome": "malformed_bearer", "result_class": "auth_denied", "principal_class": "none", "status": 401}},
		{id: "req-invalid", body: rawToolsList(), header: bearerHeader(expired.token), bearer: true, wantHTTP: 401,
			want: map[string]any{"auth_outcome": "invalid_credential", "result_class": "auth_denied", "status": 401}},
		{id: "req-scope", body: rawToolsList(), header: bearerHeader(noContextRead.token), bearer: true, wantHTTP: 403,
			want: map[string]any{"auth_outcome": "insufficient_scope", "result_class": "auth_denied", "status": 403}},
		{id: "req-entitlement", body: rawToolsList(), header: bearerHeader(noEvidenceRead.token), bearer: true, wantHTTP: 403,
			want: map[string]any{"auth_outcome": "insufficient_entitlement", "result_class": "auth_denied", "status": 403}},
		{id: "req-rate", body: rawToolsList(), header: bearerHeader(valid.token), bearer: true, wantHTTP: 429, setup: func() { hosted.blocked.Store(true) },
			want: map[string]any{"auth_outcome": "rate_limited", "result_class": "auth_denied", "status": 429}},
		{id: "req-incompatible", body: rawToolsList(), header: bearerHeader(incompatible.token), bearer: true, wantHTTP: 502,
			want: map[string]any{"auth_outcome": "upstream_incompatible", "result_class": "auth_unavailable", "status": 502}},
		{id: "req-unavailable", body: rawToolsList(), header: bearerHeader(valid.token), bearer: true, wantHTTP: 503, down: true,
			want: map[string]any{"auth_outcome": "upstream_unavailable", "result_class": "auth_unavailable", "status": 503, "protocol_revision": "2026-07-28"}},
	}

	e := newEndpoint(t, hosted)
	down := newHostedAPI(t)
	eDown := newEndpoint(t, down)
	down.server.Close()

	seenOutcomes, seenResults := map[string]bool{}, map[string]bool{}
	for _, d := range drivers {
		target := e
		if d.down {
			target = eDown
		}
		hosted.blocked.Store(false)
		if d.setup != nil {
			d.setup()
		}
		method := d.method
		if method == "" {
			method = http.MethodPost
		}
		resp := postMCP(t, target, method, d.body, withRequestID(d.header, d.id))
		_, _ = io.Copy(io.Discard, resp.Body)
		hosted.blocked.Store(false)
		if d.wantHTTP != 0 && resp.StatusCode != d.wantHTTP {
			t.Fatalf("%s: status %d, want %d", d.id, resp.StatusCode, d.wantHTTP)
		}
		if got := resp.Header.Get("X-Request-ID"); got != d.id {
			t.Fatalf("%s: response X-Request-ID %q, want the inbound id", d.id, got)
		}
		log := parseLog(t, target)
		want := map[string]any{
			"request_id": d.id, "transport": "http",
			"server_version": testIdentity.Version, "server_commit": testIdentity.Commit,
		}
		for k, v := range d.want {
			want[k] = v
		}
		if d.bearer {
			want["principal_class"] = "bearer"
		}
		result, err := certify.Certify(log, certify.Assertion{Event: eventspec.MCPHTTPRequest, Want: want})
		if err != nil {
			t.Fatalf("%s: %v", d.id, err)
		}
		line := result.Line
		ref, hasRef := line["principal_ref"].(string)
		if d.bearer != hasRef || (hasRef && !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(ref)) {
			t.Fatalf("%s: principal_ref %q present=%v, want present=%v", d.id, ref, hasRef, d.bearer)
		}
		if latency, ok := line["latency_ms"].(float64); !ok || latency < 0 {
			t.Fatalf("%s: latency_ms %v", d.id, line["latency_ms"])
		}
		seenOutcomes[d.want["auth_outcome"].(string)] = true
		seenResults[d.want["result_class"].(string)] = true
	}
	for _, outcome := range acrmcp.HTTPAuthOutcomeVocabulary() {
		if !seenOutcomes[outcome] {
			t.Errorf("auth outcome %q has no executed driver", outcome)
		}
	}
	for _, class := range acrmcp.HTTPResultClassVocabulary() {
		if !seenResults[class] {
			t.Errorf("result class %q has no executed driver", class)
		}
	}
	if e.handler.InFlight() != 0 || eDown.handler.InFlight() != 0 {
		t.Fatalf("in-flight gauge did not return to 0")
	}
}

// The line never carries the bearer, its store hash, or a request body, and
// the same bearer yields the same opaque reference within the process while a
// different bearer yields a different one.
func TestHTTPRequestLineCarriesNoCredentialOrBody(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpoint(t, hosted)
	a := hosted.issue(readScopes, []string{repoPlain}, nil)
	b := hosted.issue(readScopes, []string{repoPlain}, nil)
	marker := "corpus-marker-7f3a"
	for i, token := range []string{a.token, a.token, b.token} {
		resp := postMCP(t, e, http.MethodPost, rawToolsCall("source_evidence", map[string]any{"evidence_ref_id": marker}), withRequestID(bearerHeader(token), []string{"ra1", "ra2", "rb1"}[i]))
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	raw := string(e.logs.Bytes())
	for _, secret := range []string{a.token, b.token, auth.HashToken(a.token), auth.HashToken(b.token), marker, a.credentialID, b.credentialID} {
		if strings.Contains(raw, secret) {
			t.Fatalf("request log carries %q", secret)
		}
	}
	log := parseLog(t, e)
	refs := map[string]string{}
	for _, id := range []string{"ra1", "ra2", "rb1"} {
		result, err := certify.Certify(log, certify.Assertion{Event: eventspec.MCPHTTPRequest, Want: map[string]any{"request_id": id}})
		if err != nil {
			t.Fatal(err)
		}
		refs[id] = result.Line["principal_ref"].(string)
	}
	if refs["ra1"] != refs["ra2"] || refs["ra1"] == refs["rb1"] {
		t.Fatalf("principal refs %v: want equal per bearer, distinct across bearers", refs)
	}
}

// An inbound correlation id that is not a short safe token is replaced by a
// generated one, and the replacement is what the response and line carry.
func TestHTTPRequestIDIsGeneratedWhenAbsentOrUnsafe(t *testing.T) {
	hosted := newHostedAPI(t)
	e := newEndpoint(t, hosted)
	valid := hosted.issue(readScopes, []string{repoPlain}, nil)
	for _, inbound := range []string{"", "has space", strings.Repeat("x", 129)} {
		header := bearerHeader(valid.token)
		if inbound != "" {
			header.Set("X-Request-ID", inbound)
		}
		resp := postMCP(t, e, http.MethodPost, rawToolsList(), header)
		_, _ = io.Copy(io.Discard, resp.Body)
		got := resp.Header.Get("X-Request-ID")
		if !regexp.MustCompile(`^mcp_[0-9a-f]{24}$`).MatchString(got) {
			t.Fatalf("inbound %q: response id %q is not a generated id", inbound, got)
		}
		if _, err := certify.Certify(parseLog(t, e), certify.Assertion{Event: eventspec.MCPHTTPRequest, Want: map[string]any{"request_id": got}}); err != nil {
			t.Fatal(err)
		}
	}
}

// The production entry point: ServeHTTPTransport reads the ACR_API_* process
// configuration (no credential), binds, writes the serving line, serves a
// real client on 2026-07-28, and on cancellation lets an in-flight call
// finish before it returns.
func TestServeHTTPTransportServesAndShutsDownGracefully(t *testing.T) {
	hosted := newHostedAPI(t)
	caller := hosted.issue(readScopes, []string{repoPlain}, nil)
	t.Setenv(sidecar.APIURLEnvironment, hosted.server.URL)
	t.Setenv(sidecar.CACertPathEnvironment, hosted.caPath)
	opts := acrmcp.DefaultServeOptions()
	opts.Transport = acrmcp.TransportHTTP
	opts.Listen = "127.0.0.1:0"
	opts.ShutdownTimeout = 10 * time.Second

	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- acrmcp.ServeHTTPTransport(ctx, logs, testIdentity, opts) }()

	var address string
	deadline := time.Now().Add(10 * time.Second)
	for address == "" && time.Now().Before(deadline) {
		if log, err := certify.Parse(logs.Bytes()); err == nil {
			if lines := log.LinesWithMsg(eventspec.MCPHTTPServing.Msg); len(lines) == 1 {
				address, _ = lines[0]["listen_address"].(string)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if address == "" {
		t.Fatalf("no serving line; log: %s", logs.Bytes())
	}
	log, err := certify.Parse(logs.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.MCPHTTPServing, Want: map[string]any{
		"listen_address": address, "transport": "http", "base_path": "/mcp",
		"server_version": testIdentity.Version, "server_commit": testIdentity.Commit,
		"max_body_bytes": acrmcp.DefaultHTTPMaxBodyBytes,
	}}); err != nil {
		t.Fatal(err)
	}
	revisions, _ := log.LinesWithMsg(eventspec.MCPHTTPServing.Msg)[0]["protocol_revisions"].([]any)
	if len(revisions) == 0 || revisions[0] != "2026-07-28" {
		t.Fatalf("serving line protocol_revisions %v, want 2026-07-28 first", revisions)
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "serve-test", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcpsdk.StreamableClientTransport{
		Endpoint: "http://" + address + "/mcp", HTTPClient: &http.Client{Transport: &headerTransport{bearer: caller.token}},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.InitializeResult().ProtocolVersion; got != "2026-07-28" {
		t.Fatalf("negotiated %q", got)
	}

	gate := newBarrier(2)
	hosted.evidenceWait.Store(gate)
	callDone := make(chan error, 1)
	go func() {
		result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "source_evidence", Arguments: map[string]any{"evidence_ref_id": "evidence_slow"}})
		if err == nil && result.IsError {
			err = errors.New("the held call answered a tool error")
		}
		callDone <- err
	}()
	for time.Now().Before(deadline.Add(10*time.Second)) && func() bool { gate.mu.Lock(); defer gate.mu.Unlock(); return gate.arrived == 0 }() {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	time.Sleep(100 * time.Millisecond)
	gate.wait() // second arrival releases the held call
	if err := <-callDone; err != nil {
		t.Fatalf("in-flight call did not complete across shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ServeHTTPTransport returned %v after cancellation", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ServeHTTPTransport did not return after cancellation")
	}
	if _, err := http.Get("http://" + address + "/healthz"); err == nil {
		t.Fatal("listener still accepting after shutdown")
	}
}

// Startup refuses an invalid setting before binding, naming the setting.
func TestServeHTTPTransportRefusesInvalidOptions(t *testing.T) {
	opts := acrmcp.DefaultServeOptions()
	opts.Transport = acrmcp.TransportHTTP
	opts.BasePath = "no-leading-slash"
	var out strings.Builder
	err := acrmcp.ServeHTTPTransport(context.Background(), &out, testIdentity, opts)
	if err == nil || !strings.Contains(out.String(), acrmcp.HTTPBasePathEnvironment) {
		t.Fatalf("err %v, diagnostics %q", err, out.String())
	}
}
