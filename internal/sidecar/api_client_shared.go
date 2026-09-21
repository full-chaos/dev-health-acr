package sidecar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// hostedLivenessPath is the hosted API's unauthenticated liveness route.
const hostedLivenessPath = "/healthz"

// ErrHostedNotLive reports a hosted API that answered its liveness route with
// a non-2xx status. The status is carried as a number only.
var ErrHostedNotLive = errors.New("acr: hosted API liveness route did not answer 2xx")

// WithCredentialSource returns a client that shares this client's origin,
// configuration and connection pool but resolves its bearer from source.
//
// It is the constructor a multi-caller process uses per request: the pooled
// transport carries no identity (the Authorization header is set per call
// from the credential source), so sharing it between callers shares
// connections, never a credential. A nil source is refused rather than
// falling back to LoadCredential's process-wide precedence chain.
func (c *Client) WithCredentialSource(source CredentialSource) (*Client, error) {
	if c == nil {
		return nil, errors.New("acr: a base client is required")
	}
	if source == nil {
		return nil, errors.New("acr: a credential source is required")
	}
	derived := *c
	derived.credential = source
	return &derived, nil
}

// Reachable probes the hosted API's unauthenticated liveness route with the
// process configuration alone. It sends no credential and reads no body, so
// it can answer "is the hosted API there" without any caller's data.
//
// The error is ErrTransportUnavailable (wrapped in an *APIError) for a
// network failure, a context error for cancellation or deadline, and
// ErrHostedNotLive for a non-2xx answer.
func (c *Client) Reachable(ctx context.Context) error {
	requestURL, err := c.buildURL(hostedLivenessPath)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return fmt.Errorf("build hosted liveness request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("hosted liveness request failed: %w", err)
		}
		return newTransportUnavailableError()
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%w: status %d", ErrHostedNotLive, resp.StatusCode)
	}
	return nil
}
