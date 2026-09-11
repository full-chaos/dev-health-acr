package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type evidenceRows struct{}

const testRequestID = "req_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func (evidenceRows) ResolveEvidenceScope(context.Context, contextpacket.ReadPlan) (contractsv1.ResolvedScope, error) {
	return contractsv1.ResolvedScope{}, nil
}

func (evidenceRows) EvidenceRows(context.Context, contextpacket.ReadPlan) ([]contractsv1.EvidenceRef, []contractsv1.SourceWatermark, []contractsv1.UnavailableSource, error) {
	return nil, nil, nil, nil
}

func testLogger(buffer *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func testApp(t *testing.T, checks ...ReadinessCheck) *App {
	t.Helper()
	fixedNow := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	app, err := NewApp(AppConfig{
		ServiceName:    "dev-health-acr",
		ServiceVersion: "test",
		RequestTimeout: time.Second,
	}, Dependencies{
		Capabilities: StaticCapabilitiesProvider{
			Now: func() time.Time { return fixedNow },
			Value: contractsv1.Capabilities{
				SchemaVersion:         contractsv1.CapabilitiesSchema,
				Service:               "dev-health-acr",
				ServiceVersion:        "test",
				MinimumSidecarVersion: "0.1.0",
			},
		},
		ReadinessChecks: checks,
		Now:             func() time.Time { return fixedNow },
		RequestID:       func() string { return testRequestID },
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestHealth(t *testing.T) {
	response := httptest.NewRecorder()
	testApp(t).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	if response.Header().Get("X-Request-ID") != testRequestID {
		t.Fatalf("missing generated request id: %q", response.Header().Get("X-Request-ID"))
	}
}

func TestNewEvidenceStore_uses_injected_factory(t *testing.T) {
	codec, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: "test", Keys: map[string][]byte{"test": []byte("01234567890123456789012345678901")}})
	if err != nil {
		t.Fatal(err)
	}
	app := testApp(t)
	app.evidenceStoreFactory = contextpacket.NewEvidenceStoreFactory(codec)
	store, err := app.NewEvidenceStore(evidenceRows{})
	if err != nil || store == nil {
		t.Fatalf("evidence store = %#v, error = %v", store, err)
	}
}

func TestRequestIDIsPropagated(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", "req_0123456789abcdef0123456789abcdef")
	response := httptest.NewRecorder()
	testApp(t).Handler().ServeHTTP(response, request)
	if got := response.Header().Get("X-Request-ID"); got != "req_0123456789abcdef0123456789abcdef" {
		t.Fatalf("unexpected request ID: %q", got)
	}
}

func TestRequestIDInvalidCallerValueIsReplaced(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", "caller-request")
	response := httptest.NewRecorder()
	testApp(t).Handler().ServeHTTP(response, request)
	if got := response.Header().Get("X-Request-ID"); !strings.HasPrefix(got, "req_") || len(got) != 36 {
		t.Fatalf("replacement request ID is not canonical: %q", got)
	}
}

// TestRequestIDControlCharacterCallerValueIsReplaced is the CHAOS-4355
// response-bound follow-up's CodeQL go/log-injection guard (CWE-117,
// alert #54): a caller-supplied X-Request-ID carrying a newline (or any
// other control character) must never reach the response header, the
// request context RequestID(ctx) reads, or -- transitively -- any log
// line or error-response detail keys on that value. It was already
// replaced before this test existed (observability.parseRequestID's
// strict req_+32-hex format check rejects it too), but
// isSafeRequestIDHeaderValue (app.go) now rejects it explicitly, at the
// one place untrusted input enters the pipeline, rather than relying on
// that indirect path alone.
func TestRequestIDControlCharacterCallerValueIsReplaced(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", "evil\nFAKE_LOG_LINE=injected")
	response := httptest.NewRecorder()
	testApp(t).Handler().ServeHTTP(response, request)
	got := response.Header().Get("X-Request-ID")
	if strings.ContainsAny(got, "\r\n") || strings.Contains(got, "evil") {
		t.Fatalf("control-character request ID was not replaced: %q", got)
	}
	if !strings.HasPrefix(got, "req_") || len(got) != 36 {
		t.Fatalf("replacement request ID is not canonical: %q", got)
	}
}

func TestRequestIDInvalidGeneratedValueIsReplaced(t *testing.T) {
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second}, Dependencies{
		Capabilities: StaticCapabilitiesProvider{Value: contractsv1.Capabilities{SchemaVersion: contractsv1.CapabilitiesSchema}},
		RequestID:    func() string { return "invalid-generated-id" },
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	requestID := response.Header().Get("X-Request-ID")
	if !strings.HasPrefix(requestID, "req_") || len(requestID) != 36 {
		t.Fatalf("generated request ID is not canonical: %q", requestID)
	}
}

func TestRequestIDEntropyFailureStillProducesCanonicalID(t *testing.T) {
	requestID := newRequestIDFrom(strings.NewReader(""), time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC), 1)
	if !strings.HasPrefix(requestID, "req_") || len(requestID) != 36 {
		t.Fatalf("request ID = %q", requestID)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(requestID, "req_")); err != nil || strings.ToLower(requestID) != requestID {
		t.Fatalf("request ID is not lowercase canonical hex: %q, %v", requestID, err)
	}
}

func TestReadinessFailureIsSafe(t *testing.T) {
	app := testApp(t, CheckFunc{CheckName: "clickhouse", Fn: func(context.Context) error {
		return errors.New("clickhouse://user:super-secret@example")
	}})
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "super-secret") {
		t.Fatal("readiness response leaked dependency details")
	}
	var body readinessResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "not_ready" || len(body.Checks) != 1 || body.Checks[0].Name != "clickhouse" {
		t.Fatalf("unexpected body: %#v", body)
	}
}

