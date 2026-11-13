package config

import (
	"errors"
	"log/slog"
	"net/url"
	"strings"
)

type EntitlementMode string

const (
	EntitlementModeLocal  EntitlementMode = "local"
	EntitlementModeRemote EntitlementMode = "remote"
)

// EntitlementCredentialWorkload is the one credential the Dev Health entitlement client sends: a workload token from
// the auth workload client. ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL may name it or stay unset; it names nothing else.
const EntitlementCredentialWorkload = "workload"

// The settings of the removed service-token credential (CHAOS-13600). The credential value is refused: no credential
// of that name exists, so the operator asked for a credential acr cannot send. The token file setting names no
// credential, so a deployment that still sets it starts and logs one Warn line (product rule 3: no start refusal for
// config hygiene).
const (
	RetiredEntitlementTokenFileEnv  = "ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE"
	RetiredEntitlementServiceToken  = "service_token"
	entitlementCredentialEnv        = "ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL"
	retiredEntitlementCredentialFix = "acr-api sends its auth workload token (ACR_AUTH_WORKLOAD_*)"
)

// RetiredEntitlementTokenFileWarning is the Warn line of a start that finds the retired token file setting. It names
// the setting, never its value.
const RetiredEntitlementTokenFileWarning = RetiredEntitlementTokenFileEnv + " is retired and ignored: the service token " +
	"entitlement credential was removed and " + retiredEntitlementCredentialFix + "; unset it"

// CheckEntitlementCredentialSettings refuses any credential other than workload. acr-api (Load) and acr-projector
// purge read the entitlement settings through it.
func CheckEntitlementCredentialSettings(lookup func(string) (string, bool)) error {
	credential, _ := lookup(entitlementCredentialEnv)
	return validateEntitlementCredential(strings.TrimSpace(credential))
}

// WarnRetiredEntitlementSettings writes the one start Warn line when the retired token file setting is present
// (even empty). acr-api and acr-projector purge call it once at start.
func WarnRetiredEntitlementSettings(logger *slog.Logger, lookup func(string) (string, bool)) {
	if _, set := lookup(RetiredEntitlementTokenFileEnv); set {
		logger.Warn(RetiredEntitlementTokenFileWarning, "setting", RetiredEntitlementTokenFileEnv)
	}
}

func validateEntitlementCredential(credential string) error {
	switch credential {
	case "", EntitlementCredentialWorkload:
		return nil
	case RetiredEntitlementServiceToken:
		return errors.New(entitlementCredentialEnv + "=" + RetiredEntitlementServiceToken + " is retired: " +
			retiredEntitlementCredentialFix + "; set workload or leave it unset")
	default:
		return errors.New(entitlementCredentialEnv + " must be workload or unset")
	}
}

// EntitlementMode reports the automatically selected entitlement provider.
// Validation guarantees local mode is reachable only in development and test.
func (c Config) EntitlementMode() EntitlementMode {
	if c.DevHealthEntitlementCredential == EntitlementCredentialWorkload {
		return EntitlementModeRemote
	}
	if (c.Environment == "development" || c.Environment == "test") && strings.TrimSpace(c.DevHealthEntitlementURL) == "" {
		return EntitlementModeLocal
	}
	return EntitlementModeRemote
}

func validateEntitlementConfiguration(c Config) error {
	if err := validateEntitlementCredential(c.DevHealthEntitlementCredential); err != nil {
		return err
	}
	if strings.TrimSpace(c.DevHealthEntitlementURL) != "" || c.DevHealthEntitlementCredential == EntitlementCredentialWorkload {
		return validateRemoteEntitlement(c)
	}
	if c.Environment == "staging" || c.Environment == "production" {
		return errors.New("ACR_DEV_HEALTH_ENTITLEMENT_URL remote entitlement configuration is required in staging and production")
	}
	for name, configured := range map[string]bool{
		"ACR_DEV_HEALTH_ENTITLEMENT_CA_BUNDLE": strings.TrimSpace(c.DevHealthEntitlementCACertPath) != "",
		"ACR_DEV_HEALTH_ENTITLEMENT_PROXY_URL": strings.TrimSpace(c.DevHealthEntitlementProxyURL) != "",
	} {
		if configured {
			return errors.New(name + " requires remote entitlement URL configuration")
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

// validateRemoteEntitlement is the remote entitlement's input check: the origin and the auth workload client that
// mints its token are required.
func validateRemoteEntitlement(c Config) error {
	if strings.TrimSpace(c.DevHealthEntitlementURL) == "" {
		return errors.New("ACR_DEV_HEALTH_ENTITLEMENT_URL is required when ACR_DEV_HEALTH_ENTITLEMENT_CREDENTIAL is workload")
	}
	if !c.AuthWorkload.Enabled() {
		return errors.New("ACR_AUTH_WORKLOAD_* must be configured when ACR_DEV_HEALTH_ENTITLEMENT_URL is set: the entitlement client sends a workload token")
	}
	return validateDevHealthEntitlementURL(c)
}
