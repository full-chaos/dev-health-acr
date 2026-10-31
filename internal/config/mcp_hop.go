package config

import (
	"errors"
	"strings"
)

// DefaultMCPHopAudience is the TokenReview audience of acr-mcp's hop token
// (CHAOS-12094); the chart sets the same value on both workloads.
const DefaultMCPHopAudience = "dev-health-acr-api-mcp-hop"

// MCPHopConfig is acr-api's side of the acr-mcp hop proof (CHAOS-12094, F1).
type MCPHopConfig struct {
	// Required (ACR_MCP_HOP_REQUIRED, default false) turns the check on.
	// Off, the hop header is ignored and nothing changes.
	Required bool
	// Audience (ACR_MCP_HOP_AUDIENCE, default DefaultMCPHopAudience) is the
	// audience TokenReview must confirm the hop token was minted for.
	Audience string
	// Namespace and ServiceAccount (ACR_MCP_HOP_SERVICE_ACCOUNT_NAMESPACE,
	// ACR_MCP_HOP_SERVICE_ACCOUNT_NAME) name the acr-mcp ServiceAccount
	// whose token is accepted; required with Required.
	Namespace, ServiceAccount string
}

func mcpHopValue(lookup lookupEnv) (MCPHopConfig, error) {
	required, err := boolValue(lookup, "ACR_MCP_HOP_REQUIRED", false)
	if err != nil {
		return MCPHopConfig{}, err
	}
	return MCPHopConfig{
		Required:       required,
		Audience:       strings.TrimSpace(stringValue(lookup, "ACR_MCP_HOP_AUDIENCE", DefaultMCPHopAudience)),
		Namespace:      strings.TrimSpace(stringValue(lookup, "ACR_MCP_HOP_SERVICE_ACCOUNT_NAMESPACE", "")),
		ServiceAccount: strings.TrimSpace(stringValue(lookup, "ACR_MCP_HOP_SERVICE_ACCOUNT_NAME", "")),
	}, nil
}

// validateMCPHop: the hop decides MCP-bound credentials, which only the
// OAuth login issues, and it names acr-api's own resource by the issuer, so
// it needs the OAuth login and the hosted runtime, an audience and the
// acr-mcp ServiceAccount. Off, nothing is checked.
func validateMCPHop(c Config) error {
	if !c.MCPHop.Required {
		return nil
	}
	if c.OAuthIssuer == "" {
		return errors.New("ACR_MCP_HOP_REQUIRED requires ACR_OAUTH_ISSUER: the hop decides OAuth credentials bound to an MCP resource")
	}
	if !c.RequireBackingStores {
		return errors.New("ACR_MCP_HOP_REQUIRED requires the hosted runtime (ACR_REQUIRE_BACKING_STORES)")
	}
	if c.MCPHop.Audience == "" {
		return errors.New("ACR_MCP_HOP_AUDIENCE must not be blank")
	}
	if c.MCPHop.Namespace == "" || c.MCPHop.ServiceAccount == "" {
		return errors.New("ACR_MCP_HOP_REQUIRED requires ACR_MCP_HOP_SERVICE_ACCOUNT_NAMESPACE and ACR_MCP_HOP_SERVICE_ACCOUNT_NAME")
	}
	return nil
}
