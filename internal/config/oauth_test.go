package config

import (
	"strings"
	"testing"
)

func TestValidateOAuthConfigDomain(t *testing.T) {
	valid := Config{
		OAuthIssuer: "https://acr.example.test", OAuthResources: []string{"https://mcp.example.test/mcp"},
		WebAssertionJWKSFile: "/run/jwks.json", RequireBackingStores: true,
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"valid", func(*Config) {}, ""},
		{"off", func(c *Config) { c.OAuthIssuer, c.OAuthResources = "", nil }, ""},
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
