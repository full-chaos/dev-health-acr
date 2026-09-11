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

// RemoteAddressClientIP's ordinary output was assumed to be a bare IP
// literal whenever net.SplitHostPort succeeds ("host is never free text").
// r2 review round (CHAOS-5558) found that assumption itself false:
// SplitHostPort is a SYNTACTIC splitter, not a semantic IP validator -- it
// accepts ANY text before the last unbracketed colon as `host`. Executed
// proof: `net.SplitHostPort("evil\nFAKE_LOG_LINE=injected:443")` returns
// host=`"evil\nFAKE_LOG_LINE=injected"`, err=nil. r1 already fixed the
// FALLBACK branch (SplitHostPort failing outright); this closes the
// SUCCESS branch the same way, uniformly, rather than trying to enumerate
// every other way a "host:port"-shaped string could still carry free text.
// SanitizeLogAttr is a no-op on every genuinely well-formed IP literal (all
// bytes already print as-is, well under the 256-rune bound), so the
// ordinary path's behavior is unchanged; only a pathological RemoteAddr
// now reaches middleware.go's "remote_ip" log field sanitized instead of
// raw.
func RemoteAddressClientIP(request *http.Request) string {
	value := strings.TrimSpace(request.RemoteAddr)
	if host, _, err := net.SplitHostPort(value); err == nil && host != "" {
		value = host
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
