package mcp

import (
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// TestClassifyBareMalformedResponseIsUnavailable is the CHAOS-2908
// approval-remediation regression lock: sidecar.ErrMalformedResponse
// returned bare (never wrapped in *sidecar.APIError -- the shape a 2xx
// hosted response with an unparseable body actually takes, see
// api_client_transport.go's decodeExact call inside c.call) must classify
// as "unavailable" (a hosted-API response problem), not fall through
// classify()'s generic "internal" catch-all just because it bypassed the
// *APIError envelope.
func TestClassifyBareMalformedResponseIsUnavailable(t *testing.T) {
	bare := fmt.Errorf("decode hosted API response: %w: unexpected end of JSON input", sidecar.ErrMalformedResponse)
	ce := classify(bare)
	if ce.category != "unavailable" {
		t.Fatalf("expected unavailable, got %q", ce.category)
	}
}

// TestClassifyBareInvalidResponseIsUnavailable locks the same fix for
// sidecar.ErrInvalidResponse, which never travels inside an *APIError at
// all (see api_client_validate.go's validateCapabilities/
// validateContextPacket/validateExpandedEvidence and
// api_client_transport.go's requiredFieldsPresent check): a hosted
// response that decoded as valid JSON but failed semantic validation is
// still a hosted-API response problem, not an internal one.
func TestClassifyBareInvalidResponseIsUnavailable(t *testing.T) {
	bare := fmt.Errorf("%w: context packet: repository.slug violates v1 bounds", sidecar.ErrInvalidResponse)
	ce := classify(bare)
	if ce.category != "unavailable" {
		t.Fatalf("expected unavailable, got %q", ce.category)
	}
}

// TestClassifyBareRequestTooLargeIsValidation locks the fix for
// sidecar.ErrRequestTooLarge, which only ever originates bare (returned
// before any HTTP call is attempted -- see api_client_transport.go's
// c.call size check -- so it can never be wrapped in *APIError): an
// outgoing local request that is too large for this sidecar to send is a
// caller-input problem, not a hosted-API unavailability.
func TestClassifyBareRequestTooLargeIsValidation(t *testing.T) {
	bare := fmt.Errorf("%w: 300000 bytes exceeds the configured limit of 262144", sidecar.ErrRequestTooLarge)
	ce := classify(bare)
	if ce.category != "validation" {
		t.Fatalf("expected validation, got %q", ce.category)
	}
}

// TestClassifyRepositoryScopeMismatchIsValidation locks the fix for
// ErrRepositoryScopeMismatch (defined in this package, context_scope.go):
// a caller-supplied repository that does not match the locally discovered
// Git workspace, with changed-file discovery explicitly requested, is a
// caller-input scoping problem, not an internal one.
func TestClassifyRepositoryScopeMismatchIsValidation(t *testing.T) {
	ce := classify(ErrRepositoryScopeMismatch)
	if ce.category != "validation" {
		t.Fatalf("expected validation, got %q", ce.category)
	}
}

// TestClassifyChangedFilesTruncatedIsValidation locks the fix for
// ErrChangedFilesTruncated (defined in this package, context_scope.go):
// a local changed-file count exceeding the bounded discovery limit, with
// changed-file discovery explicitly requested, is a caller-input scoping
// problem (the request as scoped cannot be safely fulfilled), not an
// internal one.
func TestClassifyChangedFilesTruncatedIsValidation(t *testing.T) {
	ce := classify(ErrChangedFilesTruncated)
	if ce.category != "validation" {
		t.Fatalf("expected validation, got %q", ce.category)
	}
}
