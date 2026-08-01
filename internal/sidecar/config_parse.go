package sidecar

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func validateOriginOnly(base *url.URL) error {
	if base.User != nil {
		return &ConfigError{Field: APIURLEnvironment, Detail: "must not contain userinfo (no embedded credentials)"}
	}
	if base.Path != "" && base.Path != "/" {
		return &ConfigError{Field: APIURLEnvironment, Detail: "must not contain a path; it must be scheme and host only"}
	}
	if base.RawQuery != "" || base.ForceQuery {
		return &ConfigError{Field: APIURLEnvironment, Detail: "must not contain a query string"}
	}
	if base.Fragment != "" {
		return &ConfigError{Field: APIURLEnvironment, Detail: "must not contain a fragment"}
	}
	return nil
}

func validateScheme(base *url.URL, _ bool) error {
	switch base.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(base.Hostname()) {
			return nil
		}
	}
	return &ConfigError{Field: APIURLEnvironment, Detail: "must use https (plain http is only allowed for a loopback origin)"}
}
func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
}
func firstOrEmpty(lookup lookupEnv, key string) string { value, _ := lookup(key); return value }
func stringOrDefault(lookup lookupEnv, key, fallback string) string {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}
func durationOrDefault(lookup lookupEnv, key string, fallback time.Duration) (time.Duration, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, &ConfigError{Field: key, Detail: "must be a valid Go duration (e.g. \"30s\", \"2m\")"}
	}
	return parsed, nil
}
func int64OrDefault(lookup lookupEnv, key string, fallback int64) (int64, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, &ConfigError{Field: key, Detail: "must be a valid integer"}
	}
	return parsed, nil
}
func boolOrDefault(lookup lookupEnv, key string, fallback bool) (bool, error) {
	value, ok := lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, &ConfigError{Field: key, Detail: "must be \"true\" or \"false\""}
	}
	return parsed, nil
}
func strictBoolOrDefault(lookup lookupEnv, key string, fallback bool) (bool, error) {
	value, ok := lookup(key)
	if !ok || value == "" {
		return fallback, nil
	}
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, &ConfigError{Field: key, Detail: "must be \"true\" or \"false\""}
	}
}
