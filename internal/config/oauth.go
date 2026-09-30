package config

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

const (
	// defaultOAuthRequestPurgeGrace and defaultOAuthClientIdleTTL are the
	// CHAOS-6191 purge windows, equal so that "idle for the client TTL" is
	// exact: request rows are the only record of when a client last asked to
	// authorize, so they are kept as long as the client idle window, and a
	// client is collected only once its last request row has gone.
	defaultOAuthRequestPurgeGrace = 30 * 24 * time.Hour
	defaultOAuthClientIdleTTL     = 30 * 24 * time.Hour
)

// oauthResources splits the comma-separated ACR_OAUTH_RESOURCES value.
func oauthResources(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var resources []string
	for _, part := range strings.Split(value, ",") {
		resources = append(resources, strings.TrimSpace(part))
	}
	return resources
}

// OAuthConfigured reports whether the OAuth login is turned on.
func (c Config) OAuthConfigured() bool {
	return c.OAuthIssuer != "" || len(c.OAuthResources) > 0 || c.OAuthConsentURL != ""
}

// validateOAuthConfig requires the issuer and resources together, the web
// approval surface (consent is the device flow's approval) and the hosted
// runtime, and checks each value's shape. Values are never echoed.
func validateOAuthConfig(c Config) error {
	if !c.OAuthConfigured() {
		return nil
	}
	if c.OAuthIssuer == "" {
		return errors.New("ACR_OAUTH_ISSUER is required when ACR_OAUTH_RESOURCES is set")
	}
	if len(c.OAuthResources) == 0 {
		return errors.New("ACR_OAUTH_RESOURCES is required when ACR_OAUTH_ISSUER is set")
	}
	if !originURL(c.OAuthIssuer) {
		return errors.New("ACR_OAUTH_ISSUER must be an https origin with no path")
	}
	seen := make(map[string]struct{}, len(c.OAuthResources))
	for _, resource := range c.OAuthResources {
		if !resourceURL(resource) {
			return errors.New("ACR_OAUTH_RESOURCES must list absolute https URLs without query or fragment")
		}
		if _, duplicate := seen[resource]; duplicate {
			return errors.New("ACR_OAUTH_RESOURCES must not repeat a resource")
		}
		seen[resource] = struct{}{}
	}
	if c.OAuthConsentURL == "" {
		return errors.New("ACR_OAUTH_CONSENT_URL is required with ACR_OAUTH_ISSUER: /authorize sends the browser to the web consent page")
	}
	if !ConsentPageURL(c.OAuthConsentURL) {
		return errors.New("ACR_OAUTH_CONSENT_URL must be an absolute https URL with a path and no query or fragment")
	}
	if c.WebAssertionJWKSFile == "" {
		return errors.New("ACR_OAUTH_ISSUER requires web assertions (ACR_WEB_ASSERTION_*): consent is approved on the web approval page")
	}
	if !c.RequireBackingStores {
		return errors.New("ACR_OAUTH_ISSUER requires the hosted runtime (ACR_REQUIRE_BACKING_STORES)")
	}
	// CHAOS-6191: the purge windows must be positive, and the client idle
	// window must not be shorter than the request grace: request rows are the
	// only record of a client's last authorization request, so a shorter idle
	// window would judge "no request in N days" on rows already purged.
	if c.OAuthRequestPurgeGrace <= 0 {
		return errors.New("ACR_OAUTH_REQUEST_PURGE_GRACE must be a positive duration")
	}
	if c.OAuthClientIdleTTL < c.OAuthRequestPurgeGrace {
		return errors.New("ACR_OAUTH_CLIENT_IDLE_TTL must not be shorter than ACR_OAUTH_REQUEST_PURGE_GRACE")
	}
	return nil
}

func loopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func originURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return false
	}
	return parsed.Scheme == "https" || (parsed.Scheme == "http" && loopbackHost(parsed.Hostname()))
}

func resourceURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Scheme == "https" || (parsed.Scheme == "http" && loopbackHost(parsed.Hostname()))
}

// ConsentPageURL accepts the web consent page URL: https (or http on a
// loopback host, for tests), a host, a non-root path, and no user info, query
// or fragment, so /authorize can append exactly one handle parameter.
func ConsentPageURL(value string) bool {
	// A bare "?" or "#" parses to an empty query or fragment, so refuse the
	// characters themselves: the handle must be the only query parameter.
	if strings.ContainsAny(value, "?#") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.Path == "" || parsed.Path == "/" {
		return false
	}
	return parsed.Scheme == "https" || (parsed.Scheme == "http" && loopbackHost(parsed.Hostname()))
}