// TestReadinessTransitionIsLoggedOnce is dictation 811: /readyz
// must log an Info "readiness state changed" line the FIRST time it is
// observed and again every time the overall status actually flips -- not
// ready -> ready and back -- naming every check's state, but NOT on every
// repeated poll that finds the same status. Exercises the handler at
// production level (real Handler().ServeHTTP, real App, real logger, no
// mocked transition tracker) with non-trivial values (a real check name,
// "postgres", never the field's zero value) so this pin cannot pass by
// asserting an unevaluated default.
func TestReadinessTransitionIsLoggedOnce(t *testing.T) {
	buffer := &bytes.Buffer{}
	failing := false
	app := testApp(t, CheckFunc{CheckName: "postgres", Fn: func(context.Context) error {
		if failing {
			return errors.New("postgres unreachable")
		}
		return nil
	}})
	app.logger = testLogger(buffer)

	poll := func(wantStatus int) {
		t.Helper()
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if response.Code != wantStatus {
			t.Fatalf("readyz status = %d, want %d", response.Code, wantStatus)
		}
	}
	transitionLines := func() []string {
		var lines []string
		for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
			if strings.Contains(line, "readiness state changed") {
				lines = append(lines, line)
			}
		}
		return lines
	}

	// First observation: unknown -> ready. Must log exactly once.
	poll(http.StatusOK)
	if lines := transitionLines(); len(lines) != 1 {
		t.Fatalf("after first observation, transition lines = %d, want 1: %v", len(lines), lines)
	} else if !strings.Contains(lines[0], `"previous_status":"unknown"`) || !strings.Contains(lines[0], `"status":"ready"`) || !strings.Contains(lines[0], `"postgres":"ready"`) {
		t.Fatalf("unexpected first transition line: %s", lines[0])
	}

	// Repeated poll at the same status: must NOT log again.
	poll(http.StatusOK)
	poll(http.StatusOK)
	if lines := transitionLines(); len(lines) != 1 {
		t.Fatalf("after repeated ready polls, transition lines = %d, want 1 (no duplicate): %v", len(lines), lines)
	}

	// ready -> not_ready: must log again, naming the failing store.
	failing = true
	poll(http.StatusServiceUnavailable)
	lines := transitionLines()
	if len(lines) != 2 {
		t.Fatalf("after failure, transition lines = %d, want 2: %v", len(lines), lines)
	}
	if !strings.Contains(lines[1], `"previous_status":"ready"`) || !strings.Contains(lines[1], `"status":"not_ready"`) || !strings.Contains(lines[1], `"postgres":"not_ready"`) {
		t.Fatalf("unexpected not_ready transition line: %s", lines[1])
	}

	// Repeated not_ready poll: must NOT log again.
	poll(http.StatusServiceUnavailable)
	if lines := transitionLines(); len(lines) != 2 {
		t.Fatalf("after repeated not_ready polls, transition lines = %d, want 2 (no duplicate): %v", len(lines), lines)
	}

	// not_ready -> ready: must log a third time.
	failing = false
	poll(http.StatusOK)
	lines = transitionLines()
	if len(lines) != 3 {
		t.Fatalf("after recovery, transition lines = %d, want 3: %v", len(lines), lines)
	}
	if !strings.Contains(lines[2], `"previous_status":"not_ready"`) || !strings.Contains(lines[2], `"status":"ready"`) {
		t.Fatalf("unexpected recovery transition line: %s", lines[2])
	}
	if strings.Contains(buffer.String(), "postgres unreachable") {
		t.Fatal("readiness transition log leaked the raw check error")
	}
}

