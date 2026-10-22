package hosted

import (
	"log/slog"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// EnvBYOAllowNonPublicBaseURL lets every per-organization BYO model runtime dial
// a non-public address (a loopback, private, link-local or otherwise reserved
// one). Unset, blank, false or malformed means the default: a BYO runtime
// dials public addresses only, checked at the dial after DNS resolution. The
// deployment-default (platform) model runtime is never held to the rule. The
// variable is for a deployment whose organizations run their own model server
// inside the network; it is not in the helm chart.
const EnvBYOAllowNonPublicBaseURL = "ACR_CONTEXT_FABRIC_BYO_ALLOW_NON_PUBLIC_BASE_URL"

func byoAllowNonPublicBaseURLFromEnv(lookup func(string) (string, bool), logger *slog.Logger) bool {
	raw, ok := lookup(EnvBYOAllowNonPublicBaseURL)
	value := strings.TrimSpace(raw)
	if !ok || value == "" {
		return false
	}
	allowed, err := strconv.ParseBool(value)
	if err != nil {
		logger.Warn("context fabric BYO non-public base url allowance malformed, refusing non-public endpoints",
			"variable", EnvBYOAllowNonPublicBaseURL, "value", contextfabric.SanitizeLogAttr(value))
		return false
	}
	if allowed {
		logger.Warn("context fabric per-organization BYO model calls may dial non-public addresses",
			"variable", EnvBYOAllowNonPublicBaseURL)
	}
	return allowed
}
