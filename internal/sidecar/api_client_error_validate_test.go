package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// These tests prove decodeAPIError/validateErrorEnvelope reject every
// error.v1.schema.json-invalid envelope the real HTTP path can receive,
// mirroring api_client_validate_parity_test.go's schema-parity pattern for
// the three success-response types: every mutation is checked against the
// canonical JSON Schema first (assertSchemaRejects), then against the
// actual client boundary, so a future change that weakens
// validateErrorEnvelope relative to the schema fails here first.
//
// The Unicode/UTF-8 boundary-precision half of this suite (multiByteChar,
// exact-max-length code-point cases, and invalid-UTF-8 wire payloads)
// lives in api_client_error_validate_unicode_test.go.

// goldenErrorEnvelopeMap decodes the golden error.v1.json fixture as a
// generic map so mutation cases can delete/null/replace individual wire
// fields the typed contractsv1.ErrorEnvelope cannot represent (a Go string
// field cannot distinguish absent from explicit null).
func goldenErrorEnvelopeMap(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(contractFixturePath(t, "error.v1.json"))
	if err != nil {
		t.Fatalf("read golden error fixture: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode golden error fixture: %v", err)
	}
	return envelope
}

// mutateErrorDetail adapts a mutation of the nested "error" object into a
// top-level envelope mutation, matching the golden fixture's shape.
func mutateErrorDetail(mutate func(detail map[string]any)) func(map[string]any) {
	return func(envelope map[string]any) {
		detail, ok := envelope["error"].(map[string]any)
		if !ok {
			return
		}
		mutate(detail)
		envelope["error"] = detail
	}
}

// serveRawErrorBody spins a fixture HTTP server that always answers the
// hosted read routes with the given raw bytes and status, and returns the
// error client.Capabilities() surfaces for it - the actual production
// decode path (call -> decodeAPIError -> validateErrorEnvelope), not a
// direct unit call to either function.
func serveRawErrorBody(t *testing.T, status int, body []byte) error {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	_, callErr := client.Capabilities(context.Background())
	return callErr
}

// assertRejectedAsTransportFallback proves an invalid envelope was mapped
// to the sanitized newTransportError path, not accepted as a trusted
// business error: the ErrMalformedResponse sentinel, an empty Code (a real
// decoded APIError always carries a known non-empty Code), and - when a
// canary string is supplied - proof that string never appears anywhere in
// the returned error.
func assertRejectedAsTransportFallback(t *testing.T, err error, canary string) {
	t.Helper()
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("expected ErrMalformedResponse (sanitized transport fallback), got %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected an *APIError, got %v (%T)", err, err)
	}
	if apiErr.Code != "" {
		t.Fatalf("a sanitized transport fallback must never carry a decoded code, got %q", apiErr.Code)
	}
	if canary != "" && strings.Contains(err.Error(), canary) {
		t.Fatalf("rejected envelope content leaked into the sanitized error: %v", err)
	}
}

func TestClientAcceptsGoldenErrorEnvelope(t *testing.T) {
	fixture := loadContractFixture[contractsv1.ErrorEnvelope](t, "error.v1.json")
	assertSchemaAccepts(t, "error.v1.schema.json", fixture)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The transport-level request ID an APIError carries always comes
		// from the X-Request-ID response header (see call() in
		// api_client_transport.go), never from the envelope body's own
		// request_id field, so the fixture server must set it explicitly
		// to exercise that wiring end to end.
		w.Header().Set("X-Request-ID", fixture.RequestID)
		writeJSONFixture(t, w, fixture.Error.HTTPStatus, fixture)
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	_, callErr := client.Capabilities(context.Background())
	var apiErr *APIError
	if !errors.As(callErr, &apiErr) {
		t.Fatalf("expected an *APIError, got %v (%T)", callErr, callErr)
	}
	if !errors.Is(callErr, ErrRepositoryForbidden) {
		t.Fatalf("expected errors.Is to match ErrRepositoryForbidden, got %v", callErr)
	}
	if apiErr.Code != fixture.Error.Code {
		t.Fatalf("unexpected code: got %q want %q", apiErr.Code, fixture.Error.Code)
	}
	if apiErr.RequestID != fixture.RequestID {
		t.Fatalf("unexpected request id: got %q want %q", apiErr.RequestID, fixture.RequestID)
	}
}

