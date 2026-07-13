package config

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

func validateDevHealthEntitlementURL(c Config) error {
	parsed, err := url.Parse(c.DevHealthEntitlementURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("ACR_DEV_HEALTH_ENTITLEMENT_URL must be an origin")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && c.Environment == "development" && c.DevHealthEntitlementAllowInsecureLoopback && isLoopbackHost(parsed.Hostname()) {
		return nil
	}
	return errors.New("ACR_DEV_HEALTH_ENTITLEMENT_URL must use HTTPS")
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}
