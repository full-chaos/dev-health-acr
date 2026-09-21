package config

import (
	"errors"
	"net/url"
	"strings"
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
func (c Config) OAuthConfigured() bool { return c.OAuthIssuer != "" || len(c.OAuthResources) > 0 }

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
	if c.WebAssertionJWKSFile == "" {
		return errors.New("ACR_OAUTH_ISSUER requires web assertions (ACR_WEB_ASSERTION_*): consent is approved on the web approval page")
	}
	if !c.RequireBackingStores {
		return errors.New("ACR_OAUTH_ISSUER requires the hosted runtime (ACR_REQUIRE_BACKING_STORES)")
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
