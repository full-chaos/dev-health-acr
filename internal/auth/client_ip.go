package auth

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/logsanitize"
)

type ClientIPResolver func(*http.Request) string

// RemoteAddressClientIP's ordinary output (SplitHostPort succeeding on a
// real TCP peer address) is never free text -- host is a bare IP literal.
// r1 review round (CHAOS-5558) found the FALLBACK branch was not: when
// RemoteAddr is not "host:port" shape (a non-standard listener/transport,
// or a test double), the function returned `value` UNMODIFIED -- reachable
// straight into middleware.go's "remote_ip" log field with no sanitizer in
// between, the same class of gap this whole ticket exists to close.
// SanitizeLogAttr is a no-op on the well-formed "host:port" path (that
// branch returns before reaching here) and only ever touches this one
// fallback.
func RemoteAddressClientIP(request *http.Request) string {
	value := strings.TrimSpace(request.RemoteAddr)
	host, _, err := net.SplitHostPort(value)
	if err == nil && host != "" {
		return host
	}
	if value == "" {
		return "unknown"
	}
	return logsanitize.SanitizeLogAttr(value)
}

func NewTrustedProxyClientIPResolver(cidrs []string) (ClientIPResolver, error) {
	trusted := make([]netip.Prefix, 0, len(cidrs))
	for _, value := range cidrs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil {
			return nil, errors.New("trusted proxy CIDR is invalid")
		}
		trusted = append(trusted, prefix.Masked())
	}
	return func(request *http.Request) string {
		peerText := RemoteAddressClientIP(request)
		peer, err := netip.ParseAddr(peerText)
		if err != nil || !addressTrusted(peer, trusted) {
			return peerText
		}
		forwarded := []string{}
		for _, header := range request.Header.Values("X-Forwarded-For") {
			forwarded = append(forwarded, strings.Split(header, ",")...)
		}
		for index := len(forwarded) - 1; index >= 0; index-- {
			candidate, parseErr := netip.ParseAddr(strings.TrimSpace(forwarded[index]))
			if parseErr != nil {
				return peerText
			}
			if !addressTrusted(candidate, trusted) {
				return candidate.String()
			}
		}
		return peerText
	}, nil
}

func addressTrusted(address netip.Addr, prefixes []netip.Prefix) bool {
	address = address.Unmap()
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
