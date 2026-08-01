package config

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

type EntitlementMode string

const (
	EntitlementModeLocal  EntitlementMode = "local"
	EntitlementModeRemote EntitlementMode = "remote"
)

// EntitlementMode reports the automatically selected entitlement provider.
// Validation guarantees local mode is reachable only in development and test.
func (c Config) EntitlementMode() EntitlementMode {
	if (c.Environment == "development" || c.Environment == "test") && strings.TrimSpace(c.DevHealthEntitlementURL) == "" && strings.TrimSpace(c.DevHealthEntitlementTokenFile) == "" {
		return EntitlementModeLocal
	}
	return EntitlementModeRemote
}

func validateEntitlementConfiguration(c Config) error {
	urlConfigured := strings.TrimSpace(c.DevHealthEntitlementURL) != ""
	tokenConfigured := strings.TrimSpace(c.DevHealthEntitlementTokenFile) != ""
	if urlConfigured != tokenConfigured {
		return errors.New("ACR_DEV_HEALTH_ENTITLEMENT_URL and ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE must be configured together")
	}
	if urlConfigured {
		return validateDevHealthEntitlementURL(c)
	}
	if c.Environment == "staging" || c.Environment == "production" {
		return errors.New("ACR_DEV_HEALTH_ENTITLEMENT_URL and ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE remote entitlement configuration is required in staging and production")
	}
	for name, configured := range map[string]bool{
		"ACR_DEV_HEALTH_ENTITLEMENT_CA_BUNDLE":               strings.TrimSpace(c.DevHealthEntitlementCACertPath) != "",
		"ACR_DEV_HEALTH_ENTITLEMENT_PROXY_URL":               strings.TrimSpace(c.DevHealthEntitlementProxyURL) != "",
		"ACR_DEV_HEALTH_ENTITLEMENT_ALLOW_INSECURE_LOOPBACK": c.DevHealthEntitlementAllowInsecureLoopback,
	} {
		if configured {
			return errors.New(name + " requires remote entitlement URL and token configuration")
		}
	}
	return nil
}

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
