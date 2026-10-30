package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
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
	return canonicalCORSHostPort(parsed)
}

// canonicalCORSHostPort refuses a host or port that a browser rewrites when it
// serializes an origin (codex #1770 r1 P1): the middleware compares byte for
// byte, so an entry such as https://host:443 would load and then refuse the
// https://host the browser actually sends. Refusing at load names the problem
// once, where a silent per-request refusal would not.
func canonicalCORSHostPort(parsed *url.URL) error {
	if strings.HasSuffix(parsed.Host, ":") {
		return fmt.Errorf("an empty port is not how a browser writes an origin; drop the colon")
	}
	host := parsed.Hostname()
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return fmt.Errorf("port %q is not how a browser writes it (decimal 1-65535, no leading zero)", port)
		}
		if (parsed.Scheme == "https" && n == 443) || (parsed.Scheme == "http" && n == 80) {
			return fmt.Errorf("a browser drops the default port %d from the origin it sends; write %s://%s", n, parsed.Scheme, strings.TrimSuffix(parsed.Host, ":"+port))
		}
	}
	if strings.HasPrefix(parsed.Host, "[") {
		ip := net.ParseIP(host)
		// Go writes an IPv4-mapped address as dotted IPv4, so the comparison
		// also refuses that form, which a browser writes in hex.
		if ip == nil || ip.String() != host {
			return fmt.Errorf("IPv6 host %q is not written as a browser serializes it (compressed lower-case form, no IPv4-mapped form, no zone)", host)
		}
		return nil
	}
	for _, r := range host {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_') {
			return fmt.Errorf("host %q holds %q; a browser sends an ASCII host (write an internationalized name in its xn-- form)", host, r)
		}
	}
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	for _, label := range labels {
		if label == "" {
			return fmt.Errorf("host %q has an empty label", host)
		}
	}
	// A host whose last label is a number is an IPv4 address to a browser,
	// which rewrites it to dotted decimal (127.1 becomes 127.0.0.1).
	if last := labels[len(labels)-1]; strings.HasPrefix(last, "0x") || strings.Trim(last, "0123456789") == "" {
		// The host holds no colon, so a parse is an IPv4 address, and Go
		// parses only the four-part dotted-decimal form a browser writes.
		if net.ParseIP(host) == nil {
			return fmt.Errorf("host %q ends in a number, so a browser reads it as an IPv4 address; write the dotted-decimal address it sends", host)
		}
	}
	return nil
}
