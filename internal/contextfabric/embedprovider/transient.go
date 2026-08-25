package embedprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/openai/openai-go"
)

// IsTransientEmbedError classifies an error returned from Embed as worth a
// bounded retry (CHAOS-4147 item 3 / CHAOS-4259) or not.
//
// TRANSIENT (true): a context deadline (the call timed out -- Timeout or
// BatchTimeout expired), a rate limit (HTTP 429), a server error (HTTP
// 5xx), or a transport-layer failure that never reached the server at all
// (connection refused/reset, DNS failure, TLS handshake failure -- none of
// these produce an *openai.Error, because no HTTP response was ever
// received to build one from). All of these describe the SERVER or the
// NETWORK, not the REQUEST, so the same request retried a moment later may
// simply succeed.
//
// PERSISTENT (false): this package's own structural/identity sentinels
// (ErrResponseShape, ErrDimensionMismatch, ErrModelIdentityMismatch) --
// retrying changes nothing about a malformed response, a dimension that
// will not match, or a server serving the wrong model -- and any
// *openai.Error whose status code is NOT 429 or 5xx (400, 401, 403, 404,
// ...). Those describe the REQUEST or its CREDENTIAL as the server has
// judged them; an identical retry gets the identical judgment. This
// includes a blank-or-wrong API key surfacing at call time (CHAOS-4192
// closes the blank-at-startup case, but a WRONG key still reaches here as a
// 401 on the first real call) -- clearing the batch's vectors is still the
// correct outcome for that, exactly as today; what changes is that a
// caller can now escalate a SUSTAINED run of these past a WARN (see
// GraphTelemetry.RecordVectorProjectionEmbedFailuresEscalated).
//
// ALSO PERSISTENT (codex R1 finding 2): a malformed 2xx response body. The
// openai-go SDK reads a 2xx body and JSON-decodes it into the response
// struct BEFORE this package ever sees an error
// (requestconfig.executeRequest, wrapped by embedChunk as "embed request
// failed: %w") -- a response WAS received here, unlike the transport-layer
// case above, so classifying it the same way as "no response at all" was
// simply wrong: a server that returns unparseable JSON for a given request
// shape is very likely to keep doing so, not recover on the next attempt a
// couple hundred milliseconds later. Detected via errors.As against the
// standard library's own decode-error types (*json.SyntaxError,
// *json.UnmarshalTypeError) rather than by matching this package's own
// wrapping text, so it survives a future rewording of that %w message.
//
// context.Canceled is deliberately excluded from both buckets by being
// treated as PERSISTENT (no retry): a caller cancellation is a shutdown or
// an upstream deadline this function's own retry loop must not fight.
func IsTransientEmbedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrResponseShape) || errors.Is(err, ErrDimensionMismatch) || errors.Is(err, ErrModelIdentityMismatch) {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		return false
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == http.StatusTooManyRequests {
			return true
		}
		return apiErr.StatusCode >= 500 && apiErr.StatusCode < 600
	}
	// Nothing above matched: no *openai.Error, no context error, no
	// standard-library JSON decode error. The most common remaining shape is
	// a transport failure that never reached the server at all (connection
	// refused/reset, DNS failure, TLS handshake failure) -- exactly the
	// "network blip" case a bounded retry exists for -- so this defaults
	// toward transient. A bounded retry against a genuinely persistent but
	// unrecognized error class costs at most EmbedFailureMaxRetries extra
	// calls before falling through to the existing clear-and-degrade path
	// unchanged; that is a strictly safer default than guessing persistent
	// and never retrying a real blip this classifier simply does not know
	// the shape of yet.
	return true
}
