package outboundhttp

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"syscall"
	"time"
)

// ErrNonPublicAddress is what a public-only dial returns for an address that is not publicly routable. Its text is
// fixed: it names no host, address or url.
var ErrNonPublicAddress = errors.New("destination resolves to a non-public address")

// RefuseNonPublicAddress is a net.Dialer Control hook. It runs after DNS resolution, on the address about to be
// dialled, so a name that resolves to a private address, a name that is rebound between two lookups and a redirect
// to a private address are each refused at the dial.
func RefuseNonPublicAddress(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrNonPublicAddress
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !PublicAddress(ip) {
		return ErrNonPublicAddress
	}
	return nil
}

// PublicTransport is Transport() with a dialer that refuses every non-public address after DNS resolution.
func PublicTransport() *http.Transport {
	return publicTransportWith(RefuseNonPublicAddress)
}

func publicTransportWith(control func(network, address string, conn syscall.RawConn) error) *http.Transport {
	transport := Transport()
	transport.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: control}).DialContext
	return transport
}

var sharedPublic = sync.OnceValue(PublicTransport)

// SharedPublicTransport is one process-wide PublicTransport(), as SharedTransport is for Transport().
func SharedPublicTransport() *http.Transport { return sharedPublic() }

// PublicAddress reports whether an address is publicly routable. It refuses what acr has always refused (loopback,
// private, link-local, multicast, unspecified, shared 100.64/10, IANA documentation, benchmarking and discard
// ranges, and any of those in an IPv4-mapped or IPv4-compatible IPv6 form) and, in addition, everything the ops
// base url validator refuses (safePublicIPOps), so acr never accepts an address ops refuses; a union of the two
// tables is stricter than either.
func PublicAddress(ip netip.Addr) bool {
	if !safePublicIPOps(ip) {
		return false
	}
	ip = ip.Unmap()
	if embedded, ok := ipv4CompatibleAddress(ip); ok {
		ip = embedded
	}
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	return !inAny(ip, nonPublicPrefixes)
}

// ipv4CompatibleAddress unwraps the deprecated IPv4-compatible IPv6 form (RFC 4291 section 2.5.5.1: the high 96 bits
// zero, distinct from the IPv4-mapped ::ffff:0:0/96 form Unmap already handles) to the IPv4 address it embeds.
func ipv4CompatibleAddress(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is6() {
		return netip.Addr{}, false
	}
	bytes := ip.As16()
	for _, b := range bytes[:12] {
		if b != 0 {
			return netip.Addr{}, false
		}
	}
	return netip.AddrFrom4([4]byte{bytes[12], bytes[13], bytes[14], bytes[15]}), true
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("2001:20::/28"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// The tables below are the ops table, verbatim: dev-health-ops internal/llmorgsettings/ssrf.go at commit
// 69e7179d70573959df2586f1d8ba090e30fccb01 (nonGlobalV4, nonGlobalV6, globalExceptionsV6, reservedV6, isSafePublicIP).
// testdata/ops_public_address_oracle.json is that function executed over the edges of every prefix.

var nonGlobalV4Ops = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
}

var nonGlobalV6Ops = []netip.Prefix{
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("fec0::/10"),
}

var globalExceptionsV6Ops = []netip.Prefix{
	netip.MustParsePrefix("2001:1::1/128"),
	netip.MustParsePrefix("2001:1::2/128"),
	netip.MustParsePrefix("2001:3::/32"),
	netip.MustParsePrefix("2001:4:112::/48"),
	netip.MustParsePrefix("2001:20::/28"),
	netip.MustParsePrefix("2001:30::/28"),
}

var reservedV6Ops = []netip.Prefix{
	netip.MustParsePrefix("::/8"), netip.MustParsePrefix("100::/8"), netip.MustParsePrefix("200::/7"),
	netip.MustParsePrefix("400::/6"), netip.MustParsePrefix("800::/5"), netip.MustParsePrefix("1000::/4"),
	netip.MustParsePrefix("4000::/3"), netip.MustParsePrefix("6000::/3"), netip.MustParsePrefix("8000::/3"),
	netip.MustParsePrefix("a000::/3"), netip.MustParsePrefix("c000::/3"), netip.MustParsePrefix("e000::/4"),
	netip.MustParsePrefix("f000::/5"), netip.MustParsePrefix("f800::/6"), netip.MustParsePrefix("fe00::/9"),
}

func inAny(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func safePublicIPOps(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() ||
		addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	if addr.Is4() {
		return !inAny(addr, nonGlobalV4Ops)
	}
	if inAny(addr, reservedV6Ops) {
		return false
	}
	return !inAny(addr, nonGlobalV6Ops) || inAny(addr, globalExceptionsV6Ops)
}