var errorEnvelopeMutationCases = []struct {
	name   string
	mutate func(map[string]any)
}{
	{"missing_request_id", func(e map[string]any) { delete(e, "request_id") }},
	{"null_request_id", func(e map[string]any) { e["request_id"] = nil }},
	{"empty_request_id", func(e map[string]any) { e["request_id"] = "" }},
	{"too_long_request_id", func(e map[string]any) { e["request_id"] = strings.Repeat("r", maxErrorRequestIDLength+1) }},
	{"missing_schema_version", func(e map[string]any) { delete(e, "schema_version") }},
	{"null_schema_version", func(e map[string]any) { e["schema_version"] = nil }},
	{"wrong_schema_version", func(e map[string]any) { e["schema_version"] = "error.v0" }},
	{"missing_error_object", func(e map[string]any) { delete(e, "error") }},
	{"null_error_object", func(e map[string]any) { e["error"] = nil }},
	{"unknown_top_level_field", func(e map[string]any) { e["unexpected_field"] = true }},
	{"missing_code", mutateErrorDetail(func(d map[string]any) { delete(d, "code") })},
	{"null_code", mutateErrorDetail(func(d map[string]any) { d["code"] = nil })},
	{"empty_code", mutateErrorDetail(func(d map[string]any) { d["code"] = "" })},
	{"unknown_code", mutateErrorDetail(func(d map[string]any) { d["code"] = "totally_unknown_code" })},
	{"missing_message", mutateErrorDetail(func(d map[string]any) { delete(d, "message") })},
	{"null_message", mutateErrorDetail(func(d map[string]any) { d["message"] = nil })},
	{"empty_message", mutateErrorDetail(func(d map[string]any) { d["message"] = "" })},
	{"too_long_message", mutateErrorDetail(func(d map[string]any) { d["message"] = strings.Repeat("m", maxErrorMessageLength+1) })},
	{"missing_http_status", mutateErrorDetail(func(d map[string]any) { delete(d, "http_status") })},
	{"null_http_status", mutateErrorDetail(func(d map[string]any) { d["http_status"] = nil })},
	{"http_status_below_min", mutateErrorDetail(func(d map[string]any) { d["http_status"] = float64(minErrorHTTPStatus - 1) })},
	{"http_status_above_max", mutateErrorDetail(func(d map[string]any) { d["http_status"] = float64(maxErrorHTTPStatus + 1) })},
	{"missing_retryable", mutateErrorDetail(func(d map[string]any) { delete(d, "retryable") })},
	{"null_retryable", mutateErrorDetail(func(d map[string]any) { d["retryable"] = nil })},
	{"unknown_nested_error_field", mutateErrorDetail(func(d map[string]any) { d["extra_field"] = "x" })},
	{"details_explicit_null", mutateErrorDetail(func(d map[string]any) { d["details"] = nil })},
	{"too_long_request_id_multibyte", func(e map[string]any) { e["request_id"] = strings.Repeat(multiByteChar, maxErrorRequestIDLength+1) }},
	{"too_long_message_multibyte", mutateErrorDetail(func(d map[string]any) { d["message"] = strings.Repeat(multiByteChar, maxErrorMessageLength+1) })},
}

func TestClientRejectsErrorEnvelopeSchemaInvalidMutations(t *testing.T) {
	for _, tc := range errorEnvelopeMutationCases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := goldenErrorEnvelopeMap(t)
			tc.mutate(envelope)
			assertSchemaRejects(t, "error.v1.schema.json", envelope)
			body, err := json.Marshal(envelope)
			if err != nil {
				t.Fatalf("marshal mutated envelope: %v", err)
			}
			assertRejectedAsTransportFallback(t, serveRawErrorBody(t, http.StatusForbidden, body), "")
		})
	}
}

