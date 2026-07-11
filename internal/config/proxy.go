package config

import (
	"errors"
	"net/netip"
	"strings"
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

func validateTrustedProxyCIDRs(values []string) error {
	for _, value := range values {
		if _, err := netip.ParsePrefix(value); err != nil {
			return errors.New("ACR_TRUSTED_PROXY_CIDRS contains an invalid CIDR")
		}
	}
	return nil
}
