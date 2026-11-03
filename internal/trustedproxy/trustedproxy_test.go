package trustedproxy

import (
	"net/netip"
	"strings"
	"testing"
)

// The prod list: the node pod range with the node's own addresses excluded.
var prodList = []string{"10.42.0.0/24", "!10.42.0.0", "!10.42.0.1"}

func TestExcludedAddressIsNeverAProxyInsideAListedNetwork(t *testing.T) {
	list, err := Parse(prodList)
	if err != nil {
		t.Fatal(err)
	}
	if list.Networks() != 1 || list.Exclusions() != 2 {
		t.Fatalf("networks=%d exclusions=%d, want 1 and 2", list.Networks(), list.Exclusions())
	}
	for address, want := range map[string]bool{
		"10.42.0.0":        false,
		"10.42.0.1":        false,
		"::ffff:10.42.0.1": false,
		"10.42.0.6":        true,
		"10.42.0.255":      true,
		"10.42.1.6":        false,
	} {
		if got := list.Trusts(netip.MustParseAddr(address)); got != want {
			t.Errorf("Trusts(%s) = %v, want %v", address, got, want)
		}
	}
}

func TestExclusionMayPrecedeItsNetwork(t *testing.T) {
	list, err := Parse([]string{"!10.42.0.1", "10.42.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if list.Trusts(netip.MustParseAddr("10.42.0.1")) {
		t.Fatal("an exclusion listed before its network was not applied")
	}
}

func TestParseRefusesABadExclusionAndNamesNoValue(t *testing.T) {
	for _, values := range [][]string{
		{"10.42.0.0/24", "!10.42.0.0/24"},
		{"10.42.0.0/24", "!"},
		{"10.42.0.0/24", "!junk"},
		{"fe80::/64", "!fe80::1%eth0"},
		{"10.42.0.0/24", "!::ffff:10.42.0.1"},
		{"10.42.0.0/24", "!192.0.2.1"},
		{"!10.42.0.1"},
		{"10.42.0.0/24", "not-a-cidr"},
	} {
		_, err := Parse(values)
		if err == nil {
			t.Errorf("Parse(%q) accepted", values)
			continue
		}
		for _, value := range values {
			if bare := strings.TrimPrefix(value, "!"); bare != "" && strings.Contains(err.Error(), bare) {
				t.Errorf("Parse(%q) error %q names the value %q", values, err, bare)
			}
		}
	}
}

func TestEmptyListTrustsNobody(t *testing.T) {
	list, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if list.Trusts(netip.MustParseAddr("10.42.0.6")) || list.Networks() != 0 || list.Exclusions() != 0 {
		t.Fatal("an empty list trusts an address")
	}
}
