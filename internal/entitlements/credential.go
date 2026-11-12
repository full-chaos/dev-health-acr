package entitlements

import (
	"context"
	"net/http"
)

// workloadAudience is the audience go-api's internal acr routes accept a workload token for. Those routes carry no
// action, so the token is requested with no scope.
const workloadAudience = "dev-health-api"

const (
	credentialWorkload = "workload"
	outcomeSent        = "sent"
	outcomeTokenFailed = "token_unavailable"
)

// authorize sets the one Authorization header of a call: a workload token it mints. When it cannot, it returns false
// with nothing set and the call is not sent.
func (c *Client) authorize(ctx context.Context, request *http.Request) bool {
	token, err := c.workload.Token(request.Context(), workloadAudience)
	if err != nil || token == "" {
		c.record(ctx, credentialWorkload, outcomeTokenFailed)
		return false
	}
	request.Header.Set("Authorization", "Bearer "+token)
	c.record(ctx, credentialWorkload, outcomeSent)
	return true
}

func (c *Client) record(ctx context.Context, credential, outcome string) {
	if c.observe != nil {
		c.observe(ctx, credential, outcome)
	}
}
