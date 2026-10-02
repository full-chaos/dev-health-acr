package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"
)

// allowlist is hard-coded for production in main; tests reach a loopback
// variant only through the unexported runConfig field.
type allowlist struct {
	Provider string
	BaseURL  string
	Model    string
	Insecure bool
	Scheme   string
	Host     string
	Path     string
}

var productionAllowlist = allowlist{
	Provider: "openai",
	BaseURL:  "https://api.openai.com/v1/",
	Model:    "gpt-5.6-luna",
	Insecure: false,
	Scheme:   "https",
	Host:     "api.openai.com",
	Path:     "/v1/chat/completions",
}

// Runner-owned outcomes (spec §7): never model failures.
const (
	outcomeContractFailure = "contract_failure"
	outcomeCredentialEcho  = "credential_echo"
	outcomeBudget          = "budget_exhausted"
	outcomeWriteFailure    = "write_failure"
	outcomeAllowlist       = "allowlist_breach"
	outcomeTrace           = "trace_inconsistent"
	outcomeInvalidUTF8     = "invalid_utf8"
	outcomeInterference    = "harness_interference"
)

// runnerOwnedOutcome: incomplete-capture conditions, never model failures.
func runnerOwnedOutcome(o string) bool {
	switch o {
	case outcomeContractFailure, outcomeCredentialEcho, outcomeBudget, outcomeWriteFailure, outcomeAllowlist, outcomeTrace, outcomeInvalidUTF8, outcomeInterference:
		return true
	}
	return false
}

type attemptRecord struct {
	AttemptSeq               int             `json:"attempt_seq"`
	AttemptIndex             int             `json:"attempt_index"`
	Draw                     int             `json:"draw"`
	Sent                     bool            `json:"sent"`
	RequestBodyBase64        *string         `json:"request_body_base64"`
	RequestSHA256            string          `json:"request_sha256"`
	ExpectedDescriptor       json.RawMessage `json:"expected_descriptor"`
	ExpectedDescriptorSHA256 string          `json:"expected_descriptor_sha256"`
	ObservedDescriptor       json.RawMessage `json:"observed_descriptor"`
	ObservedDescriptorSHA256 string          `json:"observed_descriptor_sha256"`
	Status                   int             `json:"status"`
	TransportError           string          `json:"transport_error"`
	ResponseSHA256           string          `json:"response_sha256"`
	ResponseBytes            int             `json:"response_bytes"`
	ResponseBodyBase64       *string         `json:"response_body_base64"`
	SystemFingerprint        string          `json:"system_fingerprint"`
	SentAt                   string          `json:"sent_at"`
	DurationMS               int64           `json:"duration_ms"`
	HarnessOverheadMS        int64           `json:"harness_overhead_ms"`
	// Prompt-variant attempts only (variant.go): the proof that the request
	// production built was the incumbent's, before the appendix was added.
	// request_sha256 and the two descriptors above then describe the request
	// that was SENT. All three are absent from incumbent attempts.
	BaseRequestSHA256            string `json:"base_request_sha256,omitempty"`
	BaseExpectedDescriptorSHA256 string `json:"base_expected_descriptor_sha256,omitempty"`
	BaseObservedDescriptorSHA256 string `json:"base_observed_descriptor_sha256,omitempty"`
}

type drawAuthorization struct {
	AfterAttemptSeq int    `json:"after_attempt_seq"`
	AuthorizedDraw  int    `json:"authorized_draw"`
	RejectionReason string `json:"rejection_reason"`
}

// invocation is the state of one InterpretQuestion call.
type invocation struct {
	pair           pairKey
	runID          string
	question       string
	systemSHA      string
	inputSHA       string
	authorizedDraw int
	latch          string
	attempts       []attemptRecord
	authorizations []drawAuthorization
	overhead       time.Duration
	// variantSystemSHA is the sha256 of the system message a prompt-variant
	// invocation sends; empty for the incumbent.
	variantSystemSHA string
}

// captureTransport never sleeps: pacing happens between invocations,
// before any deadline is armed (sol r1 H4).
type captureTransport struct {
	next    http.RoundTripper
	allow   allowlist
	model   string
	golden  envelopeGolden
	ledger  *approvalLedger
	scanner *credScanner
	now     func() time.Time
	// variant is nil for the incumbent capture: the request production
	// built is then sent as it is.
	variant *promptVariant

	mu       sync.Mutex
	inv      *invocation
	lastSend time.Time
}

func (t *captureTransport) begin(inv *invocation) {
	t.mu.Lock()
	t.inv = inv
	t.mu.Unlock()
}

func (t *captureTransport) end() *invocation {
	t.mu.Lock()
	defer t.mu.Unlock()
	inv := t.inv
	t.inv = nil
	return inv
}

