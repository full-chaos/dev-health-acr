package sidecar

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// call executes one hosted API request end to end: it bounds the outgoing
// body, resolves the credential fresh for this call, builds the request
// against the fixed origin only, applies the configured timeout on top of
// ctx, refuses to follow redirects, bounds the response body, and decodes
// exactly one JSON value into decodeInto (which may be nil for calls with
// no response body of interest).
//
// The returned error is either context.Canceled/context.DeadlineExceeded
// (or a wrapper of one, via errors.Is), an *APIError for hosted-API and
// transport-boundary failures, or a plain wrapped error for local request
// construction/credential problems.
func (c *Client) call(ctx context.Context, method, subPath string, requestBody []byte, decodeInto any) error {
	_, err := c.callWithHeaders(ctx, method, subPath, requestBody, decodeInto, nil)
	return err
}

// callWithHeaders extends call for fixed endpoint methods that require
// endpoint-specific request headers or need to distinguish successful status
// codes. Security-sensitive headers remain owned by this transport.
func (c *Client) callWithHeaders(ctx context.Context, method, subPath string, requestBody []byte, decodeInto any, headers http.Header) (int, error) {
	if int64(len(requestBody)) > c.cfg.MaxRequestBodyBytes {
		return 0, fmt.Errorf("%w: %d bytes exceeds the configured limit of %d", ErrRequestTooLarge, len(requestBody), c.cfg.MaxRequestBodyBytes)
	}
	credential, err := c.credential()
	if err != nil {
		return 0, fmt.Errorf("load ACR credential: %w", err)
	}
	// Last-mile guard: no matter which CredentialSource produced this
	// value (LoadCredential's own precedence chain, or a caller-supplied
	// override), a bearer token is never sent unless it matches the ACR
	// API token shape. This is what actually stops a Dev Health license
	// key or any other non-ACR credential from ever reaching the wire,
	// independent of how it was loaded.
	if !auth.IsTokenShapeValid(credential.Token) {
		return 0, ErrCredentialShapeInvalid
	}
	requestURL, err := c.buildURL(subPath)
	if err != nil {
		return 0, err
	}

	callCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	var bodyReader io.Reader
	if requestBody != nil {
		bodyReader = bytes.NewReader(requestBody)
	}
	req, err := http.NewRequestWithContext(callCtx, method, requestURL.String(), bodyReader)
	if err != nil {
		return 0, fmt.Errorf("build hosted API request: %w", err)
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	req.Header.Set("Authorization", "Bearer "+credential.Token)
	req.Header.Set("Accept", "application/json")
	if c.cfg.ClientVersion != "" {
		req.Header.Set("X-ACR-Client-Version", c.cfg.ClientVersion)
	}
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// A context cancellation or deadline expiry surfaces here as a
		// *url.Error wrapping context.Canceled/context.DeadlineExceeded;
		// that distinction (classified downstream as "cancelled"/"timeout",
		// not "unavailable") must survive, so it is the only case that
		// still propagates the raw, %w-wrapped error. Every other
		// transport-level failure -- DNS, TLS handshake, connection
		// refused/reset, and so on -- collapses into the sanitized,
		// fixed-message ErrTransportUnavailable rather than ever wrapping
		// the raw net/http error, whose text can contain the configured
		// host, a resolved IP, or proxy details.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return 0, fmt.Errorf("hosted API request failed: %w", err)
		}
		return 0, newTransportUnavailableError()
	}
	defer resp.Body.Close()

	requestID := resp.Header.Get("X-Request-ID")

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, &APIError{
			HTTPStatus: resp.StatusCode,
			Message:    "hosted API returned an unexpected redirect, which the client does not follow",
			RequestID:  sanitizeMessage(requestID),
			sentinel:   ErrUnexpectedRedirect,
		}
	}

	data, truncated, err := readLimited(resp.Body, c.cfg.MaxResponseBytes)
	if err != nil {
		// Mirrors the classification split immediately above for c.http.Do:
		// a context cancellation/deadline expiring mid-read must remain
		// distinctly classifiable as cancelled/timeout, but every other
		// body-read failure (a connection reset, an unexpected EOF from a
		// response that hung up before delivering as many bytes as its own
		// Content-Length promised, and so on) collapses into the sanitized,
		// fixed-message ErrTransportUnavailable rather than ever wrapping the
		// raw net/http error, whose text can contain partial response body
		// bytes.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return resp.StatusCode, fmt.Errorf("read hosted API response: %w", err)
		}
		return resp.StatusCode, newTransportUnavailableError()
	}
	if truncated {
		return resp.StatusCode, &APIError{
			HTTPStatus: resp.StatusCode,
			Message:    "hosted API response exceeded the configured size limit",
			RequestID:  sanitizeMessage(requestID),
			sentinel:   ErrResponseTooLarge,
		}
	}

	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, decodeAPIError(resp.StatusCode, requestID, resp.Header.Get("Retry-After"), data)
	}
	if resp.StatusCode == http.StatusNoContent {
		if len(data) != 0 {
			return resp.StatusCode, fmt.Errorf("%w: no-content response included a body", ErrInvalidResponse)
		}
		return resp.StatusCode, nil
	}
	if decodeInto == nil {
		return resp.StatusCode, nil
	}
	if err := decodeExact(data, decodeInto); err != nil {
		return resp.StatusCode, fmt.Errorf("%w: decode hosted API response: %w", ErrInvalidResponse, err)
	}
	// decodeExact alone cannot require any particular field to be present:
	// it only rejects unknown fields and trailing content, and once a
	// wire-required field is decoded from an absent or null JSON value it
	// is indistinguishable from one that was legitimately sent as its Go
	// zero value. requiredFieldsPresent (api_client_presence.go) closes
	// that gap before this typed contract is ever returned to a caller.
	if err := requiredFieldsPresent(data, decodeInto); err != nil {
		return resp.StatusCode, fmt.Errorf("%w: %s", ErrInvalidResponse, err)
	}
	return resp.StatusCode, nil
}

