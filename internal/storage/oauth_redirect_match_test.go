package storage

import "testing"

// TestMatchOAuthRedirectURIDomain covers the loopback port allowance RFC
// 8252 §7.3 requires: a client that registers a loopback redirect URI with
// no port must be matched against a presented redirect_uri naming ANY port,
// because a native app cannot pre-register the ephemeral port its loopback
// listener binds at runtime. Everything else stays exact. This is the fix
// for CHAOS-6232: codex's CIMD document registers
// "http://127.0.0.1/callback" (no port) and presents
// "http://127.0.0.1:<ephemeral>/callback"; before this fix, acr's exact
// match refused every such request with invalid_redirect_uri.
func TestMatchOAuthRedirectURIDomain(t *testing.T) {
	for _, tc := range []struct {
		name           string
		registered     string
		presented      string
		allowLocalhost bool
		want           bool
	}{
		{"exact match", "https://client.example.test/cb", "https://client.example.test/cb", false, true},
		{"exact match with query", "https://client.example.test/cb?a=1", "https://client.example.test/cb?a=1", false, true},

		// The executed repro: codex's exact CIMD-registered URI and its
		// presented redirect_uri at the failing request.
		{"codex repro: 127.0.0.1 any port", "http://127.0.0.1/callback", "http://127.0.0.1:36229/callback", false, true},

		{"127.0.0.1 any port, low port", "http://127.0.0.1/callback", "http://127.0.0.1:1/callback", false, true},
		{"127.0.0.1 any port, high port", "http://127.0.0.1/callback", "http://127.0.0.1:65535/callback", false, true},
		{"[::1] any port", "http://[::1]/callback", "http://[::1]:54321/callback", false, true},
		{"[::1] any port, allowLocalhost irrelevant", "http://[::1]/callback", "http://[::1]:1/callback", true, true},

		{"localhost any port, CIMD allowed", "http://localhost/callback", "http://localhost:9000/callback", true, true},
		{"localhost any port, DCR refused (no localhost allowance)", "http://localhost/callback", "http://localhost:9000/callback", false, false},

		{"registered port pinned, different presented port refused", "http://127.0.0.1:8080/callback", "http://127.0.0.1:8081/callback", false, false},
		{"registered port pinned, exact presented port matches", "http://127.0.0.1:8080/callback", "http://127.0.0.1:8080/callback", false, true},

		{"non-loopback host, different port refused", "https://client.example.test/cb", "https://client.example.test:8443/cb", false, false},
		{"non-loopback host, same as its own presented value", "https://client.example.test:8443/cb", "https://client.example.test:8443/cb", false, true},

		{"path mismatch refused", "http://127.0.0.1/callback", "http://127.0.0.1:36229/other", false, false},
		{"scheme mismatch refused", "http://127.0.0.1/callback", "https://127.0.0.1:36229/callback", false, false},
		{"host mismatch refused", "http://127.0.0.1/callback", "http://127.0.0.2:36229/callback", false, false},
		{"query mismatch refused", "http://127.0.0.1/callback", "http://127.0.0.1:36229/callback?x=1", false, false},

		{"presented carries userinfo refused", "http://127.0.0.1/callback", "http://user@127.0.0.1:36229/callback", false, false},
		{"presented carries fragment refused", "http://127.0.0.1/callback", "http://127.0.0.1:36229/callback#frag", false, false},

		{"unparseable presented refused", "http://127.0.0.1/callback", "http://127.0.0.1:36229/callback\x00", false, false},
		{"unparseable registered refused", "http://127.0.0.1:\x00/callback", "http://127.0.0.1:36229/callback", false, false},

		{"empty presented refused", "http://127.0.0.1/callback", "", false, false},
		{"empty registered refused", "", "http://127.0.0.1:36229/callback", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchOAuthRedirectURI(tc.registered, tc.presented, tc.allowLocalhost); got != tc.want {
				t.Fatalf("MatchOAuthRedirectURI(%q, %q, allowLocalhost=%v) = %v, want %v",
					tc.registered, tc.presented, tc.allowLocalhost, got, tc.want)
			}
		})
	}
}
