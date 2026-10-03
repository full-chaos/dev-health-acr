package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Internal ops query service (POST <url>/query) settings, design r5 E.6/E.8
// and D.6 ('all' row: 30 s deadline). Feature is OFF while the URL is empty.
const (
	envDataQueryURL     = "ACR_DATA_QUERY_URL"
	envDataQueryTimeout = "ACR_DATA_QUERY_TIMEOUT"
	// envDataQueryPath is the PATH run_operation posts to under ACR_DATA_QUERY_URL. Unset = "/query" (the route every deployment used until the
	// class-gated route existed); a deployment switches by value (ops' dedicated internal route "/query/run-operation"), and rolls back by value.
	envDataQueryPath = "ACR_DATA_QUERY_PATH"
	// envDataGraphQLURL is GWC's MCP listener base URL for graphql_query
	// (CHAOS-7085: its own port, POST <url>/query; prod Service
	// http://dev-health-ops-query-api-mcp.dev-health.svc:8092). Empty =
	// graphql_query off. The per-call deadline is ACR_DATA_QUERY_TIMEOUT.
	envDataGraphQLURL = "ACR_DATA_GRAPHQL_URL"

	// DefaultDataQueryPath is the run_operation path when ACR_DATA_QUERY_PATH is unset.
	DefaultDataQueryPath = "/query"

	defaultDataQueryTimeout = 30 * time.Second
	minDataQueryTimeout     = time.Second
	maxDataQueryTimeout     = 55 * time.Second
)

// DataQueryURL returns the internal ops query service base URL
// (ACR_DATA_QUERY_URL). Empty means the feature is off.
func (c Config) DataQueryURL() string { return c.dataQueryURL }

// DataQueryPath returns the path run_operation posts to under DataQueryURL (ACR_DATA_QUERY_PATH, default "/query"). It never carries a query or a
// fragment; load validated it.
func (c Config) DataQueryPath() string {
	if c.dataQueryPath == "" {
		return DefaultDataQueryPath
	}
	return c.dataQueryPath
}

// DataQueryTimeout returns the per-call deadline (ACR_DATA_QUERY_TIMEOUT,
// default 30s, validated within [1s, 55s] at load).
func (c Config) DataQueryTimeout() time.Duration {
	if c.dataQueryTimeout == 0 {
		return defaultDataQueryTimeout
	}
	return c.dataQueryTimeout
}

// DataGraphQLURL returns GWC's MCP listener base URL (ACR_DATA_GRAPHQL_URL).
// Empty means graphql_query is off.
func (c Config) DataGraphQLURL() string { return c.dataGraphQLURL }

// dataGraphQLURLValue reads and validates the MCP listener URL with the
// same rules as ACR_DATA_QUERY_URL. The value is never echoed.
func dataGraphQLURLValue(lookup lookupEnv) (string, error) {
	raw := stringValue(lookup, envDataGraphQLURL, "")
	if raw == "" {
		return "", nil
	}
	if err := validateDataQueryURL(raw); err != nil {
		return "", fmt.Errorf("%s: %w", envDataGraphQLURL, err)
	}
	return raw, nil
}

// dataQueryURLValue reads and validates the base URL. The value is never
// echoed in an error: it may carry credentials when misconfigured.
func dataQueryURLValue(lookup lookupEnv) (string, error) {
	raw := stringValue(lookup, envDataQueryURL, "")
	if raw == "" {
		return "", nil
	}
	if err := validateDataQueryURL(raw); err != nil {
		return "", fmt.Errorf("%s: %w", envDataQueryURL, err)
	}
	return raw, nil
}

// dataQueryPathValue reads ACR_DATA_QUERY_PATH: unset is the default; a value that is SET must be an absolute path made of plain segments (letters,
// digits, . _ ~ -), with no empty, "." or ".." segment, no query, no fragment and no host. An empty, whitespace-only or malformed value is refused at
// startup (it never silently falls back to the default). The value is not echoed.
func dataQueryPathValue(lookup lookupEnv) (string, error) {
	if _, set := lookup(envDataQueryPath); !set {
		return "", nil
	}
	raw := stringValue(lookup, envDataQueryPath, "")
	if err := ValidateDataQueryPath(raw); err != nil {
		return "", fmt.Errorf("%s: %w", envDataQueryPath, err)
	}
	return raw, nil
}

// ValidateDataQueryPath is the rule of ACR_DATA_QUERY_PATH, shared with the client builder.
func ValidateDataQueryPath(raw string) error {
	if raw == "" {
		return errors.New("must not be empty (leave it unset for the default)")
	}
	if len(raw) > 200 || raw[0] != '/' {
		return errors.New("must be an absolute path starting with / (at most 200 characters)")
	}
	for _, segment := range strings.Split(raw[1:], "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("must not contain an empty, . or .. segment")
		}
		for i := 0; i < len(segment); i++ {
			c := segment[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '~' || c == '-') {
				return errors.New("must contain only letters, digits and . _ ~ - in its segments")
			}
		}
	}
	return nil
}

func validateDataQueryURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return errors.New("not a valid URL")
	case u.Scheme != "http" && u.Scheme != "https":
		return errors.New("must be an absolute http or https URL")
	case u.Host == "" || u.Hostname() == "":
		return errors.New("must include a host")
	case u.User != nil:
		return errors.New("must not include userinfo")
	case u.RawQuery != "" || u.ForceQuery:
		return errors.New("must not include a query")
	case u.Fragment != "" || u.RawFragment != "" || hasFragmentMarker(raw):
		return errors.New("must not include a fragment")
	}
	return nil
}

func hasFragmentMarker(raw string) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] == '#' {
			return true
		}
	}
	return false
}

func dataQueryTimeoutValue(lookup lookupEnv) (time.Duration, error) {
	d, err := durationValue(lookup, envDataQueryTimeout, defaultDataQueryTimeout)
	if err != nil {
		return 0, err
	}
	if d < minDataQueryTimeout || d > maxDataQueryTimeout {
		return 0, fmt.Errorf("%s must be between %s and %s", envDataQueryTimeout, minDataQueryTimeout, maxDataQueryTimeout)
	}
	return d, nil
}
