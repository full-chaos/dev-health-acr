package hosted

import (
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-go/authverify"
)

// mcpHopTrustDomain fills TokenReview's required TrustDomain when the
// workload exchange names none. The hop check reads only the namespace and
// ServiceAccount TokenReview returns; the trust domain is echoed, never
// compared.
const mcpHopTrustDomain = "kubernetes"

// buildMCPHopPolicy builds acr-api's check of acr-mcp's hop proof
// (CHAOS-12094) when ACR_MCP_HOP_REQUIRED is on, and nil (no check, today's
// behavior) when it is off. The TokenReview client is the workload token
// exchange's: the in-cluster API server, CA and acr-api's own reviewer token,
// each overridable by the same settings (workload_token_exchange.go).
func buildMCPHopPolicy(cfg config.Config, now func() time.Time, lookup func(string) (string, bool)) (*auth.MCPHopPolicy, error) {
	if !cfg.MCPHop.Required {
		return nil, nil
	}
	httpClient, err := kubernetesAPIHTTPClient(stringEnvOrDefault(lookup, envKubernetesCACertPath, defaultKubernetesCACertPath))
	if err != nil {
		return nil, fmt.Errorf("build kubernetes api http client: %w", err)
	}
	trustDomain := mcpHopTrustDomain
	if value, _ := lookup(envWorkloadTrustDomain); strings.TrimSpace(value) != "" {
		trustDomain = strings.TrimSpace(value)
	}
	validator, err := authverify.NewKubernetesTokenReviewValidator(authverify.KubernetesTokenReviewOptions{
		HTTPClient:    httpClient,
		APIServerURL:  stringEnvOrDefault(lookup, envKubernetesAPIServerURL, defaultKubernetesAPIServerURL),
		ReviewerToken: rereadFileTokenSource(stringEnvOrDefault(lookup, envKubernetesReviewerTokenPath, defaultKubernetesReviewerTokenPath)),
		Audience:      cfg.MCPHop.Audience, TrustDomain: trustDomain, Now: now,
	})
	if err != nil {
		return nil, fmt.Errorf("build kubernetes token review validator: %w", err)
	}
	return auth.NewMCPHopPolicy(auth.MCPHopPolicyOptions{
		Validator: validator, Namespace: cfg.MCPHop.Namespace, ServiceAccount: cfg.MCPHop.ServiceAccount,
		Issuer: cfg.OAuthIssuer, Now: now,
	})
}
