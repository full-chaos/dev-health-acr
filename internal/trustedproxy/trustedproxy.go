// Package trustedproxy reads acr's trusted-proxy lists (ACR_TRUSTED_PROXY_CIDRS,
// ACR_MCP_TRUSTED_PROXY_CIDRS) with one parser, so a list config accepts is a
// list the client address resolver accepts.
//
// A list holds networks and "!<address>" exclusions, the exclusion form go-api
// and auth-service read (dev-health-ops internal/auth/clientaddr). An excluded
// address is never a proxy, even inside a listed network: the node pod range
// holds the node's own addresses, from which a host process or a hostNetwork
// pod reaches acr.
package trustedproxy

import (
	"fmt"
	"net/netip"
	"strings"
)

// List is a parsed trusted-proxy list. The zero List trusts no address.
type List struct {
	prefixes []netip.Prefix
	except   map[netip.Addr]bool
}

// Parse reads the entries. A network entry is a CIDR (host bits are masked).
// An exclusion is "!" and one IP address, never a network, with no zone and
// not IPv4-mapped; it must lie inside a listed network. Anything else is an
// error that names the entry by its position, never its value, so a typo
// cannot widen or silently narrow the trust.
func Parse(values []string) (List, error) {
	list := List{except: map[netip.Addr]bool{}}
	type exclusion struct {
		addr  netip.Addr
		entry int
	}
	var exclusions []exclusion
	for index, raw := range values {
		value := strings.TrimSpace(raw)
		if rest, ok := strings.CutPrefix(value, "!"); ok {
			addr, err := netip.ParseAddr(rest)
			if err != nil || addr.Zone() != "" {
				return List{}, fmt.Errorf("trusted proxy entry %d is not an excluded IP address (\"!\" and one address, never a network)", index+1)
			}
			if addr.Is4In6() {
				return List{}, fmt.Errorf("trusted proxy entry %d is an IPv4-mapped exclusion: give it as IPv4", index+1)
			}
			exclusions = append(exclusions, exclusion{addr, index + 1})
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return List{}, fmt.Errorf("trusted proxy entry %d is not a network (CIDR)", index+1)
		}
		list.prefixes = append(list.prefixes, prefix.Masked())
	}
	for _, x := range exclusions {
		if !list.inPrefixes(x.addr) {
			return List{}, fmt.Errorf("trusted proxy entry %d excludes an address outside every listed network", x.entry)
		}
		list.except[x.addr] = true
	}
	return list, nil
}

// Networks is the number of listed networks; exclusions are not counted.
func (l List) Networks() int { return len(l.prefixes) }

// Exclusions is the number of excluded addresses.
func (l List) Exclusions() int { return len(l.except) }

// Trusts reports whether address (IPv4-mapped unmapped) is a trusted proxy:
// inside a listed network and not excluded.
func (l List) Trusts(address netip.Addr) bool {
	address = address.Unmap()
	if l.except[address] {
		return false
	}
	return l.inPrefixes(address)
}

func (l List) inPrefixes(address netip.Addr) bool {
	for _, prefix := range l.prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