func TestClientRejectsErrorEnvelopeWithTrailingJSON(t *testing.T) {
	fixture := loadContractFixture[contractsv1.ErrorEnvelope](t, "error.v1.json")
	body, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	body = append(body, []byte(`{"trailing":true}`)...)
	assertRejectedAsTransportFallback(t, serveRawErrorBody(t, http.StatusForbidden, body), "")
}

// TestClientRejectsErrorEnvelopeWithoutLeakingInjectedContent is the canary
// case: an envelope carrying attacker-shaped content in a field strict
// validation rejects (an unrecognized code) must never leak that content
// into the sanitized error the caller ultimately sees.
func TestClientRejectsErrorEnvelopeWithoutLeakingInjectedContent(t *testing.T) {
	const canary = "SECRET-CANARY-VALUE-should-never-leak"
	envelope := goldenErrorEnvelopeMap(t)
	detail := envelope["error"].(map[string]any)
	detail["code"] = "not_a_real_code_" + canary
	detail["message"] = "leaked message " + canary
	envelope["error"] = detail
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	assertRejectedAsTransportFallback(t, serveRawErrorBody(t, http.StatusForbidden, body), canary)
}

// TestDecodeAPIErrorConcurrentSafety drives many concurrent hosted-API
// calls, alternating a fully valid error envelope with a schema-invalid
// one, so `go test -race` can prove validateErrorEnvelope and its callers
// hold no shared mutable state across goroutines.
func TestDecodeAPIErrorConcurrentSafety(t *testing.T) {
	validFixture := loadContractFixture[contractsv1.ErrorEnvelope](t, "error.v1.json")
	validBody, err := json.Marshal(validFixture)
	if err != nil {
		t.Fatalf("marshal valid fixture: %v", err)
	}
	invalidEnvelope := goldenErrorEnvelopeMap(t)
	detail := invalidEnvelope["error"].(map[string]any)
	detail["code"] = "not_a_real_code"
	invalidEnvelope["error"] = detail
	invalidBody, err := json.Marshal(invalidEnvelope)
	if err != nil {
		t.Fatalf("marshal invalid envelope: %v", err)
	}

	var useInvalid atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		if useInvalid.Swap(!useInvalid.Load()) {
			_, _ = w.Write(invalidBody)
		} else {
			_, _ = w.Write(validBody)
		}
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}

	const workers, callsPerWorker = 16, 10
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for range callsPerWorker {
				_, callErr := client.Capabilities(context.Background())
				if callErr == nil {
					t.Error("expected an error for every non-2xx response")
					return
				}
				if !errors.Is(callErr, ErrRepositoryForbidden) && !errors.Is(callErr, ErrMalformedResponse) {
					t.Errorf("unexpected error kind: %v", callErr)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestClientRejectsErrorEnvelopeHTTPStatusMismatch proves the fix for the
// sixth Oracle finding's second half: an envelope whose self-reported
// error.http_status disagrees with the actual HTTP status this response
// was received with is rejected before ever reaching newAPIError, even
// though both statuses are independently well-formed and in bounds on
// their own. This cannot be expressed as a JSON-Schema-invalid mutation -
// the schema has no way to know the transport-level status - so unlike
// every case in errorEnvelopeMutationCases it is verified only against
// the live client boundary, not paired with assertSchemaRejects.
func TestClientRejectsErrorEnvelopeHTTPStatusMismatch(t *testing.T) {
	fixture := loadContractFixture[contractsv1.ErrorEnvelope](t, "error.v1.json")
	if fixture.Error.HTTPStatus == http.StatusInternalServerError {
		t.Fatalf("golden fixture http_status must differ from %d for this test to exercise a real mismatch", http.StatusInternalServerError)
	}
	assertSchemaAccepts(t, "error.v1.schema.json", fixture)
	body, err := json.Marshal(fixture)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	assertRejectedAsTransportFallback(t, serveRawErrorBody(t, http.StatusInternalServerError, body), "")
}
