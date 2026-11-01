package config

import (
	"errors"
	"net/url"
	"strings"
)

type EntitlementMode string

const (
	EntitlementModeLocal  EntitlementMode = "local"
	EntitlementModeRemote EntitlementMode = "remote"
)

// EntitlementCredential names the credential the Dev Health entitlement client sends: the service token read from
// ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE, or a workload token from the auth workload client. The flag is the only
// switch; there is no runtime fallback from one to the other.
type EntitlementCredential string

const (
	EntitlementCredentialServiceToken EntitlementCredential = "service_token"
	EntitlementCredentialWorkload     EntitlementCredential = "workload"
)

// EntitlementCredential reports the credential ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL selects; unset is the service
// token.
func (c Config) EntitlementCredential() EntitlementCredential {
	if EntitlementCredential(c.DevHealthEntitlementCredential) == EntitlementCredentialWorkload {
		return EntitlementCredentialWorkload
	}
	return EntitlementCredentialServiceToken
}

// EntitlementMode reports the automatically selected entitlement provider.
// Validation guarantees local mode is reachable only in development and test.
func (c Config) EntitlementMode() EntitlementMode {
	if c.EntitlementCredential() == EntitlementCredentialWorkload {
		return EntitlementModeRemote
	}
	if (c.Environment == "development" || c.Environment == "test") && strings.TrimSpace(c.DevHealthEntitlementURL) == "" && strings.TrimSpace(c.DevHealthEntitlementTokenFile) == "" {
		return EntitlementModeLocal
	}
	return EntitlementModeRemote
}

func validateEntitlementConfiguration(c Config) error {
	switch c.DevHealthEntitlementCredential {
	case "", string(EntitlementCredentialServiceToken):
	case string(EntitlementCredentialWorkload):
		return validateWorkloadEntitlement(c)
	default:
		return errors.New("ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL must be service_token or workload")
	}
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
		"ACR_DEV_HEALTH_ENTITLEMENT_CA_BUNDLE": strings.TrimSpace(c.DevHealthEntitlementCACertPath) != "",
		"ACR_DEV_HEALTH_ENTITLEMENT_PROXY_URL": strings.TrimSpace(c.DevHealthEntitlementProxyURL) != "",
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
	if parsed.Scheme == "https" || parsed.Scheme == "http" {
		return nil
	}
	return errors.New("ACR_DEV_HEALTH_ENTITLEMENT_URL must use HTTP or HTTPS")
}

// validateWorkloadEntitlement is the workload credential's input check: the origin and the auth workload client are
// required. A token file may stay set (a rollback needs it mounted); the workload credential never reads it.
func validateWorkloadEntitlement(c Config) error {
	if strings.TrimSpace(c.DevHealthEntitlementURL) == "" {
		return errors.New("ACR_DEV_HEALTH_ENTITLEMENT_URL is required when ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL is workload")
	}
	if !c.AuthWorkload.Enabled() {
		return errors.New("ACR_AUTH_WORKLOAD_* must be configured when ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL is workload")
	}
	return validateDevHealthEntitlementURL(c)
}