// TestReadinessPerCheckTransitionIsLoggedWhileAggregateStaysNotReady is r1
// P3 finding 5's own pin: with two checks and one (postgres) ALREADY
// failing, when the SECOND (clickhouse) also changes state, that must
// still log an Info "readiness check state changed" line naming clickhouse
// specifically -- even though the AGGREGATE status never changes (it was
// not_ready before and stays not_ready after). The aggregate-only
// "readiness state changed" line cannot catch this: it only fires again
// once the aggregate itself flips, which it does not here.
func TestReadinessPerCheckTransitionIsLoggedWhileAggregateStaysNotReady(t *testing.T) {
	buffer := &bytes.Buffer{}
	postgresFailing := true
	clickhouseFailing := false
	app := testApp(t,
		CheckFunc{CheckName: "postgres", Fn: func(context.Context) error {
			if postgresFailing {
				return errors.New("postgres unreachable")
			}
			return nil
		}},
		CheckFunc{CheckName: "clickhouse", Fn: func(context.Context) error {
			if clickhouseFailing {
				return errors.New("clickhouse unreachable")
			}
			return nil
		}},
	)
	app.logger = testLogger(buffer)

	poll := func(wantStatus int) {
		t.Helper()
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if response.Code != wantStatus {
			t.Fatalf("readyz status = %d, want %d", response.Code, wantStatus)
		}
	}
	checkLines := func() []string {
		var lines []string
		for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
			if strings.Contains(line, "readiness check state changed") {
				lines = append(lines, line)
			}
		}
		return lines
	}

	// First observation: postgres not_ready (unknown -> not_ready),
	// clickhouse ready (unknown -> ready). Both are first-observation
	// transitions -- 2 per-check lines.
	poll(http.StatusServiceUnavailable)
	if lines := checkLines(); len(lines) != 2 {
		t.Fatalf("after first observation, per-check lines = %d, want 2: %v", len(lines), lines)
	}

	// clickhouse flips to failing too. Aggregate status is "not_ready"
	// BOTH before and after this poll -- the aggregate line does not fire
	// again -- but this is a real, distinct per-check transition that must
	// still be observable at Info.
	clickhouseFailing = true
	poll(http.StatusServiceUnavailable)
	lines := checkLines()
	if len(lines) != 3 {
		t.Fatalf("after clickhouse also fails, per-check lines = %d, want 3: %v", len(lines), lines)
	}
	last := lines[2]
	if !strings.Contains(last, `"check":"clickhouse"`) || !strings.Contains(last, `"previous_status":"ready"`) || !strings.Contains(last, `"status":"not_ready"`) {
		t.Fatalf("unexpected clickhouse transition line: %s", last)
	}

	// Repeated poll at the same per-check states: no new per-check lines.
	poll(http.StatusServiceUnavailable)
	if lines := checkLines(); len(lines) != 3 {
		t.Fatalf("after a repeated poll, per-check lines = %d, want 3 (no duplicate): %v", len(lines), lines)
	}
}

