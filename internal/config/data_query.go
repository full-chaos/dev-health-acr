package config

import (
	"errors"
	"fmt"
	"net/url"
	"time"
)

// Internal ops query service (POST <url>/query) settings, design r5 E.6/E.8
// and D.6 ('all' row: 30 s deadline). Feature is OFF while the URL is empty.
const (
	envDataQueryURL     = "ACR_DATA_QUERY_URL"
	envDataQueryTimeout = "ACR_DATA_QUERY_TIMEOUT"

	defaultDataQueryTimeout = 30 * time.Second
	minDataQueryTimeout     = time.Second
	maxDataQueryTimeout     = 55 * time.Second
)

// DataQueryURL returns the internal ops query service base URL
// (ACR_DATA_QUERY_URL). Empty means the feature is off.
func (c Config) DataQueryURL() string { return c.dataQueryURL }

// DataQueryTimeout returns the per-call deadline (ACR_DATA_QUERY_TIMEOUT,
// default 30s, validated within [1s, 55s] at load).
func (c Config) DataQueryTimeout() time.Duration {
	if c.dataQueryTimeout == 0 {
		return defaultDataQueryTimeout
	}
	return c.dataQueryTimeout
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
