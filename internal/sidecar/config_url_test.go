package sidecar

import (
	"strings"
	"testing"
)

// This file covers ACR_API_URL shape and scheme enforcement: HTTPS-only
// (except the explicit loopback fixture pairing), origin-only (no
// userinfo/path/query/fragment, including a bare "?" with no query text),
// and malformed-URL handling that must never echo the raw configured value
// -- particularly any userinfo it carried -- into a returned error. Bounds
// (timeout/size/proxy) and CA-bundle tests live in their own files.

func TestLoadConfigRejectsPlainHTTPForNonLoopbackHost(t *testing.T) {
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment: "http://acr.example.com",
	})); err == nil {
		t.Fatal("plain HTTP to a non-loopback host was accepted")
	}
}

func TestLoadConfigAllowsPlainHTTPForLoopbackWithExplicitFixtureFlag(t *testing.T) {
	cfg, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:                "http://127.0.0.1:38111",
		AllowInsecureLoopbackEnvironment: "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowInsecureLoopback {
		t.Fatal("expected AllowInsecureLoopback to be true")
	}
}

func TestLoadConfigAllowsPlainHTTPForLocalhostNameWithExplicitFixtureFlag(t *testing.T) {
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:                "http://localhost:38111",
		AllowInsecureLoopbackEnvironment: "true",
	})); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigRejectsPlainHTTPLoopbackWithoutFixtureFlag(t *testing.T) {
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment: "http://127.0.0.1:38111",
	})); err == nil {
		t.Fatal("plain HTTP loopback was accepted without the explicit fixture flag")
	}
}

func TestLoadConfigRejectsInsecureLoopbackFlagForNonLoopbackHost(t *testing.T) {
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:                "http://evil.example.com",
		AllowInsecureLoopbackEnvironment: "true",
	})); err == nil {
		t.Fatal("the loopback fixture flag must not bypass HTTPS for a non-loopback host")
	}
}

func TestLoadConfigRejectsUnsupportedScheme(t *testing.T) {
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment: "ftp://acr.example.com",
	})); err == nil {
		t.Fatal("unsupported URL scheme was accepted")
	}
}

func TestLoadConfigRejectsBaseURLWithPath(t *testing.T) {
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment: "https://acr.example.com/gateway-prefix",
	})); err == nil {
		t.Fatal("a base URL with a path was accepted")
	}
}

func TestLoadConfigAcceptsBaseURLWithBareTrailingSlash(t *testing.T) {
	// A single trailing "/" and no path are equivalent scheme+host origins.
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment: "https://acr.example.com/",
	})); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigRejectsBaseURLWithQueryString(t *testing.T) {
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment: "https://acr.example.com?debug=1",
	})); err == nil {
		t.Fatal("a base URL with a query string was accepted")
	}
}

// TestLoadConfigRejectsBaseURLWithForceQuery proves a bare trailing "?"
// with no query text is rejected too: url.URL reports that shape via
// ForceQuery with RawQuery == "", so a RawQuery-only check would let a
// query-string delimiter through the origin-only invariant.
func TestLoadConfigRejectsBaseURLWithForceQuery(t *testing.T) {
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment: "https://acr.example.com?",
	})); err == nil {
		t.Fatal("a base URL with a bare trailing '?' (ForceQuery) was accepted")
	}
}

func TestLoadConfigRejectsBaseURLWithFragment(t *testing.T) {
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment: "https://acr.example.com#frag",
	})); err == nil {
		t.Fatal("a base URL with a fragment was accepted")
	}
}

func TestLoadConfigRejectsBaseURLWithUserinfo(t *testing.T) {
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment: "https://user:pass@acr.example.com",
	})); err == nil {
		t.Fatal("a base URL with embedded userinfo was accepted")
	}
}

// TestLoadConfigRejectsMalformedURLWithoutLeakingUserinfoSecret is a
// canary for the exact leak this file's parse-failure branch exists to
// close: url.Parse's own error text embeds the full raw input verbatim
// (confirmed via go run against net/url), so a malformed URL whose
// userinfo happens to carry a secret-shaped password must not have that
// password reach the returned error -- which acr-mcp doctor prints
// verbatim in its JSON output.
func TestLoadConfigRejectsMalformedURLWithoutLeakingUserinfoSecret(t *testing.T) {
	const secret = "tops3cr3t-do-not-leak"
	// "%zz" is not a valid percent-escape, so url.Parse itself fails
	// (rather than succeeding and being rejected later by
	// validateOriginOnly, which is already proven leak-free by
	// TestLoadConfigRejectsBaseURLWithUserinfo above).
	malformed := "https://user:" + secret + "%zz@acr.example.com"
	_, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment: malformed,
	}))
	if err == nil {
		t.Fatal("a malformed URL with unparseable userinfo was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("LoadConfig leaked the malformed URL's userinfo secret: %v", err)
	}
}
