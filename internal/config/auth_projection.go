package config

import (
	"errors"
	"strings"
)

// AuthProjectionConfig (CHAOS-11198) switches on the credential lifecycle outbox publisher: acr-api posts each event to
// the auth-service projection endpoint with its workload token. The zero value is off. The route, audience and scope
// are fixed (internal/authprojection); only the origin is a setting.
type AuthProjectionConfig struct {
	// URL (ACR_AUTH_PROJECTION_URL) is auth-service's origin, the same shape as ACR_AUTH_WORKLOAD_EXCHANGE_URL. Unset is off.
	URL string
}

// Enabled reports whether the publisher runs.
func (a AuthProjectionConfig) Enabled() bool { return a.URL != "" }

// Validate refuses a URL that is invalid or set without the workload client that mints the token. Errors name the
// variable, never its value.
func (a AuthProjectionConfig) Validate(workload AuthWorkloadConfig) error {
	if !a.Enabled() {
		return nil
	}
	if !workload.Enabled() {
		return errors.New("ACR_AUTH_PROJECTION_URL needs the auth workload client (ACR_AUTH_WORKLOAD_*)")
	}
	if err := validateExchangeURL(a.URL, workload.AllowPlainHTTP); err != nil {
		return errors.New("ACR_AUTH_PROJECTION_URL is invalid: " + err.Error())
	}
	return nil
}

func authProjectionValue(lookup lookupEnv) AuthProjectionConfig {
	return AuthProjectionConfig{URL: strings.TrimSpace(stringValue(lookup, "ACR_AUTH_PROJECTION_URL", ""))}
}
