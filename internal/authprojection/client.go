// Package authprojection publishes acr credential lifecycle events to the auth-service projection endpoint with the
// acr-api workload token. The endpoint, audience, scope and answer table are fixed by the Auth Control Plane; a
// change to them is a change to this package and to the endpoint together.
package authprojection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/outboundhttp"
)

const (
	// Path is the route of the projection endpoint; one event is one POST.
	Path = "/internal/v1/acr/credential-events"
	// Audience is the workload token audience. The token carries no scope or action claim: the action the route
	// checks (credentials.project) is registry data on the acr-api workload, so the token request names the audience only.
	Audience = "auth-service"
	// RequestTimeout bounds one publish: the token and the POST together.
	RequestTimeout = 5 * time.Second
	// MaxRetryAfter caps a Retry-After the endpoint names.
	MaxRetryAfter = 5 * time.Minute

	maxAnswerBytes = 4096
)

// Outcome is the closed vocabulary of what one publish came to; its values are the only strings a log line carries.
type Outcome string

const (
	OutcomeApplied      Outcome = "applied"             // 204: applied, duplicate or stale
	OutcomeRetry        Outcome = "projection_retry"    // 409 projection_retry: hold the aggregate, backoff
	OutcomeConflict     Outcome = "projection_conflict" // 409 projection_conflict: final, mark, counted apart
	OutcomeFinalRefused Outcome = "projection_refused"  // 409 projection_refused: final, mark, counted apart
	OutcomeSchema       Outcome = "schema"              // 400: schema refusal, hold the aggregate
	OutcomeAuthRefused  Outcome = "auth_refused"        // 401, 403: stop the tick
	OutcomeBusy         Outcome = "busy"                // 429, 503: stop the tick, honour Retry-After
	OutcomeOther        Outcome = "other"               // any other answer: hold, no mark
	OutcomeNoAnswer     Outcome = "no_answer"           // transport failure or timeout: hold, stop the tick
	OutcomeNoToken      Outcome = "no_token"            // no usable workload token: stop the tick, no request sent
)

// Acked reports whether the endpoint closed the event, so the row is marked published: applied (a duplicate or a stale
// one included) and the two final 409 answers, which a retry cannot change and which a hold would only stall behind.
func (r Result) Acked() bool {
	switch r.Outcome {
	case OutcomeApplied, OutcomeConflict, OutcomeFinalRefused:
		return true
	}
	return false
}

// Result is one publish's outcome. RetryAfter is set for OutcomeBusy only, capped at MaxRetryAfter.
type Result struct {
	Outcome    Outcome
	RetryAfter time.Duration
}

// TokenSource is *workloadtoken.Client's Token.
type TokenSource interface {
	Token(ctx context.Context, audience string, scope ...string) (string, error)
}

// Client posts one event per request.
type Client struct {
	endpoint string
	tokens   TokenSource
	http     *http.Client
}

// New builds a client for the auth-service origin. It dials with a transport of its own that never uses the
// environment proxy, so the token travels only to the host origin names. The client follows no redirect.
func New(origin string, tokens TokenSource) (*Client, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("authprojection: the origin must be a scheme, a host and a port only")
	}
	if tokens == nil {
		return nil, errors.New("authprojection: a token source is required")
	}
	u.Path = Path
	return &Client{
		endpoint: u.String(),
		tokens:   tokens,
		http: &http.Client{
			Transport:     outboundhttp.Transport(),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Post sends body (the stored envelope, byte for byte) and classifies the answer. It never returns the answer text.
func (c *Client) Post(ctx context.Context, body []byte) Result {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	token, err := c.tokens.Token(ctx, Audience)
	if err != nil || token == "" {
		return Result{Outcome: OutcomeNoToken}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{Outcome: OutcomeNoAnswer}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return Result{Outcome: OutcomeNoAnswer}
	}
	defer func() { _ = response.Body.Close() }()
	answer, _ := io.ReadAll(io.LimitReader(response.Body, maxAnswerBytes))
	return classify(response.StatusCode, response.Header.Get("Retry-After"), answer)
}

type answerBody struct {
	ReasonCode        string `json:"reason_code"`
	RetryAfterSeconds *int64 `json:"retry_after_seconds"`
}

func classify(status int, retryAfterHeader string, answer []byte) Result {
	switch status {
	case http.StatusNoContent:
		return Result{Outcome: OutcomeApplied}
	case http.StatusBadRequest:
		return Result{Outcome: OutcomeSchema}
	case http.StatusUnauthorized, http.StatusForbidden:
		return Result{Outcome: OutcomeAuthRefused}
	case http.StatusConflict:
		var body answerBody
		_ = json.Unmarshal(answer, &body)
		switch body.ReasonCode {
		case "projection_retry":
			return Result{Outcome: OutcomeRetry}
		case "projection_conflict":
			return Result{Outcome: OutcomeConflict}
		case "projection_refused":
			return Result{Outcome: OutcomeFinalRefused}
		}
		return Result{Outcome: OutcomeOther}
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return Result{Outcome: OutcomeBusy, RetryAfter: retryAfter(retryAfterHeader, answer)}
	}
	return Result{Outcome: OutcomeOther}
}

// retryAfter reads the Retry-After header (whole seconds), else the envelope's retry_after_seconds; no value is a
// zero wait the caller replaces with its own backoff. The result is capped at MaxRetryAfter.
func retryAfter(header string, answer []byte) time.Duration {
	seconds := int64(-1)
	if n, err := strconv.ParseInt(header, 10, 64); err == nil {
		seconds = n
	} else {
		var body answerBody
		if json.Unmarshal(answer, &body) == nil && body.RetryAfterSeconds != nil {
			seconds = *body.RetryAfterSeconds
		}
	}
	if seconds <= 0 {
		return 0
	}
	if seconds > int64(MaxRetryAfter/time.Second) {
		return MaxRetryAfter
	}
	return time.Duration(seconds) * time.Second
}
