package sidecar

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

// Sentinel errors for every hosted ACR API error code (see
// internal/api/response.go's denialForError and read_routes.go /
// limits_middleware.go for the authoritative code list), plus a small set
// of client-side transport-boundary errors that never originate from a
// decoded hosted response. Callers should use errors.Is against these
// rather than matching APIError.Code by string.
var (
	ErrInvalidToken        = errors.New("acr: missing or invalid credential")
	ErrInsufficientScope   = errors.New("acr: credential missing required scope")
	ErrRepositoryForbidden = errors.New("acr: repository is not authorized for this credential")
	ErrFeatureNotEnabled   = errors.New("acr: feature is not enabled for this organization")
	ErrVersionMismatch     = errors.New("acr: sidecar version is not supported")
	ErrRateLimited         = errors.New("acr: request rate limit exceeded")
	ErrUpstreamUnavailable = errors.New("acr: hosted API is temporarily unavailable")
	ErrInternalAPIError    = errors.New("acr: hosted API reported an internal error")
	ErrNotFound            = errors.New("acr: resource was not found")
	ErrInvalidRequest      = errors.New("acr: request was rejected as invalid")
	ErrUnknownAPIError     = errors.New("acr: hosted API reported an unrecognized error code")

	ErrUnexpectedRedirect        = errors.New("acr: hosted API attempted a redirect, which the client does not follow")
	ErrResponseTooLarge          = errors.New("acr: hosted API response exceeded the configured size limit")
	ErrRequestTooLarge           = errors.New("acr: outgoing request body exceeded the configured size limit")
	ErrMalformedResponse         = errors.New("acr: hosted API response could not be parsed")
	ErrWritebackDisabled         = errors.New("acr: sidecar writeback is disabled")
	ErrTranscriptCaptureDisabled = errors.New("acr: sidecar transcript capture is disabled")
	// ErrTransportUnavailable is the sentinel for a client-side network or
	// TLS failure that occurred before any HTTP response was received (DNS
	// failure, TLS handshake failure, connection refused/reset, and so
	// on). It never originates from a decoded hosted response, and a
	// context.Canceled/context.DeadlineExceeded failure is never mapped to
	// it (see newTransportUnavailableError's caller in
	// api_client_transport.go): those remain distinctly classifiable as
	// cancellation/timeout rather than collapsing into "unavailable".
	ErrTransportUnavailable = errors.New("acr: hosted API could not be reached")
)

var codeSentinels = map[string]error{
	"invalid_token":        ErrInvalidToken,
	"insufficient_scope":   ErrInsufficientScope,
	"repo_forbidden":       ErrRepositoryForbidden,
	"feature_not_enabled":  ErrFeatureNotEnabled,
	"version_mismatch":     ErrVersionMismatch,
	"rate_limited":         ErrRateLimited,
	"upstream_unavailable": ErrUpstreamUnavailable,
	"internal_error":       ErrInternalAPIError,
	"not_found":            ErrNotFound,
	"invalid_request":      ErrInvalidRequest,
}

// codeSafeMessages maps each recognized hosted error code to a fixed,
// client-authored, operator-safe message. newAPIError always uses this
// mapping instead of the hosted response's own detail.Message: even though
// validateErrorEnvelope (api_client_error_validate.go) already bounds that
// field's length and sanitizeMessage would strip control characters from
// it, its content is otherwise entirely server-controlled -- a compromised
// or malicious upstream could send any text it likes under a recognized,
// schema-valid code. Never surfacing that text at all, rather than merely
// sanitizing it, closes that channel independent of what the hosted
// response actually said.
var codeSafeMessages = map[string]string{
	"invalid_token":        "the configured credential is missing or invalid",
	"insufficient_scope":   "the configured credential is missing a required scope",
	"repo_forbidden":       "the repository is not authorized for this credential",
	"feature_not_enabled":  "this feature is not enabled for the organization",
	"version_mismatch":     "the sidecar version is not supported by the hosted API",
	"rate_limited":         "the request rate limit was exceeded",
	"upstream_unavailable": "the hosted API is temporarily unavailable",
	"internal_error":       "the hosted API reported an internal error",
	"not_found":            "the requested resource was not found",
	"invalid_request":      "the request was rejected as invalid",
}

// unknownCodeSafeMessage is used for a hosted error code this client does
// not recognize. In practice validateErrorEnvelope already rejects any
// envelope whose code is not a codeSentinels key before newAPIError is
// ever reached via the real decode path (decodeAPIError), so this is a
// defensive fallback for direct callers of newAPIError, not a path
// production traffic exercises.
const unknownCodeSafeMessage = "the hosted API reported an unrecognized error"

// maxSanitizedMessageLength bounds every sanitized error message so a
// misbehaving or compromised upstream cannot inflate logs or exhaust
// memory through error text alone.
const maxSanitizedMessageLength = 500

// APIError is a sanitized, typed representation of a hosted ACR API
// failure, covering both decoded error envelopes and transport-boundary
// failures (unparseable bodies, unexpected redirects, size limits). It is
// built exclusively from server-controlled or client-local data and never
// carries the Authorization header, the bearer token, or a raw upstream
// response body.
type APIError struct {
	Code                 string
	Message              string
	HTTPStatus           int
	Retryable            bool
	RetryAfter           time.Duration
	RequestID            string
	MinimumClientVersion string

	sentinel error
}

