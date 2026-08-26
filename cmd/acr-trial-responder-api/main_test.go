package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// fakeChatCompletionsServer is a minimal OpenAI-Chat-Completions-shaped
// HTTP server (CHAOS-4313 acceptance: "a fake exchange dir with N requests
// is fully answered; output validates against output_schema; DONE handling
// and nonce echo proven"). It answers every request with a fixed JSON
// object satisfying the schema this test's requests carry, so the test
// never touches the real network or a live key.
func fakeChatCompletionsServer(t *testing.T, answer string) (*httptest.Server, *int64) {
	t.Helper()
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		if r.URL.Path != "/chat/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		resp := map[string]any{
			"id":      "chatcmpl-fake",
			"object":  "chat.completion",
			"created": 0,
			"model":   "gpt-5.6-luna",
			"choices": []map[string]any{
				{
					"index":         0,
					"finish_reason": "stop",
					"message": map[string]any{
						"role":    "assistant",
						"content": answer,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	return srv, &calls
}

// writeExchangeRequest publishes a request file the SAME way
// file_exchange_runtime_test.go's exchange() does (temp+rename), so the
// responder under test exercises the identical discovery path a real
// go-test-side exchange() call would produce.
func writeExchangeRequest(t *testing.T, dir string, seq int, nonce string) {
	t.Helper()
	name := fmt.Sprintf("%06d-interpret.json", seq)
	req := exchangeRequest{
		Operation:    "interpret",
		Seq:          int64(seq),
		SessionNonce: nonce,
		System:       "system prompt",
		Prompt:       "user prompt",
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`),
		Instructions: "produce JSON matching output_schema; echo session_nonce",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "requests", name), body, 0o644); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

func newTestResponder(t *testing.T, dir, baseURL string) *responder {
	t.Helper()
	client := openai.NewClient(
		option.WithAPIKey("test-key-not-a-real-secret"),
		option.WithBaseURL(baseURL),
		option.WithMaxRetries(0),
	)
	return &responder{
		client:                    client,
		model:                     "gpt-5.6-luna",
		exchangeDir:               dir,
		poll:                      20 * time.Millisecond,
		requestTimeout:            5 * time.Second,
		attempts:                  map[string]int{},
		loggedStrictNormalization: map[string]bool{},
	}
}

// TestContractFullyAnswersEveryPublishedRequest is the CHAOS-4313
// acceptance contract test: N requests, all answered, each output
// validates against the request's own output_schema (has the required
// "answer" field), and DONE handling: the run loop only exits once DONE
// exists AND every request has a response.
func TestContractFullyAnswersEveryPublishedRequest(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"requests", "responses"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	const n = 5
	nonces := make([]string, n)
	for i := 1; i <= n; i++ {
		nonces[i-1] = fmt.Sprintf("nonce-%d", i)
		writeExchangeRequest(t, dir, i, nonces[i-1])
	}

	srv, calls := fakeChatCompletionsServer(t, `{"answer":"ok"}`)
	defer srv.Close()
	r := newTestResponder(t, dir, srv.URL)

	// DONE is published BEFORE run() starts (mirrors run-responder-codex.sh's
	// own trap-driven "touch DONE" happening while the responder may still
	// have pending work) -- proves run() does not exit early just because
	// DONE exists; it must still answer every pending request first.
	if err := os.WriteFile(filepath.Join(dir, "DONE"), nil, 0o644); err != nil {
		t.Fatalf("touch DONE: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- r.run(t.Context()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not exit within 10s -- DONE handling is broken")
	}

	if got := atomic.LoadInt64(calls); got != n {
		t.Errorf("fake server received %d calls, want %d (every published request answered exactly once)", got, n)
	}

	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("%06d-interpret.json", i)
		raw, err := os.ReadFile(filepath.Join(dir, "responses", name))
		if err != nil {
			t.Fatalf("response %s not written: %v", name, err)
		}
		var resp exchangeResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("response %s not valid JSON: %v", name, err)
		}
		if resp.Error != "" {
			t.Fatalf("response %s carries error=%q, want a successful output", name, resp.Error)
		}
		// Nonce echo (CHAOS-4313 acceptance: "nonce echo proven").
		if resp.SessionNonce != nonces[i-1] {
			t.Errorf("response %s session_nonce = %q, want %q", name, resp.SessionNonce, nonces[i-1])
		}
		// Output validates against output_schema: the schema requires an
		// "answer" string field.
		var out struct {
			Answer string `json:"answer"`
		}
		if err := json.Unmarshal(resp.Output, &out); err != nil {
			t.Fatalf("response %s output does not decode: %v", name, err)
		}
		if out.Answer != "ok" {
			t.Errorf("response %s output.answer = %q, want %q", name, out.Answer, "ok")
		}
	}
}

// TestContractWaitsForDoneAndPendingBeforeExiting proves the SPECIFIC
// ordering the acceptance criterion names: a request published AFTER DONE
// still gets answered before run() returns (DONE alone is not enough --
// pending must ALSO reach zero).
func TestContractWaitsForDoneAndPendingBeforeExiting(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"requests", "responses"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	srv, calls := fakeChatCompletionsServer(t, `{"answer":"late"}`)
	defer srv.Close()
	r := newTestResponder(t, dir, srv.URL)

	done := make(chan error, 1)
	go func() { done <- r.run(t.Context()) }()

	// Give run() a moment to enter its poll loop with nothing to do, then
	// publish a request and DONE together -- if run() exited on an empty
	// requests dir alone (DONE not yet checked, or checked before this
	// write raced in) this would be flaky; instead we publish, wait for
	// the answer, THEN touch DONE, proving both conditions are required.
	time.Sleep(60 * time.Millisecond)
	writeExchangeRequest(t, dir, 1, "late-nonce")

	deadline := time.Now().Add(5 * time.Second)
	for {
		if atomic.LoadInt64(calls) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("responder never answered the late-published request")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(dir, "DONE"), nil, 0o644); err != nil {
		t.Fatalf("touch DONE: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not exit after DONE + pending==0")
	}
}

// TestClassifyErrorTerminalVsRetryable pins the closed-vocabulary
// class/terminal split classifyError makes -- the exact contract the
// answerOne fail-fast-vs-retry decision depends on.
func TestClassifyErrorTerminalVsRetryable(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		wantClass    string
		wantTerminal bool
	}{
		{"rate limited", &openai.Error{StatusCode: http.StatusTooManyRequests}, classRateLimited, false},
		{"server error", &openai.Error{StatusCode: http.StatusInternalServerError}, classServerError, false},
		{"unauthorized", &openai.Error{StatusCode: http.StatusUnauthorized}, classAuthError, true},
		{"forbidden", &openai.Error{StatusCode: http.StatusForbidden}, classAuthError, true},
		{"bad request", &openai.Error{StatusCode: http.StatusBadRequest}, classInvalidRequest, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotClass, gotTerminal := classifyError(tc.err)
			if gotClass != tc.wantClass || gotTerminal != tc.wantTerminal {
				t.Errorf("classifyError(%v) = (%q, %v), want (%q, %v)", tc.err, gotClass, gotTerminal, tc.wantClass, tc.wantTerminal)
			}
		})
	}
}

// TestAnswerOneWritesNoResponseOnRetryableFailure is the red-first proof
// backing answerOne's fail-fast-vs-retry split: a 429 must leave the
// request UNANSWERED (so run()'s own poll loop retries it), never write a
// premature error response.
func TestAnswerOneWritesNoResponseOnRetryableFailure(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"requests", "responses", "_responder_logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	}))
	defer srv.Close()
	r := newTestResponder(t, dir, srv.URL)
	writeExchangeRequest(t, dir, 1, "n1")

	reqPath := filepath.Join(dir, "requests", "000001-interpret.json")
	respPath := filepath.Join(dir, "responses", "000001-interpret.json")
	r.answerOne(t.Context(), reqPath, respPath, filepath.Join(dir, "_responder_logs"))

	if _, err := os.Stat(respPath); err == nil {
		t.Fatal("answerOne wrote a response for a rate-limited (retryable) call -- must leave it unanswered for the next poll tick")
	}
}

// TestAnswerOneFailsFastOnAuthError is the corresponding green case: a
// terminal failure (401) MUST write an error response immediately, with a
// closed-vocabulary class, never the provider's raw body.
func TestAnswerOneFailsFastOnAuthError(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"requests", "responses", "_responder_logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-secret-corpus-leak-token","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()
	r := newTestResponder(t, dir, srv.URL)
	writeExchangeRequest(t, dir, 1, "n1")

	reqPath := filepath.Join(dir, "requests", "000001-interpret.json")
	respPath := filepath.Join(dir, "responses", "000001-interpret.json")
	r.answerOne(t.Context(), reqPath, respPath, filepath.Join(dir, "_responder_logs"))

	raw, err := os.ReadFile(respPath)
	if err != nil {
		t.Fatalf("answerOne did not write a response for a terminal (401) failure: %v", err)
	}
	var resp exchangeResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if resp.Error != classAuthError {
		t.Errorf("response error = %q, want closed-vocabulary class %q", resp.Error, classAuthError)
	}
	if resp.SessionNonce != "n1" {
		t.Errorf("response session_nonce = %q, want echoed %q even on error", resp.SessionNonce, "n1")
	}
	if strings.Contains(resp.Error, "sk-secret-corpus-leak-token") {
		t.Fatal("response error field leaked the provider's raw error body -- must be the closed-vocabulary class only")
	}
}

// TestAnswerOneSendsInstructionsToModel is the codex xhigh review round 1
// (High, confirmed) red-first proof: an earlier draft decoded
// exchangeRequest.Instructions but never included it in the request sent
// to OpenAI, silently dropping the envelope's own grounding guidance
// ("base your answer ONLY on system and prompt"). Captures the ACTUAL
// request body the fake server receives and asserts the instructions text
// appears in it as a distinct message.
func TestAnswerOneSendsInstructionsToModel(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"requests", "responses", "_responder_logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		capturedBody = body
		resp := map[string]any{
			"id": "chatcmpl-fake", "object": "chat.completion", "created": 0, "model": "gpt-5.6-luna",
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": `{"answer":"ok"}`}}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	r := newTestResponder(t, dir, srv.URL)
	writeExchangeRequest(t, dir, 1, "n1")

	reqPath := filepath.Join(dir, "requests", "000001-interpret.json")
	respPath := filepath.Join(dir, "responses", "000001-interpret.json")
	r.answerOne(t.Context(), reqPath, respPath, filepath.Join(dir, "_responder_logs"))

	if capturedBody == nil {
		t.Fatal("fake server never received a request -- answerOne did not call the API at all")
	}
	const wantInstructions = "produce JSON matching output_schema; echo session_nonce"
	if !strings.Contains(string(capturedBody), wantInstructions) {
		t.Fatalf("request body does not contain the request's own instructions text %q -- instructions were decoded but never sent to the model.\nbody: %s", wantInstructions, capturedBody)
	}
}

// TestResolveResponderEffort (codex xhigh review round 1, High, confirmed)
// is the red-first proof that ACR_TEST_TRIAL_RESPONDER_EFFORT is bounded
// before it reaches any of its three sinks (the OpenAI request, this
// process's own stdout, the provenance artifact) -- see
// resolveResponderEffort's own doc comment for the exact reasoning.
func TestResolveResponderEffort(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "unset", raw: "", want: ""},
		{name: "whitespace-only", raw: "   ", want: ""},
		{name: "known tier", raw: "medium", want: "medium"},
		{name: "internal tier name outside the SDK enum", raw: "xhigh", want: "xhigh"},
		{name: "trims surrounding whitespace", raw: "  high  ", want: "high"},
		{name: "underscore and hyphen allowed", raw: "tier_9-x", want: "tier_9-x"},
		{name: "exactly 32 chars is the boundary, still valid", raw: strings.Repeat("a", 32), want: strings.Repeat("a", 32)},
		{name: "33 chars is one over the boundary, rejected", raw: strings.Repeat("a", 33), wantErr: true},
		{name: "embedded space is rejected", raw: "very high", wantErr: true},
		{name: "control character is rejected", raw: "high\x00", wantErr: true},
		{name: "embedded newline is rejected (surrounding whitespace is trimmed first, this one is not)", raw: "hi\ngh", wantErr: true},
		{name: "long garbage/secret-shaped value is rejected", raw: "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789ABCDEF", wantErr: true},
		{name: "punctuation outside the allowed set is rejected", raw: "high!", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveResponderEffort(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveResponderEffort(%q) = %q, nil -- want an error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveResponderEffort(%q) unexpected error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("resolveResponderEffort(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestAnswerOneSendsReasoningEffortWhenSet (CHAOS-4313 follow-up) is the
// red-first proof for the ACR_TEST_TRIAL_RESPONDER_EFFORT passthrough: when
// r.effort is non-empty, the request body actually sent to the API must
// carry it as "reasoning_effort" -- a knob that exists but is never wired
// into the request is worse than no knob (silently misleads provenance
// AND the live comparison run chris asked for).
func TestAnswerOneSendsReasoningEffortWhenSet(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"requests", "responses", "_responder_logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		capturedBody = body
		resp := map[string]any{
			"id": "chatcmpl-fake", "object": "chat.completion", "created": 0, "model": "gpt-5.6-luna",
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": `{"answer":"ok"}`}}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	r := newTestResponder(t, dir, srv.URL)
	r.effort = "xhigh"
	writeExchangeRequest(t, dir, 1, "n1")

	reqPath := filepath.Join(dir, "requests", "000001-interpret.json")
	respPath := filepath.Join(dir, "responses", "000001-interpret.json")
	r.answerOne(t.Context(), reqPath, respPath, filepath.Join(dir, "_responder_logs"))

	if capturedBody == nil {
		t.Fatal("fake server never received a request -- answerOne did not call the API at all")
	}
	if !strings.Contains(string(capturedBody), `"reasoning_effort":"xhigh"`) {
		t.Fatalf("request body does not contain reasoning_effort=xhigh -- r.effort was set but never reached the API call.\nbody: %s", capturedBody)
	}
}

// TestAnswerOneOmitsReasoningEffortWhenUnset (CHAOS-4313 follow-up) is the
// companion red-first proof for the opposite direction: an empty r.effort
// (the default, matching every case-57 run before this knob existed) must
// send NO reasoning_effort field at all, not an empty-string one -- an
// explicit empty value is a different (and likely rejected) request shape
// than never sending the field, and this is the provenance-neutral case
// every existing run's artifact already depends on.
func TestAnswerOneOmitsReasoningEffortWhenUnset(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"requests", "responses", "_responder_logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		capturedBody = body
		resp := map[string]any{
			"id": "chatcmpl-fake", "object": "chat.completion", "created": 0, "model": "gpt-5.6-luna",
			"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": `{"answer":"ok"}`}}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	r := newTestResponder(t, dir, srv.URL) // r.effort left at its zero value ""
	writeExchangeRequest(t, dir, 1, "n1")

	reqPath := filepath.Join(dir, "requests", "000001-interpret.json")
	respPath := filepath.Join(dir, "responses", "000001-interpret.json")
	r.answerOne(t.Context(), reqPath, respPath, filepath.Join(dir, "_responder_logs"))

	if capturedBody == nil {
		t.Fatal("fake server never received a request -- answerOne did not call the API at all")
	}
	if strings.Contains(string(capturedBody), "reasoning_effort") {
		t.Fatalf("request body contains reasoning_effort even though r.effort was never set -- want the field omitted entirely.\nbody: %s", capturedBody)
	}
}

// TestAnswerOneRejectsOutputNotMatchingSchema is the codex xhigh review
// round 1 (High, confirmed) red-first proof: json.Valid alone accepts ANY
// well-formed JSON value, including one that does not satisfy the
// request's own output_schema (here: missing the required "answer"
// field). Before local gojsonschema validation existed, this published as
// a false success. A live acceptance run (CHAOS-4313 case 57, kiac)
// later found this needs to be RETRYABLE, not immediately terminal --
// schema-non-conformant content is the model's own sampled output (this
// schema is not strict-mode-compatible, see response_format's own comment
// in answerOne, so nothing grammar-constrains the model), and a fresh
// sampling attempt against the SAME request can plausibly produce
// compliant output where the first attempt did not. So: one attempt must
// NOT publish the mismatched output as (or alongside) a success, and must
// leave the request unanswered for the next poll tick; only after
// defaultMaxAnswerAttempts does it become a terminal
// classRetriesExhausted response -- still never Output alongside it.
func TestAnswerOneRejectsOutputNotMatchingSchema(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"requests", "responses", "_responder_logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	// Well-formed JSON, but missing the schema's required "answer" field --
	// json.Valid alone would accept this.
	srv, _ := fakeChatCompletionsServer(t, `{"unrelated_field":"ok"}`)
	defer srv.Close()
	r := newTestResponder(t, dir, srv.URL)
	writeExchangeRequest(t, dir, 1, "n1")

	reqPath := filepath.Join(dir, "requests", "000001-interpret.json")
	respPath := filepath.Join(dir, "responses", "000001-interpret.json")

	r.answerOne(t.Context(), reqPath, respPath, filepath.Join(dir, "_responder_logs"))
	if _, err := os.Stat(respPath); err == nil {
		t.Fatal("answerOne published a response after a single schema-mismatched attempt -- must be retried, not treated as terminal, on the first miss")
	}

	for i := 1; i < defaultMaxAnswerAttempts; i++ {
		r.answerOne(t.Context(), reqPath, respPath, filepath.Join(dir, "_responder_logs"))
	}

	raw, err := os.ReadFile(respPath)
	if err != nil {
		t.Fatalf("answerOne did not write a response after %d attempts of persistent schema-invalid output: %v", defaultMaxAnswerAttempts, err)
	}
	var resp exchangeResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if resp.Error != classRetriesExhausted {
		t.Errorf("response error = %q, want %q (persistent schema-invalid output exhausted every retry)", resp.Error, classRetriesExhausted)
	}
	if resp.Output != nil {
		t.Errorf("response carries Output %s alongside an error -- schema-invalid output must never be published as a success", resp.Output)
	}
}

// TestClassifyErrorJSONDecodeIsTerminalInvalidOutput is the codex xhigh
// review round 1 (High, confirmed) proof that a malformed-2xx-body decode
// error is classified as a TERMINAL classInvalidOutput, not the old
// catch-all classNetworkError (retryable) -- a server that keeps returning
// unparseable JSON for a given request shape will keep doing so, so
// retrying only burns attempts toward defaultMaxAnswerAttempts without
// ever converging. Mirrors embedprovider.IsTransientEmbedError's own
// identical classification for the identical error shapes.
func TestClassifyErrorJSONDecodeIsTerminalInvalidOutput(t *testing.T) {
	var syntaxErr *json.SyntaxError
	if err := json.Unmarshal([]byte("{not valid json"), &struct{}{}); err != nil {
		if !errors.As(err, &syntaxErr) {
			t.Fatalf("test setup invariant broken: json.Unmarshal error is not a *json.SyntaxError: %T", err)
		}
	} else {
		t.Fatal("test setup invariant broken: malformed JSON did not produce an error")
	}

	cases := []struct {
		name string
		err  error
	}{
		{"syntax error", syntaxErr},
		{"unexpected EOF", io.ErrUnexpectedEOF},
		{"EOF", io.EOF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotClass, gotTerminal := classifyError(tc.err)
			if gotClass != classInvalidOutput || !gotTerminal {
				t.Errorf("classifyError(%v) = (%q, %v), want (%q, true)", tc.err, gotClass, gotTerminal, classInvalidOutput)
			}
		})
	}
}

// TestClassifyError408IsRetryableNetworkError is the codex xhigh review
// round 1 (High, confirmed) proof: HTTP 408 (Request Timeout) says the
// server gave up waiting on THIS request, which says nothing about
// whether an identical retry would fail the same way -- it must NOT fall
// into the switch's terminal default branch the way a genuine 4xx
// request-shape error does.
func TestClassifyError408IsRetryableNetworkError(t *testing.T) {
	gotClass, gotTerminal := classifyError(&openai.Error{StatusCode: http.StatusRequestTimeout})
	if gotClass != classNetworkError || gotTerminal {
		t.Errorf("classifyError(408) = (%q, %v), want (%q, false)", gotClass, gotTerminal, classNetworkError)
	}
}

// TestRetryOrExhaustGivesUpAfterMaxAttempts is the codex xhigh review round
// 1 (High, confirmed) red-first proof: before this existed, a persistently
// transient failure left the request unanswered FOREVER, so run()'s own
// `DONE && pending == 0` exit condition could never fire and the
// launcher's cleanup trap (`wait "$responder_pid"`) would hang. Calling
// retryOrExhaust defaultMaxAnswerAttempts times must write a terminal
// classRetriesExhausted response on (and only on) the LAST call.
func TestRetryOrExhaustGivesUpAfterMaxAttempts(t *testing.T) {
	dir := t.TempDir()
	r := &responder{attempts: map[string]int{}}
	respPath := filepath.Join(dir, "resp.json")
	noop := func(string, ...any) {}

	for i := 1; i < defaultMaxAnswerAttempts; i++ {
		r.retryOrExhaust(respPath, "req.json", "n1", noop)
		if _, err := os.Stat(respPath); err == nil {
			t.Fatalf("retryOrExhaust wrote a response on attempt %d/%d -- must wait until defaultMaxAnswerAttempts (%d)", i, defaultMaxAnswerAttempts, defaultMaxAnswerAttempts)
		}
	}
	r.retryOrExhaust(respPath, "req.json", "n1", noop)
	raw, err := os.ReadFile(respPath)
	if err != nil {
		t.Fatalf("retryOrExhaust did not write a response after %d attempts: %v", defaultMaxAnswerAttempts, err)
	}
	var resp exchangeResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if resp.Error != classRetriesExhausted {
		t.Errorf("response error = %q, want %q", resp.Error, classRetriesExhausted)
	}
	if resp.SessionNonce != "n1" {
		t.Errorf("response session_nonce = %q, want %q", resp.SessionNonce, "n1")
	}
}

// TestResponseAnswersRequestRejectsNonceMismatch is the codex xhigh review
// round 1 (Medium, confirmed) red-first proof: a stale response file left
// over from a different session (mismatched session_nonce) must NOT be
// treated as answering the current request -- a bare os.Stat used to
// accept it regardless.
func TestResponseAnswersRequestRejectsNonceMismatch(t *testing.T) {
	dir := t.TempDir()
	reqPath := filepath.Join(dir, "000001-interpret.json")
	respPath := filepath.Join(dir, "000001-interpret.resp.json")
	req := exchangeRequest{SessionNonce: "current-nonce", Operation: "interpret"}
	reqBody, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := os.WriteFile(reqPath, reqBody, 0o644); err != nil {
		t.Fatalf("write request: %v", err)
	}

	stale := exchangeResponse{SessionNonce: "stale-nonce", Output: json.RawMessage(`{"answer":"old"}`)}
	staleBody, err := json.Marshal(stale)
	if err != nil {
		t.Fatalf("marshal stale response: %v", err)
	}
	if err := os.WriteFile(respPath, staleBody, 0o644); err != nil {
		t.Fatalf("write stale response: %v", err)
	}
	if responseAnswersRequest(reqPath, respPath) {
		t.Fatal("responseAnswersRequest accepted a response whose session_nonce does not match the request's own -- a stale/mismatched response must never be treated as answered")
	}

	matching := exchangeResponse{SessionNonce: "current-nonce", Output: json.RawMessage(`{"answer":"new"}`)}
	matchingBody, err := json.Marshal(matching)
	if err != nil {
		t.Fatalf("marshal matching response: %v", err)
	}
	if err := os.WriteFile(respPath, matchingBody, 0o644); err != nil {
		t.Fatalf("write matching response: %v", err)
	}
	if !responseAnswersRequest(reqPath, respPath) {
		t.Fatal("responseAnswersRequest rejected a response whose session_nonce DOES match the request's own")
	}
}

// TestClassifyErrorApijsonDecodeIsTerminalInvalidOutput is the codex xhigh
// review round 2 (High, confirmed) red-first proof: the openai-go SDK's
// own response-body decoder (internal/apijson) does not use the standard
// library's *json.SyntaxError/*json.UnmarshalTypeError for a
// well-formed-JSON-but-wrong-shape 2xx body -- it returns a bare
// fmt.Errorf("apijson: ...") with nothing to errors.As/errors.Is against,
// so round 1's decode-error checks never matched it and it fell through to
// the generic retryable network_error default, exactly the "keeps
// returning unparseable JSON forever" persistent-failure class those
// checks exist to catch.
func TestClassifyErrorApijsonDecodeIsTerminalInvalidOutput(t *testing.T) {
	err := fmt.Errorf("apijson: could not deserialize to an array")
	gotClass, gotTerminal := classifyError(err)
	if gotClass != classInvalidOutput || !gotTerminal {
		t.Errorf("classifyError(%v) = (%q, %v), want (%q, true)", err, gotClass, gotTerminal, classInvalidOutput)
	}
}

// TestResponseAnswersRequestRecognizesItsOwnExhaustedGiveUp is the codex
// xhigh review round 2 (High, confirmed) red-first proof: retryOrExhaust's
// give-up write can carry an EMPTY SessionNonce (a request read/parse
// failure never got far enough to learn it), which the round-1 nonce-match
// check in responseAnswersRequest treated as "not answered" -- forever.
// Every subsequent poll tick then re-ran answerOne, which failed the same
// read, called retryOrExhaust again (already past
// defaultMaxAnswerAttempts), and rewrote the identical exhausted response
// every tick -- run()'s own `pending == 0` exit condition could never be
// satisfied for this request.
func TestResponseAnswersRequestRecognizesItsOwnExhaustedGiveUp(t *testing.T) {
	dir := t.TempDir()
	reqPath := filepath.Join(dir, "000001-interpret.json")
	respPath := filepath.Join(dir, "000001-interpret.resp.json")

	// The request file is deliberately NEVER written -- mirrors the real
	// failure mode (os.ReadFile(reqPath) itself failed), the exact case
	// retryOrExhaust's own empty-nonce parameter documents.
	r := &responder{attempts: map[string]int{}}
	noop := func(string, ...any) {}
	for i := 0; i < defaultMaxAnswerAttempts; i++ {
		r.retryOrExhaust(respPath, "000001-interpret.json", "", noop)
	}
	if _, err := os.Stat(respPath); err != nil {
		t.Fatalf("test setup invariant broken: retryOrExhaust did not write the exhausted response: %v", err)
	}

	if !responseAnswersRequest(reqPath, respPath) {
		t.Fatal("responseAnswersRequest did not recognize its own classRetriesExhausted give-up as answered -- run() would retry this request's failing read forever")
	}
}

// TestResponseAnswersRequestStillRejectsExhaustedWithMismatchedNonce is the
// codex xhigh review round 3 (Medium, confirmed) red-first proof that the
// round-2 exhaustion carve-out was too broad: an earlier version accepted
// ANY classRetriesExhausted response unconditionally, regardless of nonce
// -- reopening round 1's own stale-response bug for a stale exhausted
// response left over from a DIFFERENT session (a non-empty, mismatched
// nonce), which would then be silently accepted as answering the CURRENT
// request. The carve-out must be scoped to exactly the empty-nonce case
// (a request read/parse failure that never learned a nonce at all); an
// exhausted response WITH a real, non-matching nonce still goes through
// the ordinary nonce-match rejection.
func TestResponseAnswersRequestStillRejectsExhaustedWithMismatchedNonce(t *testing.T) {
	dir := t.TempDir()
	reqPath := filepath.Join(dir, "000001-interpret.json")
	respPath := filepath.Join(dir, "000001-interpret.resp.json")

	req := exchangeRequest{SessionNonce: "current-nonce", Operation: "interpret"}
	reqBody, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := os.WriteFile(reqPath, reqBody, 0o644); err != nil {
		t.Fatalf("write request: %v", err)
	}

	staleExhausted := exchangeResponse{SessionNonce: "stale-nonce", Error: classRetriesExhausted}
	staleBody, err := json.Marshal(staleExhausted)
	if err != nil {
		t.Fatalf("marshal stale exhausted response: %v", err)
	}
	if err := os.WriteFile(respPath, staleBody, 0o644); err != nil {
		t.Fatalf("write stale exhausted response: %v", err)
	}

	if responseAnswersRequest(reqPath, respPath) {
		t.Fatal("responseAnswersRequest accepted a STALE classRetriesExhausted response with a mismatched non-empty session_nonce -- the exhaustion carve-out must be scoped to the empty-nonce case only")
	}
}

// TestAnswerOneRetriesMalformedModelJSON is the live acceptance-run
// finding (CHAOS-4313 case 57, kiac) red-first proof that malformed JSON
// from the MODEL's own sampled content (never a decode error at the SDK
// level -- json.Valid on msg.Content) is retryable, not immediately
// terminal, for the same reason schema-mismatched content is (see
// TestAnswerOneRejectsOutputNotMatchingSchema's own doc comment): a fresh
// sampling attempt can plausibly produce well-formed JSON where the first
// did not, since nothing grammar-constrains the model here.
// TestAnswerOneTreatsExplicitNullAsOmittedForOptionalFields is the
// team-lead-directed lossless-strict-mode fix's own red-first proof:
// strict mode's compiled schema can only express "this optional field is
// absent" as an explicit JSON null (every property must be present under
// strict mode) -- but the ORIGINAL schema this test's request carries
// declares "note" as a bare {"type":"string"} (not required, and NOT
// nullable in its own right), the shape genkitruntime's real schemas use
// for every optional field. Without stripping null-valued keys before
// post-response validation, a model response that correctly omits an
// optional field the ONLY way strict mode allows (explicit null) would be
// rejected as violating the original schema's own type constraint --
// exactly the failure mode a live case-57 kiac run measured (0/1
// exchanges succeeding under strict mode before this fix).
func TestAnswerOneTreatsExplicitNullAsOmittedForOptionalFields(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"requests", "responses", "_responder_logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	srv, _ := fakeChatCompletionsServer(t, `{"answer":"ok","note":null}`)
	defer srv.Close()
	r := newTestResponder(t, dir, srv.URL)

	name := "000001-interpret.json"
	req := exchangeRequest{
		Operation:    "interpret",
		Seq:          1,
		SessionNonce: "n1",
		System:       "system prompt",
		Prompt:       "user prompt",
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"answer":{"type":"string"},"note":{"type":"string"}},"required":["answer"]}`),
		Instructions: "produce JSON matching output_schema",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "requests", name), body, 0o644); err != nil {
		t.Fatalf("write request: %v", err)
	}

	reqPath := filepath.Join(dir, "requests", name)
	respPath := filepath.Join(dir, "responses", name)
	r.answerOne(t.Context(), reqPath, respPath, filepath.Join(dir, "_responder_logs"))

	raw, err := os.ReadFile(respPath)
	if err != nil {
		t.Fatalf("answerOne did not publish a response for an explicit-null optional field: %v", err)
	}
	var resp exchangeResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("response carries error=%q, want a successful output (explicit null on an optional, non-nullable-typed field must be treated as omitted)", resp.Error)
	}
	if resp.Output == nil {
		t.Fatal("response carries no Output despite no error")
	}
}

func TestAnswerOneRetriesMalformedModelJSON(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"requests", "responses", "_responder_logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	srv, _ := fakeChatCompletionsServer(t, `{"answer": not valid json`)
	defer srv.Close()
	r := newTestResponder(t, dir, srv.URL)
	writeExchangeRequest(t, dir, 1, "n1")

	reqPath := filepath.Join(dir, "requests", "000001-interpret.json")
	respPath := filepath.Join(dir, "responses", "000001-interpret.json")
	r.answerOne(t.Context(), reqPath, respPath, filepath.Join(dir, "_responder_logs"))

	if _, err := os.Stat(respPath); err == nil {
		t.Fatal("answerOne published a response after a single malformed-JSON attempt -- must be retried, not treated as terminal, on the first miss")
	}
}
