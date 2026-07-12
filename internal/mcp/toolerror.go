package mcp

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// classifiedError is a sanitized, category-tagged failure safe to surface
// verbatim in a CallToolResult. Message never contains a bearer token, a
// raw hosted response body, or a filesystem path: it is either a fixed
// string this package chose, or *sidecar.APIError.Error()/
// sidecar.DescribeConfigError(), both of which are engineered upstream to
// be safe by construction.
type classifiedError struct {
	category string // auth | entitlement | repo_forbidden | validation | rate_limit | no_data | version | unavailable | timeout | cancelled | internal
	message  string
}

func (e *classifiedError) Error() string { return e.category + ": " + e.message }

// classify maps every error this package's tool handlers can encounter
// (hosted API calls, local workspace discovery, request validation) onto a
// classifiedError. This includes sidecar sentinels that never travel
// inside a *sidecar.APIError -- a local request that never reached the
// hosted API (too large to send), or a hosted response that decoded or
// validated incorrectly outside the *APIError envelope (see
// classifyLocalSidecarSentinel) -- and this package's own
// ErrRepositoryScopeMismatch/ErrChangedFilesTruncated scoping failures.
// Unrecognized errors fall back to a fixed, generic "internal" message
// rather than ever echoing err.Error() for a type this function does not
// explicitly know to be safe.
func classify(err error) *classifiedError {
	if err == nil {
		return nil
	}
	var already *classifiedError
	if errors.As(err, &already) {
		return already
	}

	switch {
	case errors.Is(err, context.Canceled):
		return &classifiedError{category: "cancelled", message: "the request was cancelled"}
	case errors.Is(err, context.DeadlineExceeded):
		return &classifiedError{category: "timeout", message: "the hosted API call exceeded the configured timeout"}
	}

	var configErr *sidecar.ConfigError
	if errors.As(err, &configErr) {
		return &classifiedError{category: "internal", message: sidecar.DescribeConfigError(err)}
	}

	var apiErr *sidecar.APIError
	if errors.As(err, &apiErr) {
		return classifyAPIError(err, apiErr)
	}

	if ce := classifyLocalSidecarSentinel(err); ce != nil {
		return ce
	}

	if isWorkspaceError(err) {
		return &classifiedError{category: "validation", message: "the local Git workspace could not be resolved for this request"}
	}

	if errors.Is(err, ErrRepositoryScopeMismatch) || errors.Is(err, ErrChangedFilesTruncated) {
		return &classifiedError{category: "validation", message: err.Error()}
	}

	if errors.Is(err, sidecar.ErrCredentialShapeInvalid) || errors.Is(err, sidecar.ErrCredentialMissing) {
		return &classifiedError{category: "auth", message: "the ACR API credential is missing or does not match the expected shape"}
	}

	return &classifiedError{category: "internal", message: "the request could not be completed"}
}

// classifyAPIError maps a *sidecar.APIError onto a category using the
// sentinel it wraps. apiErr.Error() is already sanitized and length-bounded
// upstream (see internal/sidecar/api_errors.go), so it is safe to surface
// as the classifiedError message verbatim.
func classifyAPIError(err error, apiErr *sidecar.APIError) *classifiedError {
	category := "unavailable"
	switch {
	case errors.Is(err, sidecar.ErrInvalidToken):
		category = "auth"
	case errors.Is(err, sidecar.ErrRepositoryForbidden):
		category = "repo_forbidden"
	case errors.Is(err, sidecar.ErrInsufficientScope), errors.Is(err, sidecar.ErrFeatureNotEnabled):
		category = "entitlement"
	case errors.Is(err, sidecar.ErrVersionMismatch):
		category = "version"
	case errors.Is(err, sidecar.ErrRateLimited):
		category = "rate_limit"
	case errors.Is(err, sidecar.ErrNotFound):
		category = "no_data"
	case errors.Is(err, sidecar.ErrInvalidRequest):
		category = "validation"
	case errors.Is(err, sidecar.ErrUpstreamUnavailable), errors.Is(err, sidecar.ErrInternalAPIError),
		errors.Is(err, sidecar.ErrUnexpectedRedirect), errors.Is(err, sidecar.ErrResponseTooLarge),
		errors.Is(err, sidecar.ErrMalformedResponse):
		category = "unavailable"
	}
	return &classifiedError{category: category, message: apiErr.Error()}
}

// classifyLocalSidecarSentinel maps a sidecar sentinel that can reach this
// package bare (never wrapped in *sidecar.APIError) onto a classifiedError,
// or returns nil when err matches none of them. Message is always a fixed
// string, never err.Error(): a bare-wrapped decode or validation error can
// carry fragments of the untrusted hosted response body (e.g. an unknown
// field name from encoding/json's own error text), which must never reach
// a classifiedError message.
//
// sidecar.ErrRequestTooLarge only ever originates bare: the outgoing-size
// check in Client.call (api_client_transport.go) runs before any HTTP call
// is attempted, so it can never be wrapped in an APIError. It is a
// caller-input problem (this sidecar declined to send an oversized local
// request), not a hosted-API unavailability.
//
// sidecar.ErrMalformedResponse and sidecar.ErrInvalidResponse both
// originate bare from the 2xx response path: a 2xx body that fails to
// decode (decodeExact, called directly from Client.call for a successful
// response) or that decodes but fails requiredFieldsPresent or one of
// validateCapabilities/validateContextPacket/validateExpandedEvidence
// (api_client_validate.go). Both are hosted-API response problems -- the
// same category classifyAPIError already assigns ErrMalformedResponse when
// it does arrive wrapped (a non-2xx envelope-decode failure) -- so both
// classify as "unavailable" here too, for parity regardless of which shape
// the failure took.
func classifyLocalSidecarSentinel(err error) *classifiedError {
	switch {
	case errors.Is(err, sidecar.ErrRequestTooLarge):
		return &classifiedError{category: "validation", message: "the outgoing request exceeded the configured size limit"}
	case errors.Is(err, sidecar.ErrMalformedResponse), errors.Is(err, sidecar.ErrInvalidResponse):
		return &classifiedError{category: "unavailable", message: "the hosted API response could not be parsed or failed validation"}
	default:
		return nil
	}
}

func isWorkspaceError(err error) bool {
	for _, sentinel := range []error{
		sidecar.ErrNoWorkspaceRoot, sidecar.ErrInvalidWorkspaceRoot, sidecar.ErrWorkspaceRootSymlink,
		sidecar.ErrWorkspaceRootNotDir, sidecar.ErrUnsupportedRootScheme, sidecar.ErrTooManyWorkspaceRoots,
		sidecar.ErrControlCharacters, sidecar.ErrNotGitRepository, sidecar.ErrAmbiguousWorkspaceRoot,
		sidecar.ErrUnsupportedRemote, sidecar.ErrAmbiguousRemote, sidecar.ErrNoCommits, sidecar.ErrGitOutputTooLarge,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

// toolErrorResult builds a CallToolResult with IsError set from a
// classified failure. This is a normal tool-level failure, not a protocol
// error: the SDK's low-level Server.AddTool path treats a returned Go error
// as a protocol crash, so handlers must always return (result, nil) here.
func toolErrorResult(err error) *mcpsdk.CallToolResult {
	ce := classify(err)
	result := &mcpsdk.CallToolResult{}
	result.SetError(ce)
	return result
}
