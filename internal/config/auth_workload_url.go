package config

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// The URL rules of the auth workload client, one validator for both fields. The Helm helper
// acr.validateAuthWorkloadClient holds the same rules; internal/config/testdata/auth_workload_urls.tsv is the corpus
// both are held to. They are at least as strict as the client's own (dev-health-go workloadtoken endpoint and issuer
// shape), so a value that passes here is one the client builds from.
var (
	dnsLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	// issuerPathSegment is the client's issuer path segment: at most eight, none empty, none starting with a dot.
	issuerPathSegment = regexp.MustCompile(`^[A-Za-z0-9_~-][A-Za-z0-9._~-]{0,63}$`)
)

const maxHostBytes = 253

func validateExchangeURL(raw string, allowPlainHTTP bool) error {
	u, err := parseAbsolute(raw)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowPlainHTTP {
			return errors.New("an http URL needs ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP")
		}
	default:
		return errors.New("the scheme must be https (or http with ACR_AUTH_WORKLOAD_ALLOW_PLAIN_HTTP)")
	}
	if err := validateAuthority(u, true); err != nil {
		return err
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("the URL must hold only a scheme, a host and a port")
	}
	return nil
}

func validateIssuerURL(raw string) error {
	u, err := parseAbsolute(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" {
		return errors.New("the issuer must be an https URL")
	}
	if err := validateAuthority(u, false); err != nil {
		return err
	}
	if u.Hostname() != strings.ToLower(u.Hostname()) {
		return errors.New("the issuer host must be lower case")
	}
	if u.RawPath != "" || strings.Contains(u.Path, "%") {
		return errors.New("the issuer path must not hold an escape")
	}
	if u.Path == "" {
		return nil
	}
	segments := strings.Split(u.Path[1:], "/")
	if len(segments) > 8 {
		return errors.New("the issuer path has more than eight segments")
	}
	for _, segment := range segments {
		if !issuerPathSegment.MatchString(segment) {
			return errors.New("the issuer path segments must be non-empty and use letters, digits and . _ ~ - (not starting with a dot)")
		}
	}
	return nil
}

func parseAbsolute(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.Host == "" {
		return nil, errors.New("must be an absolute URL with a host")
	}
	if !strings.HasPrefix(raw, u.Scheme+"://") {
		return nil, errors.New("the scheme must be written in lower case, followed by ://")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, errors.New("must hold no userinfo, query or fragment")
	}
	return u, nil
}

// validateAuthority checks the host and the port. A DNS host has labels of 1 to 63 letters, digits and hyphens, none
// starting or ending with a hyphen, none empty; a trailing dot is refused (a rooted name is another origin key, and the
// client's issuer shape refuses it). An IPv4 literal is accepted; an IPv6 literal (in brackets, no zone, no IPv4 tail)
// only where ipv6 is true, because the client's issuer shape has none.
func validateAuthority(u *url.URL, ipv6 bool) error {
	host := u.Hostname()
	if strings.HasPrefix(u.Host, "[") {
		if !ipv6 {
			return errors.New("an IP literal in brackets is not accepted here")
		}
		ip := net.ParseIP(host)
		if ip == nil || !strings.Contains(host, ":") || strings.ContainsAny(host, ".%") {
			return errors.New("the bracketed host is not an IPv6 address")
		}
	} else if net.ParseIP(host) == nil {
		if host == "" || len(host) > maxHostBytes {
			return errors.New("the host is empty or longer than 253 characters")
		}
		for _, label := range strings.Split(host, ".") {
			if !dnsLabel.MatchString(label) {
				return errors.New("the host must be a DNS name: labels of 1 to 63 letters, digits and hyphens, none starting or ending with a hyphen, none empty, no trailing dot")
			}
		}
	}
	if strings.HasSuffix(u.Host, ":") {
		return errors.New("the port is empty")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || len(port) > 5 || n < 1 || n > 65535 {
			return errors.New("the port must be a number from 1 to 65535")
		}
	}
	return nil
}
