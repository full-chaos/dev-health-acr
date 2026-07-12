package sidecar

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Bounds mirrored exactly from contracts/jsonschema/v1/error.v1.schema.json
// so decodeAPIError never accepts an envelope the canonical schema itself
// would reject.
const (
	minErrorRequestIDLength = 1
	maxErrorRequestIDLength = 256
	minErrorMessageLength   = 1
	maxErrorMessageLength   = 2000
	minErrorHTTPStatus      = 400
	maxErrorHTTPStatus      = 599
)

// validateErrorEnvelope proves a decoded hosted-API ErrorEnvelope actually
// satisfies every constraint contracts/jsonschema/v1/error.v1.schema.json
// enforces, closing the gap decodeExact alone leaves open (see
// api_client_presence.go's requiredFieldsPresent doc comment): decodeExact
// only proves the payload is *shaped* like an ErrorEnvelope - it does not
// require any field to be present or non-null, restrict error.code to the
// hosted API's documented enum, or enforce any string length or numeric
// bound. A response that merely resembles an error envelope (an
// intermediary proxy, a compromised or buggy upstream, a schema version
// this client no longer speaks) must never reach newAPIError - the
// constructor for a trusted, caller-facing business error - without passing
// every one of these checks first; decodeAPIError instead falls back to the
// sanitized newTransportError for anything this rejects.
//
// String bounds are measured in Unicode code points via
// utf8.RuneCountInString, exactly matching how the canonical JSON Schema
// (internal/contractcheck/schema.go's validateString) measures minLength/
// maxLength - JSON Schema's "length" keyword counts code points, not
// bytes. Measuring bytes instead (len(string)) would reject valid,
// schema-conformant non-ASCII envelopes whose multi-byte characters push
// the byte count above the bound while the code-point count stays inside
// it, silently downgrading a legitimate business error to a sanitized
// transport fallback. encoding/json.Unmarshal already normalizes any
// invalid UTF-8 byte sequence in the wire payload to U+FFFD before this
// function ever sees the decoded string (proven by
// TestClientAcceptsErrorEnvelopeWithInvalidUTF8AtExactMaxBounds in
// api_client_error_validate_test.go), so decoded string fields are always
// valid UTF-8 and rune-counting them is well-defined.
//
// actualStatus is the real HTTP status code the transport observed on the
// wire (http.Response.StatusCode). It is compared against the decoded
// envelope's self-reported error.http_status: a hosted API response can
// never disagree with itself about its own status, so any mismatch means
// the body was not actually produced by the hosted API's error-encoding
// path (a caching layer, proxy, or compromised intermediary rewrote either
// the status line or the body) and the envelope must be rejected before
// newAPIError ever maps it into a trusted business error.
func validateErrorEnvelope(data []byte, envelope contractsv1.ErrorEnvelope, actualStatus int) error {
	if err := requiredFieldsPresent(data, &envelope); err != nil {
		return fmt.Errorf("required field missing or null: %w", err)
	}
	if envelope.SchemaVersion != contractsv1.ErrorSchema {
		return fmt.Errorf("schema_version must be %q", contractsv1.ErrorSchema)
	}
	if length := utf8.RuneCountInString(envelope.RequestID); length < minErrorRequestIDLength || length > maxErrorRequestIDLength {
		return fmt.Errorf("request_id length %d outside [%d,%d]", length, minErrorRequestIDLength, maxErrorRequestIDLength)
	}
	if _, known := codeSentinels[envelope.Error.Code]; !known {
		return fmt.Errorf("error.code %q is not a recognized value", envelope.Error.Code)
	}
	if length := utf8.RuneCountInString(envelope.Error.Message); length < minErrorMessageLength || length > maxErrorMessageLength {
		return fmt.Errorf("error.message length %d outside [%d,%d]", length, minErrorMessageLength, maxErrorMessageLength)
	}
	if envelope.Error.HTTPStatus < minErrorHTTPStatus || envelope.Error.HTTPStatus > maxErrorHTTPStatus {
		return fmt.Errorf("error.http_status %d outside [%d,%d]", envelope.Error.HTTPStatus, minErrorHTTPStatus, maxErrorHTTPStatus)
	}
	if envelope.Error.HTTPStatus != actualStatus {
		return fmt.Errorf("error.http_status %d does not match the actual response status %d", envelope.Error.HTTPStatus, actualStatus)
	}
	return validateErrorDetailsNotExplicitNull(data)
}

// validateErrorDetailsNotExplicitNull enforces the one error.v1.schema.json
// constraint requiredFieldsPresent's omitempty handling cannot express:
// error.details is optional (absent from the schema's "required" list), but
// when present it must be a JSON object per the schema's own
// "type": "object" - an explicit JSON null is not of type "object", while
// requiredFieldsPresent treats an omitempty field's null the same as its
// absence (correctly, since the field itself may be omitted). This is the
// one remaining gap for an optional-but-typed field.
func validateErrorDetailsNotExplicitNull(data []byte) error {
	var top struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return fmt.Errorf("re-parse error envelope: %w", err)
	}
	var detail map[string]json.RawMessage
	if err := json.Unmarshal(top.Error, &detail); err != nil {
		return fmt.Errorf("re-parse error detail: %w", err)
	}
	if raw, present := detail["details"]; present && isJSONNull(raw) {
		return fmt.Errorf("error.details must not be explicit null")
	}
	return nil
}
