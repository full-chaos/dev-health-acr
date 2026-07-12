package sidecar

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// This file isolates the Unicode/UTF-8 boundary-precision half of the
// error-envelope validation suite: exact-max-length code-point vs. byte
// counting, and invalid-UTF-8 wire payloads. See
// api_client_error_validate_test.go for the shared helpers
// (goldenErrorEnvelopeMap, mutateErrorDetail, serveRawErrorBody,
// assertRejectedAsTransportFallback) these tests reuse, and for the
// general envelope acceptance/rejection suite.

// multiByteChar is a single 4-byte-UTF-8 code point (U+1F680 ROCKET),
// chosen so a byte-length bound and a Unicode-code-point-length bound
// diverge as sharply as possible: N repetitions always cost 4N bytes but
// exactly N code points, giving boundary tests below maximum leverage to
// catch a byte-counting regression (Oracle finding six: JSON Schema's
// minLength/maxLength count code points, not bytes).
const multiByteChar = "\U0001F680"

// TestMultiByteCharIsExactlyOneCodepoint locks in the assumption every
// boundary test below relies on: multiByteChar is exactly one Unicode
// code point encoded as 4 UTF-8 bytes, so strings.Repeat(multiByteChar, N)
// always yields exactly N code points but 4N bytes - maximum leverage for
// distinguishing a code-point bound from a byte bound.
func TestMultiByteCharIsExactlyOneCodepoint(t *testing.T) {
	if got := utf8.RuneCountInString(multiByteChar); got != 1 {
		t.Fatalf("multiByteChar must be exactly one Unicode code point, got %d", got)
	}
	if got := len(multiByteChar); got != 4 {
		t.Fatalf("multiByteChar must be 4 UTF-8 bytes, got %d", got)
	}
}

// envelopeErrorHTTPStatus extracts the error.http_status a golden-fixture-
// derived envelope map carries, so boundary tests can serve the mutated
// envelope with a matching actual response status without hardcoding the
// golden fixture's current value.
func envelopeErrorHTTPStatus(t *testing.T, envelope map[string]any) int {
	t.Helper()
	detail, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatal("envelope has no error object")
	}
	status, ok := detail["http_status"].(float64)
	if !ok {
		t.Fatal("envelope error.http_status is not a number")
	}
	return int(status)
}

// TestClientAcceptsErrorEnvelopeWithMultiByteUnicodeAtExactMaxBounds
// proves the fix for the sixth Oracle finding: request_id and message
// bounds are enforced in Unicode code points (matching JSON Schema's
// minLength/maxLength semantics, see internal/contractcheck/schema.go's
// validateString), not UTF-8 bytes. Before the fix, a request_id or
// message built entirely of 4-byte code points at exactly the schema's
// code-point maximum was wrongly rejected - its byte length (4x the
// code-point count) exceeded the old byte-counting bound even though the
// canonical JSON Schema, which counts code points, accepted it outright.
// Both sub-cases are checked against the real schema first
// (assertSchemaAccepts) so this is genuine parity evidence, not an
// incidental Go-side-only check.
func TestClientAcceptsErrorEnvelopeWithMultiByteUnicodeAtExactMaxBounds(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			"request_id_at_max_codepoints",
			func(e map[string]any) { e["request_id"] = strings.Repeat(multiByteChar, maxErrorRequestIDLength) },
		},
		{
			"message_at_max_codepoints",
			mutateErrorDetail(func(d map[string]any) { d["message"] = strings.Repeat(multiByteChar, maxErrorMessageLength) }),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envelope := goldenErrorEnvelopeMap(t)
			tc.mutate(envelope)
			assertSchemaAccepts(t, "error.v1.schema.json", envelope)
			body, err := json.Marshal(envelope)
			if err != nil {
				t.Fatalf("marshal mutated envelope: %v", err)
			}
			callErr := serveRawErrorBody(t, envelopeErrorHTTPStatus(t, envelope), body)
			var apiErr *APIError
			if !errors.As(callErr, &apiErr) {
				t.Fatalf("expected a trusted *APIError for a schema-valid envelope, got %v (%T)", callErr, callErr)
			}
			if apiErr.Code == "" {
				t.Fatalf("a valid envelope at exactly the code-point bound decoded as a sanitized fallback instead of a trusted business error: %v", callErr)
			}
			if !errors.Is(callErr, ErrRepositoryForbidden) {
				t.Fatalf("expected errors.Is to match ErrRepositoryForbidden, got %v", callErr)
			}
		})
	}
}

