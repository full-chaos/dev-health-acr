package config

import (
	"fmt"
	"net/url"
	"strings"
)

// CHAOS-12086 (Ask Dev direct-browser design §5, T6): the exact browser
// origins acr-api answers CORS for, from ACR_CORS_ALLOWED_ORIGINS (comma
// separated). Empty -- the default -- answers CORS for nobody. Each entry is
// one serialized origin, compared byte for byte with the request's Origin:
// scheme "https" (or "http" outside production), a host, an optional port,
// and nothing else -- no path, query, fragment, user info, trailing slash or
// wildcard. Production accepts https origins only.

// CORSAllowedOriginsEnv names the allow list's environment variable.
const CORSAllowedOriginsEnv = "ACR_CORS_ALLOWED_ORIGINS"

func validateCORSAllowedOrigins(environment string, origins []string) error {
	for _, origin := range origins {
		if err := validCORSOrigin(environment, origin); err != nil {
			return fmt.Errorf("%s entry %q: %w", CORSAllowedOriginsEnv, origin, err)
		}
	}
	return nil
}

func validCORSOrigin(environment, origin string) error {
	if strings.Contains(origin, "*") {
		return fmt.Errorf("a wildcard is never allowed; list exact origins")
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("not a URL: %w", err)
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if environment == "production" {
			return fmt.Errorf("production accepts https origins only")
		}
	default:
		return fmt.Errorf("scheme must be https or http")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || strings.HasSuffix(origin, "?") || strings.HasSuffix(origin, "#") {
		return fmt.Errorf("must be scheme://host[:port] with nothing after the host")
	}
	// The serialized form a browser sends: lower-case scheme and host. An
	// entry in any other spelling would never match byte for byte.
	if origin != parsed.Scheme+"://"+strings.ToLower(parsed.Host) {
		return fmt.Errorf("must be written as a browser serializes it (lower case, no trailing slash)")
	}
	return nil
}