func TestCapabilitiesShape(t *testing.T) {
	app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	var capabilities contractsv1.Capabilities
	if err := json.Unmarshal(response.Body.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities.SchemaVersion != contractsv1.CapabilitiesSchema {
		t.Fatalf("unexpected schema: %s", capabilities.SchemaVersion)
	}
}

func TestCapabilitiesFailureUsesContractError(t *testing.T) {
	provider := failingCapabilitiesProvider{}
	app, token := newHostedTestApp(t, provider, nil, []string{auth.ScopeContextRead}, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Request-ID", testRequestID)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.RequestID != testRequestID || envelope.Error.Code != "upstream_unavailable" {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
}

func TestLogsDoNotContainRawFailureDetailsOrPaths(t *testing.T) {
	// Given
	secret := "fcacr_sensitive_bearer_and_dsn"
	buffer := &bytes.Buffer{}
	app, token := newHostedTestApp(t, secretCapabilitiesProvider{err: errors.New(secret)}, nil, []string{auth.ScopeContextRead}, nil, nil)
	app.logger = testLogger(buffer)
	app.readinessChecks = []ReadinessCheck{CheckFunc{CheckName: "dependency", Fn: func(context.Context) error { return errors.New(secret) }}}

	// When
	app.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
	capabilitiesRequest := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	capabilitiesRequest.Header.Set("Authorization", "Bearer "+token)
	app.Handler().ServeHTTP(httptest.NewRecorder(), capabilitiesRequest)
	app.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/"+secret, nil))

	// Then
	if strings.Contains(buffer.String(), secret) {
		t.Fatalf("log leaked raw request or failure data: %s", buffer.String())
	}
}

type failingCapabilitiesProvider struct{}

func (failingCapabilitiesProvider) Capabilities(context.Context, *http.Request) (contractsv1.Capabilities, error) {
	return contractsv1.Capabilities{}, errors.New("unavailable")
}

type secretCapabilitiesProvider struct{ err error }

func (p secretCapabilitiesProvider) Capabilities(context.Context, *http.Request) (contractsv1.Capabilities, error) {
	return contractsv1.Capabilities{}, p.err
}

func TestRequestIDIsAvailableToDownstreamMiddleware(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	testApp(t).Handler().ServeHTTP(response, request)
	if request.Header.Get("X-Request-ID") != testRequestID {
		t.Fatalf("downstream request header did not receive request ID: %q", request.Header.Get("X-Request-ID"))
	}
	if response.Header().Get("X-Request-ID") != testRequestID {
		t.Fatalf("response header did not receive request ID: %q", response.Header().Get("X-Request-ID"))
	}
}

// brokenResponseWriter simulates exactly the CHAOS-4330 mechanism:
// http.Server.WriteTimeout (or any other mid-write connection failure)
// closes the connection AFTER a handler has already decided its status
// code, so every Write call after that point fails.
type brokenResponseWriter struct {
	http.ResponseWriter
	err error
}

func (b *brokenResponseWriter) Write([]byte) (int, error) { return 0, b.err }

// findLogEntry parses each newline-delimited JSON log line and returns the
// last one whose "msg" matches -- accessLogMiddleware's own line, not any
// other log statement a handler might have already emitted.
func findLogEntry(t *testing.T, buffer *bytes.Buffer, msg string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not valid JSON: %s (%v)", line, err)
		}
		if entry["msg"] == msg {
			found = entry
		}
	}
	if found == nil {
		t.Fatalf("no log line with msg=%q found in: %s", msg, buffer.String())
	}
	return found
}

// CHAOS-4330: the handler decides `status` (here, 200 from handleHealth's
// own writeJSON call) BEFORE the body write that this test forces to fail.
// Before the fix, "request completed" logged status=200 regardless --
// identical to a real success, even though the client received nothing.
func TestAccessLogDoesNotClaimSuccessWhenTheResponseWriteFails(t *testing.T) {
	buffer := &bytes.Buffer{}
	app := testApp(t)
	app.logger = testLogger(buffer)
	rawErrorText := "write tcp 10.0.0.1:8080->10.0.0.2:54321: use of closed network connection"
	broken := &brokenResponseWriter{
		ResponseWriter: httptest.NewRecorder(),
		err:            errors.New(rawErrorText),
	}

	app.Handler().ServeHTTP(broken, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	entry := findLogEntry(t, buffer, "request completed")
	if entry["level"] != "WARN" {
		t.Fatalf("level = %v, want WARN when the response write failed", entry["level"])
	}
	// A classified bucket, never the raw error text (codex review,
	// CHAOS-4330: this repo's own observability rule forbids raw error
	// text as a log attribute -- it can carry a remote address, as the
	// fixture error above deliberately does, to prove this).
	if entry["write_error"] != "write_failed" {
		t.Fatalf("write_error = %v, want the classified bucket \"write_failed\"", entry["write_error"])
	}
	if strings.Contains(fmt.Sprint(entry), "10.0.0.1") {
		t.Fatalf("log entry leaked raw write error text: %#v", entry)
	}
	// status still reports what the handler decided (200) -- this test does
	// not want that changed, only that "request completed" at INFO with no
	// other signal can no longer be read as proof the client received it.
	if entry["status"] != float64(http.StatusOK) {
		t.Fatalf("status = %v, want 200 (unchanged -- write_error is the added signal, not a replaced status)", entry["status"])
	}
}

// A genuinely successful request must NOT gain a write_error field or a
// WARN level -- this is the fix's own negative case, proving it only
// engages on an actual write failure.
func TestAccessLogStaysInfoWhenTheResponseWriteSucceeds(t *testing.T) {
	buffer := &bytes.Buffer{}
	app := testApp(t)
	app.logger = testLogger(buffer)

	app.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	entry := findLogEntry(t, buffer, "request completed")
	if entry["level"] != "INFO" {
		t.Fatalf("level = %v, want INFO on a genuinely successful write", entry["level"])
	}
	if _, ok := entry["write_error"]; ok {
		t.Fatalf("unexpected write_error field on a successful write: %#v", entry)
	}
}