func (t *captureTransport) trip(inv *invocation, reason string) {
	if inv.latch == "" {
		inv.latch = reason
	}
}

var errLatched = errors.New("gocapture: invocation latched; request refused unsent")

// RoundTrip checks the full request descriptor, reserves budget and only
// then sends (spec §6, §7). Any refusal latches the invocation.
func (t *captureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	entered := t.now()
	t.mu.Lock()
	inv := t.inv
	if inv == nil {
		t.mu.Unlock()
		return nil, errors.New("gocapture: HTTP outside an invocation refused")
	}
	if inv.latch != "" {
		t.mu.Unlock()
		return nil, errLatched
	}
	body, err := readAndRestoreRequest(request)
	if err != nil {
		t.trip(inv, outcomeContractFailure)
		t.mu.Unlock()
		return nil, err
	}
	record := attemptRecord{AttemptIndex: len(inv.attempts) + 1, Draw: inv.authorizedDraw, RequestSHA256: sha256Hex(body)}
	if request.URL.Scheme != t.allow.Scheme || request.URL.Host != t.allow.Host || request.URL.Path != t.allow.Path || request.Method != http.MethodPost {
		t.trip(inv, outcomeAllowlist)
		inv.attempts = append(inv.attempts, record)
		t.mu.Unlock()
		return nil, errors.New("gocapture: endpoint outside the allowlist; refused unsent")
	}
	systemSHA := inv.systemSHA
	if t.variant != nil {
		// Prove the production request is the incumbent's, then add the
		// appendix to its system message. From here on body is what is
		// checked, recorded and sent.
		changed, err := t.variant.rewrite(t, inv, body, &record)
		if err != nil {
			if t.scanner.scan(body) {
				t.trip(inv, outcomeCredentialEcho)
			} else {
				encoded := base64.StdEncoding.EncodeToString(body)
				record.RequestBodyBase64 = &encoded
				t.trip(inv, outcomeContractFailure)
			}
			inv.attempts = append(inv.attempts, record)
			t.mu.Unlock()
			return nil, err
		}
		record.BaseRequestSHA256 = record.RequestSHA256
		record.RequestSHA256 = sha256Hex(changed)
		body, systemSHA = changed, inv.variantSystemSHA
		request = withBody(request, changed)
	}
	expected, err := expectedDescriptor(t.golden, t.model, systemSHA, inv.inputSHA, interpretSeed(inv.question, inv.authorizedDraw))
	if err != nil {
		t.trip(inv, outcomeContractFailure)
		t.mu.Unlock()
		return nil, err
	}
	expectedCJ, _ := descriptorCJ(expected)
	record.ExpectedDescriptor = expectedCJ
	record.ExpectedDescriptorSHA256 = sha256Hex(expectedCJ)
	observed, obsErr := observedDescriptor(body)
	if obsErr == nil {
		observedCJ, cjErr := descriptorCJ(observed)
		if cjErr != nil {
			obsErr = cjErr
		} else {
			record.ObservedDescriptor = observedCJ
			record.ObservedDescriptorSHA256 = sha256Hex(observedCJ)
		}
	}
	if t.scanner.scan(body) {
		t.trip(inv, outcomeCredentialEcho)
		inv.attempts = append(inv.attempts, record)
		t.mu.Unlock()
		return nil, errors.New("gocapture: credential in request body; refused unsent")
	}
	encoded := base64.StdEncoding.EncodeToString(body)
	record.RequestBodyBase64 = &encoded
	if obsErr != nil || record.ObservedDescriptorSHA256 != record.ExpectedDescriptorSHA256 {
		t.trip(inv, outcomeContractFailure)
		inv.attempts = append(inv.attempts, record)
		t.mu.Unlock()
		return nil, errors.New("gocapture: request differs from the expected descriptor; refused unsent")
	}
	seq, err := t.ledger.reserveHTTP(inv.pair, inv.runID, inv.authorizedDraw, record.ExpectedDescriptorSHA256)
	if err != nil {
		if errors.Is(err, errBudget) {
			t.trip(inv, outcomeBudget)
		} else {
			t.trip(inv, outcomeWriteFailure)
		}
		inv.attempts = append(inv.attempts, record)
		t.mu.Unlock()
		return nil, err
	}
	record.AttemptSeq = seq
	started := t.now()
	pre := started.Sub(entered)
	inv.overhead += pre
	record.HarnessOverheadMS = pre.Milliseconds()
	t.lastSend = started
	record.Sent = true
	record.SentAt = started.UTC().Format(time.RFC3339Nano)
	index := len(inv.attempts)
	inv.attempts = append(inv.attempts, record)
	t.mu.Unlock()

	response, sendErr := t.next.RoundTrip(request)
	var responseBody []byte
	var readErr error
	if sendErr == nil {
		responseBody, readErr = readAndRestoreResponse(response)
	}
	providerDone := t.now()

	t.mu.Lock()
	defer t.mu.Unlock()
	rec := &inv.attempts[index]
	rec.DurationMS = providerDone.Sub(started).Milliseconds()
	// Harness time after the provider answered, inside the same deadlines.
	defer func() {
		post := t.now().Sub(providerDone)
		inv.overhead += post
		rec.HarnessOverheadMS += post.Milliseconds()
	}()
	if sendErr != nil {
		rec.TransportError = transportClass(sendErr)
		return nil, sendErr
	}
	if readErr != nil {
		rec.TransportError = "read_error"
		return nil, readErr
	}
	rec.Status = response.StatusCode
	rec.ResponseSHA256 = sha256Hex(responseBody)
	rec.ResponseBytes = len(responseBody)
	// Scan every body, 2xx or not, in every decoded form, before anything
	// is kept (spec §6, sol r1 H1).
	if t.scanner.scan(responseBody) {
		t.trip(inv, outcomeCredentialEcho)
		return nil, errors.New("gocapture: credential echoed by the provider; content suppressed")
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		t.trip(inv, outcomeAllowlist)
		return nil, errors.New("gocapture: redirect refused")
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 && !utf8.Valid(responseBody) {
		t.trip(inv, outcomeInvalidUTF8)
		return nil, errors.New("gocapture: response body is not valid UTF-8")
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		keep := base64.StdEncoding.EncodeToString(responseBody)
		rec.ResponseBodyBase64 = &keep
		var fp struct {
			SystemFingerprint string `json:"system_fingerprint"`
		}
		if json.Unmarshal(responseBody, &fp) == nil {
			rec.SystemFingerprint = fp.SystemFingerprint
		}
	}
	return response, nil
}

// authorize is called from the runtime's own rejection event: the ONLY way
// the draw index advances. A seed on the wire never authorizes itself.
func (t *captureTransport) authorize(sample int, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	inv := t.inv
	if inv == nil || inv.latch != "" {
		return
	}
	if sample != inv.authorizedDraw {
		t.trip(inv, outcomeContractFailure)
		return
	}
	last := 0
	if n := len(inv.attempts); n > 0 {
		last = inv.attempts[n-1].AttemptSeq
	}
	inv.authorizedDraw = sample + 1
	inv.authorizations = append(inv.authorizations, drawAuthorization{AfterAttemptSeq: last, AuthorizedDraw: sample + 1, RejectionReason: reason})
}

func transportClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	return "transport_error"
}

