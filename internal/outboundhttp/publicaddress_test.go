package outboundhttp

import (
	"errors"
	"net/netip"
	"testing"
)

func TestPublicAddress(t *testing.T) {
	for address, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "0.0.0.0": false, "::1": false, "fe80::1": false, "fc00::1": false, "::ffff:10.0.0.1": false,
		"::ffff:127.0.0.1": false, "224.0.0.1": false, "198.18.0.1": false, "::": false, "64:ff9b::a00:1": false,
	} {
		if got := PublicAddress(netip.MustParseAddr(address)); got != want {
			t.Errorf("PublicAddress(%s) = %v, want %v", address, got, want)
		}
	}
}

func TestPublicAddressDomain(t *testing.T) {
	for _, tc := range []struct {
		address string
		want    bool
	}{
		// public
		{"8.8.8.8", true}, {"1.1.1.1", true}, {"2606:4700::1111", true}, {"::ffff:8.8.8.8", true},
		// range edges just outside the refused blocks stay public
		{"172.32.0.1", true}, {"172.15.255.255", true}, {"100.63.255.255", true}, {"100.128.0.1", true},
		{"198.17.255.255", true}, {"198.20.0.1", true}, {"239.255.255.255", false}, {"11.0.0.1", true},
		{"192.0.1.1", true}, {"1.0.0.1", true}, {"223.255.255.255", true},
		// IANA-reserved documentation/benchmarking/discard ranges (review round P2)
		{"192.0.2.1", false}, {"198.51.100.1", false}, {"203.0.113.1", false},
		{"100::1", false}, {"2001:2::1", false}, {"2001:10::1", false}, {"2001:20::1", false}, {"3fff::1", false},
		// the deprecated IPv4-compatible IPv6 form embeds the same address rules apply to
		{"::10.0.0.1", false}, {"::a00:1", false}, {"::8.8.8.8", false}, {"::0.0.0.1", false}, {"::", false}, {"::1:a00:1", false},
		// loopback / unspecified / this-network
		{"127.0.0.1", false}, {"127.255.255.254", false}, {"::1", false}, {"0.0.0.0", false}, {"0.1.2.3", false}, {"::", false},
		// RFC 1918
		{"10.0.0.0", false}, {"10.255.255.255", false}, {"172.16.0.1", false}, {"172.31.255.255", false}, {"192.168.0.1", false},
		// link-local (cloud metadata)
		{"169.254.169.254", false}, {"169.254.0.1", false}, {"fe80::1", false}, {"fe80::a9fe:a9fe", false},
		// unique local
		{"fc00::1", false}, {"fd12:3456::1", false},
		// CGNAT, benchmarking, IETF protocol, reserved
		{"100.64.0.1", false}, {"100.127.255.255", false}, {"198.18.0.1", false}, {"198.19.255.255", false},
		{"192.0.0.1", false}, {"192.0.0.255", false}, {"240.0.0.1", false}, {"255.255.255.255", false},
		// multicast
		{"224.0.0.1", false}, {"ff02::1", false}, {"ff05::2", false},
		// IPv4-mapped forms of non-public addresses
		{"::ffff:10.0.0.1", false}, {"::ffff:127.0.0.1", false}, {"::ffff:169.254.169.254", false}, {"::ffff:100.64.0.1", false},
		{"::ffff:192.168.1.1", false}, {"::ffff:0.0.0.0", false},
		// ops table (dev-health-ops ssrf.go): 6to4 relay anycast, 6to4, IETF protocol assignments with their exceptions, site-local, NAT64 local-use
		{"192.88.99.1", false}, {"2002::1", false}, {"2002:808:808::1", false}, {"2001::1", false}, {"2001:1::1", true}, {"2001:1::3", false},
		{"2001:3::1", true}, {"2001:4:112::1", true}, {"2001:30::1", true}, {"fec0::1", false}, {"64:ff9b:1::1", false}, {"4000::1", false}, {"::ffff:192.88.99.1", false},
		// NAT64 and documentation prefixes
		{"64:ff9b::a00:1", false}, {"64:ff9b::808:808", false}, {"2001:db8::1", false}, {"2001:db8:ffff::1", false},
	} {
		t.Run(tc.address, func(t *testing.T) {
			if got := PublicAddress(netip.MustParseAddr(tc.address)); got != tc.want {
				t.Fatalf("PublicAddress(%s) = %v, want %v", tc.address, got, tc.want)
			}
		})
	}
	if PublicAddress(netip.Addr{}) {
		t.Fatal("the zero address is not public")
	}
}

func TestRefuseNonPublicAddressHook(t *testing.T) {
	for address, wantRefused := range map[string]bool{
		"8.8.8.8:443": false, "[2606:4700::1111]:443": false,
		"127.0.0.1:443": true, "[::1]:443": true, "10.0.0.5:443": true, "169.254.169.254:80": true, "[::ffff:10.0.0.1]:443": true,
		"localhost:443": true, "no-port": true, "": true, "example.test:443": true,
		"8.8.8.8": true, "[2606:4700::1111]": true,
	} {
		err := RefuseNonPublicAddress("tcp", address, nil)
		if (err != nil) != wantRefused || (err != nil && !errors.Is(err, ErrNonPublicAddress)) {
			t.Errorf("RefuseNonPublicAddress(%q) = %v, want refused=%v", address, err, wantRefused)
		}
	}
}
