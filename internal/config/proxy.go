package config

import (
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/trustedproxy"
)

func stringListValue(lookup lookupEnv, key string) []string {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// validateTrustedProxyCIDRs reads the list with the resolver's own parser,
// networks and "!<address>" exclusions.
func validateTrustedProxyCIDRs(values []string) error {
	if _, err := trustedproxy.Parse(values); err != nil {
		return fmt.Errorf("ACR_TRUSTED_PROXY_CIDRS: %w", err)
	}
	return nil
}