// buildURL joins a fixed sub-path (a literal, or a fixed prefix plus one
// url.PathEscape-d caller-controlled segment) onto the configured origin
// only. It never lets the sub-path change the scheme or host, so a
// pathological evidence reference ID cannot redirect the bearer token to a
// different origin.
func (c *Client) buildURL(subPath string) (*url.URL, error) {
	base := strings.TrimRight(c.baseURL.String(), "/")
	parsed, err := url.Parse(base + subPath)
	if err != nil {
		return nil, fmt.Errorf("build hosted API request URL: %w", err)
	}
	if parsed.Scheme != c.baseURL.Scheme || parsed.Host != c.baseURL.Host {
		return nil, errors.New("hosted API request URL escaped the configured origin")
	}
	return parsed, nil
}

// readLimited reads at most limit+1 bytes so callers can distinguish
// "exactly at the limit" from "over the limit" without ever buffering more
// than one byte past the configured bound.
func readLimited(body io.Reader, limit int64) (data []byte, truncated bool, err error) {
	data, err = io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return nil, true, nil
	}
	return data, false, nil
}

// decodeExact requires the payload to be exactly one JSON value matching
// target's shape: unknown fields and trailing JSON content are rejected,
// mirroring the hosted API's own strict server-side decode
// (internal/api/read_decode.go's decodeJSONBody) so the client is exactly
// as strict as the contract it is decoding.
func decodeExact(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedResponse, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("%w: %v", ErrMalformedResponse, err)
		}
		return fmt.Errorf("%w: response contains trailing JSON content", ErrMalformedResponse)
	}
	return nil
}

// decodeAPIError decodes a non-2xx hosted response as the contract's
// ErrorEnvelope. A body that does not conform - malformed JSON, a shape
// decodeExact rejects (unknown fields, trailing content), or one that
// fails validateErrorEnvelope's schema-parity semantic checks (a missing
// or explicitly null required field, an unrecognized schema_version or
// error.code, an out-of-bounds string length or http_status, a mismatch
// between error.http_status and the actual HTTP status this response was
// received with, or an explicit null for the optional details object) -
// falls back to a sanitized transport error rather than ever constructing
// a trusted APIError from unverified data. newAPIError, the
// trusted-business-error constructor, is reached only once every one of
// those checks has passed.
func decodeAPIError(status int, requestID, retryAfterHeader string, data []byte) error {
	var envelope contractsv1.ErrorEnvelope
	if err := decodeExact(data, &envelope); err != nil {
		return newTransportError(status, requestID, data)
	}
	if err := validateErrorEnvelope(data, envelope, status); err != nil {
		return newTransportError(status, requestID, data)
	}
	return newAPIError(status, envelope.Error, requestID, retryAfterHeader)
}

// newClientRequestID generates a contract-valid (8-256 char) opaque
// request identifier for outgoing context-packet requests. The hosted API
// overwrites it with its own server-assigned value regardless (see
// internal/api/read_routes.go), so this only needs to satisfy local
// contract validation before the request is sent.
func newClientRequestID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate request id: %w", err)
	}
	return "req_" + hex.EncodeToString(raw), nil
}
