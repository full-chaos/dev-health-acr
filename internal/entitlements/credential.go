package entitlements

import (
	"context"
	"net/http"
)

// workloadAudience is the audience go-api's internal acr routes accept a workload token for. Those routes carry no
// action, so the token is requested with no scope.
const workloadAudience = "dev-health-api"

const (
	credentialServiceToken = "service_token"
	credentialWorkload     = "workload"
	outcomeSent            = "sent"
	outcomeTokenFailed     = "token_unavailable"
)

// authorize sets the one Authorization header of a call. With a workload source it mints a token and, when it cannot,
// returns false with nothing set: the call is not sent and the service token is never a fallback.
func (c *Client) authorize(ctx context.Context, request *http.Request) bool {
	if c.workload == nil {
		request.Header.Set("Authorization", "Bearer "+c.token)
		c.record(ctx, credentialServiceToken, outcomeSent)
		return true
	}
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
