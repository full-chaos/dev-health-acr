package authprojection

import (
	"context"
	"net/http"
	"time"
)

const (
	Path           = "/internal/v1/acr/credential-events"
	Audience       = "auth-service"
	Scope          = "credential_events.publish"
	RequestTimeout = 5 * time.Second
	MaxRetryAfter  = 5 * time.Minute
)

type Outcome string

const (
	OutcomeApplied        Outcome = "applied"
	OutcomeGap            Outcome = "gap"
	OutcomeDigestMismatch Outcome = "digest_mismatch"
	OutcomeRefused        Outcome = "refused"
	OutcomeAuthRefused    Outcome = "auth_refused"
	OutcomeBusy           Outcome = "busy"
	OutcomeOther          Outcome = "other"
	OutcomeNoAnswer       Outcome = "no_answer"
	OutcomeNoToken        Outcome = "no_token"
)

type Result struct {
	Outcome    Outcome
	RetryAfter time.Duration
}

type TokenSource interface {
	Token(ctx context.Context, audience string, scope ...string) (string, error)
}

type Client struct{}

func New(string, TokenSource, http.RoundTripper) (*Client, error) { return &Client{}, nil }

func (c *Client) Post(context.Context, []byte) Result { return Result{} }
