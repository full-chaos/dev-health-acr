package config

import (
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

func TestValidateOAuthConfigDomain(t *testing.T) {
	valid := Config{
		OAuthIssuer: "https://acr.example.test", OAuthResources: []string{"https://mcp.example.test/mcp"},
		OAuthConsentURL:      "https://www.example.test/acr/authorize",
		WebAssertionJWKSFile: "/run/jwks.json", RequireBackingStores: true,
		OAuthRequestPurgeGrace: 24 * time.Hour, OAuthClientIdleTTL: 30 * 24 * time.Hour,
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"valid", func(*Config) {}, ""},
		{"off", func(c *Config) { c.OAuthIssuer, c.OAuthResources, c.OAuthConsentURL = "", nil, "" }, ""},
		{"consent url only", func(c *Config) { c.OAuthIssuer, c.OAuthResources = "", nil }, "ACR_OAUTH_ISSUER is required"},
		{"no consent url", func(c *Config) { c.OAuthConsentURL = "" }, "ACR_OAUTH_CONSENT_URL is required"},
		{"consent url origin only", func(c *Config) { c.OAuthConsentURL = "https://www.example.test" }, "ACR_OAUTH_CONSENT_URL must be"},
		{"consent url root", func(c *Config) { c.OAuthConsentURL = "https://www.example.test/" }, "ACR_OAUTH_CONSENT_URL must be"},
		{"consent url query", func(c *Config) { c.OAuthConsentURL = "https://www.example.test/acr/authorize?handle=x" }, "ACR_OAUTH_CONSENT_URL must be"},
		{"consent url http", func(c *Config) { c.OAuthConsentURL = "http://www.example.test/acr/authorize" }, "ACR_OAUTH_CONSENT_URL must be"},
		{"consent url bare fragment", func(c *Config) { c.OAuthConsentURL = "https://www.example.test/acr/authorize#" }, "ACR_OAUTH_CONSENT_URL must be"},
		{"consent url bare query", func(c *Config) { c.OAuthConsentURL = "https://www.example.test/acr/authorize?" }, "ACR_OAUTH_CONSENT_URL must be"},
		{"consent url loopback http", func(c *Config) { c.OAuthConsentURL = "http://localhost:3000/acr/authorize" }, ""},
		{"two resources", func(c *Config) { c.OAuthResources = append(c.OAuthResources, "https://mcp2.example.test/mcp") }, ""},
		{"loopback http", func(c *Config) {
			c.OAuthIssuer, c.OAuthResources = "http://127.0.0.1:8080", []string{"http://localhost:8081/mcp"}
		}, ""},
		{"issuer only", func(c *Config) { c.OAuthResources = nil }, "ACR_OAUTH_RESOURCES is required"},
		{"resources only", func(c *Config) { c.OAuthIssuer = "" }, "ACR_OAUTH_ISSUER is required"},
		{"issuer path", func(c *Config) { c.OAuthIssuer = "https://acr.example.test/x" }, "https origin"},
		{"issuer trailing slash", func(c *Config) { c.OAuthIssuer = "https://acr.example.test/" }, "https origin"},
		{"issuer http", func(c *Config) { c.OAuthIssuer = "http://acr.example.test" }, "https origin"},
		{"issuer query", func(c *Config) { c.OAuthIssuer = "https://acr.example.test?x=1" }, "https origin"},
		{"resource http", func(c *Config) { c.OAuthResources = []string{"http://mcp.example.test/mcp"} }, "absolute https"},
		{"resource empty entry", func(c *Config) { c.OAuthResources = []string{""} }, "absolute https"},
		{"resource fragment", func(c *Config) { c.OAuthResources = []string{"https://mcp.example.test/mcp#f"} }, "absolute https"},
		{"resource duplicate", func(c *Config) {
			c.OAuthResources = []string{"https://mcp.example.test/mcp", "https://mcp.example.test/mcp"}
		}, "must not repeat"},
		{"no web assertions", func(c *Config) { c.WebAssertionJWKSFile = "" }, "requires web assertions"},
		{"no hosted runtime", func(c *Config) { c.RequireBackingStores = false }, "requires the hosted runtime"},
		{"purge grace zero", func(c *Config) { c.OAuthRequestPurgeGrace = 0 }, "ACR_OAUTH_REQUEST_PURGE_GRACE must be a positive"},
		{"purge grace negative", func(c *Config) { c.OAuthRequestPurgeGrace = -time.Hour }, "ACR_OAUTH_REQUEST_PURGE_GRACE must be a positive"},
		{"idle ttl zero", func(c *Config) { c.OAuthClientIdleTTL = 0 }, "ACR_OAUTH_CLIENT_IDLE_TTL must be longer"},
		{"idle ttl equals grace", func(c *Config) { c.OAuthClientIdleTTL = c.OAuthRequestPurgeGrace }, "ACR_OAUTH_CLIENT_IDLE_TTL must be longer"},
		{"idle ttl just above grace", func(c *Config) { c.OAuthClientIdleTTL = c.OAuthRequestPurgeGrace + time.Second }, ""},
		{"purge windows unchecked while oauth is off", func(c *Config) {
			c.OAuthIssuer, c.OAuthResources, c.OAuthConsentURL = "", nil, ""
			c.OAuthRequestPurgeGrace, c.OAuthClientIdleTTL = 0, 0
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			cfg.OAuthResources = append([]string(nil), valid.OAuthResources...)
			tc.mutate(&cfg)
			err := validateOAuthConfig(cfg)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoad_oauthSettingsParse(t *testing.T) {
	cfg, err := load(mapLookup(map[string]string{
		"ACR_LOCAL_COMPOSITION_READY":         "true",
		"ACR_OAUTH_RESOURCES":                 " https://a.example.test/mcp , https://b.example.test/mcp",
		"ACR_OAUTH_CLIENT_METADATA_DOCUMENTS": "false",
	}))
	if err == nil || !strings.Contains(err.Error(), "ACR_OAUTH_ISSUER is required") {
		t.Fatalf("resources without issuer: err = %v", err)
	}
	cfg, err = load(mapLookup(map[string]string{"ACR_LOCAL_COMPOSITION_READY": "true"}))
	if err != nil || cfg.OAuthConfigured() || !cfg.OAuthClientMetadataDocuments {
		t.Fatalf("defaults: configured=%v metadata-documents=%v err=%v", cfg.OAuthConfigured(), cfg.OAuthClientMetadataDocuments, err)
	}
	if got := oauthResources(" https://a.example.test/mcp , https://b.example.test/mcp"); len(got) != 2 || got[1] != "https://b.example.test/mcp" {
		t.Fatalf("oauthResources = %q", got)
	}
}

// ACR_OAUTH_CONSENT_URL passes config validation exactly when acr-api's own
// check (auth.ValidOAuthConsentURL, applied when the app is built) accepts it.
func TestConsentPageURLAgreesWithTheAuthCheck(t *testing.T) {
	for _, value := range []string{
		"https://www.example.com/acr/authorize", "http://localhost:3000/acr/authorize", "http://127.0.0.1/x", "http://[::1]:3000/x",
		"", "https://www.example.com", "https://www.example.com/", "http://www.example.com/acr/authorize",
		"https://www.example.com/acr/authorize?x=1", "https://www.example.com/acr/authorize?", "https://www.example.com/acr/authorize#f",
		"https://user@www.example.com/acr/authorize", "/acr/authorize", "javascript:alert(1)", "ftp://example.com/x", "https:///x",
		"https://www.example.com/acr/authorize#", "https://www.example.com/acr/authorize?#", "https://www.example.com/acr/a%3Fb",
	} {
		if got, want := ConsentPageURL(value), auth.ValidOAuthConsentURL(value); got != want {
			t.Errorf("%q: config %v, auth %v", value, got, want)
		}
	}
}

// CHAOS-6191: the purge windows are parsed at this one site, default to
// 24h / 30d, and a malformed value fails startup (never falls back).
func TestLoad_oauthPurgeWindowsParse(t *testing.T) {
	cfg, err := load(mapLookup(map[string]string{"ACR_LOCAL_COMPOSITION_READY": "true"}))
	if err != nil || cfg.OAuthRequestPurgeGrace != 24*time.Hour || cfg.OAuthClientIdleTTL != 30*24*time.Hour {
		t.Fatalf("defaults: grace=%v idle=%v err=%v", cfg.OAuthRequestPurgeGrace, cfg.OAuthClientIdleTTL, err)
	}
	cfg, err = load(mapLookup(map[string]string{
		"ACR_LOCAL_COMPOSITION_READY":   "true",
		"ACR_OAUTH_REQUEST_PURGE_GRACE": "48h",
		"ACR_OAUTH_CLIENT_IDLE_TTL":     "1440h",
	}))
	if err != nil || cfg.OAuthRequestPurgeGrace != 48*time.Hour || cfg.OAuthClientIdleTTL != 1440*time.Hour {
		t.Fatalf("overrides: grace=%v idle=%v err=%v", cfg.OAuthRequestPurgeGrace, cfg.OAuthClientIdleTTL, err)
	}
	for _, key := range []string{"ACR_OAUTH_REQUEST_PURGE_GRACE", "ACR_OAUTH_CLIENT_IDLE_TTL"} {
		_, err = load(mapLookup(map[string]string{"ACR_LOCAL_COMPOSITION_READY": "true", key: "soon"}))
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s=soon: err = %v, want a parse error naming the key", key, err)
		}
	}
}