func readAndRestoreRequest(request *http.Request) ([]byte, error) {
	if request.Body == nil {
		return nil, errors.New("request has no body")
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	_ = request.Body.Close()
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return body, nil
}

func readAndRestoreResponse(response *http.Response) ([]byte, error) {
	if response.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	_ = response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	return body, nil
}

// drawObserver is the runtime's logger. It keeps no content: it reacts only
// to the fixed rejection message (runtime.go rejectedInterpretDrawMessage)
// and forwards nothing else anywhere.
type drawObserver struct {
	transport *captureTransport
}

const rejectedInterpretDrawMessage = "context fabric interpret draw rejected"

func (o *drawObserver) Enabled(context.Context, slog.Level) bool { return true }
func (o *drawObserver) Handle(_ context.Context, record slog.Record) error {
	if record.Message != rejectedInterpretDrawMessage {
		return nil
	}
	sample, haveSample, reason := 0, false, ""
	record.Attrs(func(attr slog.Attr) bool {
		switch attr.Key {
		case "sample":
			if attr.Value.Kind() == slog.KindInt64 {
				sample, haveSample = int(attr.Value.Int64()), true
			}
		case "rejection_reason":
			reason = closedToken(attr.Value.String())
		}
		return true
	})
	if haveSample {
		o.transport.authorize(sample, reason)
	}
	return nil
}
func (o *drawObserver) WithAttrs([]slog.Attr) slog.Handler { return o }
func (o *drawObserver) WithGroup(string) slog.Handler      { return o }

// closedToken keeps a short identifier-like value only.
func closedToken(s string) string {
	if len(s) > 64 {
		return "other"
	}
	for _, c := range s {
		if !(c == '_' || c == '-' || c == '.' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return "other"
		}
	}
	return s
}

func installDefaultTransport(replacement http.RoundTripper) func() {
	previous := http.DefaultTransport
	http.DefaultTransport = replacement
	return func() { http.DefaultTransport = previous }
}
