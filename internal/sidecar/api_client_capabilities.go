package sidecar

import (
	"context"
	"net/http"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Capabilities fetches the hosted API's capability descriptor: enabled
// tools, entitlements, permissions for the authenticated credential, and
// service limits.
func (c *Client) Capabilities(ctx context.Context) (contractsv1.Capabilities, error) {
	var capabilities contractsv1.Capabilities
	if err := c.call(ctx, http.MethodGet, capabilitiesPath, nil, &capabilities); err != nil {
		return contractsv1.Capabilities{}, err
	}
	if err := validateCapabilities(capabilities); err != nil {
		return contractsv1.Capabilities{}, err
	}
	return capabilities, nil
}