// invalidUTF8ErrorEnvelope builds a raw error envelope JSON payload whose
// error.message field contains rawMessage's bytes verbatim and
// unescaped, exactly as a misbehaving intermediary might place them on
// the wire. json.Marshal cannot be used for this: encoding/json rewrites
// any invalid UTF-8 byte in a Go string to U+FFFD at marshal time, which
// would only prove an already-sanitized string round-trips - not what a
// genuinely malformed wire payload decodes to.
func invalidUTF8ErrorEnvelope(t *testing.T, fixture contractsv1.ErrorEnvelope, rawMessage string) []byte {
	t.Helper()
	requestID, err := json.Marshal(fixture.RequestID)
	if err != nil {
		t.Fatalf("marshal request id: %v", err)
	}
	schemaVersion, err := json.Marshal(fixture.SchemaVersion)
	if err != nil {
		t.Fatalf("marshal schema version: %v", err)
	}
	code, err := json.Marshal(fixture.Error.Code)
	if err != nil {
		t.Fatalf("marshal code: %v", err)
	}
	return fmt.Appendf(nil,
		`{"schema_version":%s,"request_id":%s,"error":{"code":%s,"message":"%s","http_status":%d,"retryable":%t,"details":{}}}`,
		schemaVersion, requestID, code, rawMessage, fixture.Error.HTTPStatus, fixture.Error.Retryable,
	)
}

// TestClientAcceptsErrorEnvelopeWithInvalidUTF8AtExactMaxBounds documents
// and locks in the platform behavior validateErrorEnvelope's Unicode
// code-point counting relies on: encoding/json.Unmarshal (via decodeExact)
// silently replaces every invalid UTF-8 byte in a decoded JSON string
// with one U+FFFD replacement character before validateErrorEnvelope ever
// sees the string, so N invalid bytes on the wire always become exactly
// N code points after decode - identical to how the canonical JSON
// Schema checker (contractcheck.ValidateSerialized) sees it, since both
// paths decode through the same encoding/json machinery. An
// error.message built from
// exactly maxErrorMessageLength invalid bytes therefore decodes to
// exactly maxErrorMessageLength code points and must be accepted, proven
// against the real schema first via assertSchemaAcceptsRaw.
func TestClientAcceptsErrorEnvelopeWithInvalidUTF8AtExactMaxBounds(t *testing.T) {
	fixture := loadContractFixture[contractsv1.ErrorEnvelope](t, "error.v1.json")
	raw := invalidUTF8ErrorEnvelope(t, fixture, strings.Repeat("\xff", maxErrorMessageLength))
	assertSchemaAcceptsRaw(t, "error.v1.schema.json", raw)
	callErr := serveRawErrorBody(t, fixture.Error.HTTPStatus, raw)
	var apiErr *APIError
	if !errors.As(callErr, &apiErr) {
		t.Fatalf("expected a trusted *APIError, got %v (%T)", callErr, callErr)
	}
	if apiErr.Code != fixture.Error.Code {
		t.Fatalf("unexpected code: got %q want %q", apiErr.Code, fixture.Error.Code)
	}
}

// TestClientRejectsErrorEnvelopeWithInvalidUTF8OverMaxBounds is
// TestClientAcceptsErrorEnvelopeWithInvalidUTF8AtExactMaxBounds's
// one-invalid-byte-over-the-bound counterpart: maxErrorMessageLength+1
// invalid bytes decode to maxErrorMessageLength+1 U+FFFD code points,
// one past the schema's maxLength, so both the schema and the client
// must reject it.
func TestClientRejectsErrorEnvelopeWithInvalidUTF8OverMaxBounds(t *testing.T) {
	fixture := loadContractFixture[contractsv1.ErrorEnvelope](t, "error.v1.json")
	raw := invalidUTF8ErrorEnvelope(t, fixture, strings.Repeat("\xff", maxErrorMessageLength+1))
	assertSchemaRejectsRaw(t, "error.v1.schema.json", raw)
	assertRejectedAsTransportFallback(t, serveRawErrorBody(t, fixture.Error.HTTPStatus, raw), "")
}
