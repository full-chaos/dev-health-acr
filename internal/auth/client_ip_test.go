package auth

import (
	"net/http/httptest"
	"testing"
)

func TestTrustedProxyClientIPResolver(t *testing.T) {
	resolver, err := NewTrustedProxyClientIPResolver([]string{"10.0.0.0/8", "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, remote, forwarded, want string
	}{
		{name: "untrusted peer ignores header", remote: "203.0.113.8:443", forwarded: "198.51.100.7", want: "203.0.113.8"},
		{name: "trusted proxy uses nearest client", remote: "10.0.0.5:443", forwarded: "198.51.100.7", want: "198.51.100.7"},
		{name: "trusted proxy chain", remote: "10.0.0.5:443", forwarded: "198.51.100.7, 192.0.2.9", want: "198.51.100.7"},
		{name: "malformed chain fails closed", remote: "10.0.0.5:443", forwarded: "attacker, 192.0.2.9", want: "10.0.0.5"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "http://example.test", nil)
			request.RemoteAddr = test.remote
			request.Header.Set("X-Forwarded-For", test.forwarded)
			if got := resolver(request); got != test.want {
				t.Fatalf("client IP = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTrustedProxyClientIPResolverRejectsInvalidCIDR(t *testing.T) {
	if _, err := NewTrustedProxyClientIPResolver([]string{"not-a-cidr"}); err == nil {
		t.Fatal("invalid trusted proxy CIDR accepted")
	}
}

func TestTrustedProxyClientIPResolverUsesAllForwardedHeaderLines(t *testing.T) {
	resolver, err := NewTrustedProxyClientIPResolver([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://example.test", nil)
	request.RemoteAddr = "10.0.0.5:443"
	request.Header.Add("X-Forwarded-For", "198.51.100.7")
	request.Header.Add("X-Forwarded-For", "10.0.0.4")

	if got := resolver(request); got != "198.51.100.7" {
		t.Fatalf("client IP = %q", got)
	}
}