func (e *APIError) Error() string {
	base := fmt.Sprintf("acr api error: code=%s status=%d retryable=%t", displayCode(e.Code), e.HTTPStatus, e.Retryable)
	if e.RetryAfter > 0 {
		base += fmt.Sprintf(" retry_after=%s", e.RetryAfter)
	}
	if e.Message != "" {
		base += " message=" + strconv.Quote(e.Message)
	}
	return base
}

// Unwrap lets callers use errors.Is against the well-known sentinels above.
func (e *APIError) Unwrap() error { return e.sentinel }

func displayCode(code string) string {
	if code == "" {
		return "unknown"
	}
	return code
}

// newAPIError builds a sanitized APIError from a decoded hosted error
// envelope. retryAfterHeader is the raw `Retry-After` response header
// value (hosted responses always send it as integer seconds, matching
// internal/api/limits_middleware.go's writeRateLimitError).
//
// apiErr.Message is always one of the fixed strings in codeSafeMessages
// (or unknownCodeSafeMessage), never detail.Message itself: see
// codeSafeMessages's doc comment for why the hosted response's own message
// text is never surfaced, even sanitized.
func newAPIError(status int, detail contractsv1.ErrorDetail, requestID, retryAfterHeader string) *APIError {
	sentinel, ok := codeSentinels[detail.Code]
	if !ok {
		sentinel = ErrUnknownAPIError
	}
	message, ok := codeSafeMessages[detail.Code]
	if !ok {
		message = unknownCodeSafeMessage
	}
	apiErr := &APIError{
		Code:       detail.Code,
		Message:    message,
		HTTPStatus: status,
		Retryable:  detail.Retryable,
		RequestID:  sanitizeMessage(requestID),
		sentinel:   sentinel,
	}
	if detail.Code == "version_mismatch" {
		apiErr.MinimumClientVersion = minimumClientVersion(detail.Details)
	}
	if seconds, ok := parseRetryAfterSeconds(retryAfterHeader); ok {
		apiErr.RetryAfter = time.Duration(seconds) * time.Second
	} else if seconds, ok := retryAfterFromDetails(detail.Details); ok {
		apiErr.RetryAfter = time.Duration(seconds) * time.Second
	}
	return apiErr
}

func minimumClientVersion(details map[string]any) string {
	raw, ok := details["minimum_client_version"].(string)
	if !ok || !version.IsCanonical(raw) {
		return ""
	}
	return raw
}

// newTransportError builds a sanitized APIError for responses that could
// not be decoded as a hosted error envelope at all (for example an
// intermediary proxy returning an HTML error page). The raw body is never
// echoed into the error; only its length informs the generic message.
func newTransportError(status int, requestID string, body []byte) *APIError {
	return &APIError{
		Code:       "",
		Message:    fmt.Sprintf("hosted API returned a non-conforming %d response (%d bytes)", status, len(body)),
		HTTPStatus: status,
		Retryable:  status == 429 || status >= 500,
		RequestID:  sanitizeMessage(requestID),
		sentinel:   ErrMalformedResponse,
	}
}

// newTransportUnavailableError builds a sanitized APIError for a
// client-side network or TLS failure that occurred before any HTTP
// response was received. The underlying net/http error's own text (which
// can contain the configured hostname, a resolved IP, or proxy details)
// is never included: only a fixed, operator-safe message is surfaced,
// matching newTransportError's own no-raw-text discipline for the
// non-2xx-response case. HTTPStatus is left at its zero value: no HTTP
// response was ever received to report a status for.
func newTransportUnavailableError() *APIError {
	return &APIError{
		Message:   "the hosted API could not be reached (network or TLS failure)",
		Retryable: true,
		sentinel:  ErrTransportUnavailable,
	}
}

func retryAfterFromDetails(details map[string]any) (int, bool) {
	raw, ok := details["retry_after_seconds"]
	if !ok {
		return 0, false
	}
	switch value := raw.(type) {
	case float64:
		return int(value), value >= 0
	case int:
		return value, value >= 0
	default:
		return 0, false
	}
}

func parseRetryAfterSeconds(header string) (int, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, false
	}
	seconds, err := strconv.Atoi(header)
	if err != nil || seconds < 0 {
		return 0, false
	}
	return seconds, true
}

// sanitizeMessage strips control characters (including newlines, which
// could otherwise forge fake log lines) and caps length so hosted error
// text is always safe to place directly into logs or returned errors. The
// byte budget is enforced via truncateUTF8 so the result is always valid
// UTF-8, never a multi-byte rune split in half at the cut point.
func sanitizeMessage(raw string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw)
	return truncateUTF8(clean, maxSanitizedMessageLength)
}

// truncateUTF8 returns the longest prefix of s that fits within maxBytes
// bytes without splitting a multi-byte UTF-8 rune. s is assumed to already
// be valid UTF-8 (strings.Map in sanitizeMessage guarantees this: it
// replaces every invalid byte sequence in the input with the UTF-8 encoding
// of U+FFFD before this function ever sees the string), so backtracking to
// the nearest rune-start byte is sufficient to keep the truncated result
// valid. The byte bound itself is the security contract (see
// maxSanitizedMessageLength); this function only changes *where* within
// that budget the cut lands, never how many bytes it can return.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
