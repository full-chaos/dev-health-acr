package config

import (
	"log/slog"
	"strings"
	"testing"
)

func mapLookup(values map[string]string) lookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != ":8080" || cfg.Environment != "development" {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("unexpected log level: %v", cfg.LogLevel)
	}
	if cfg.RequireBackingStores {
		t.Fatal("development must not require backing stores by default")
	}
}

func TestProductionRequiresBackingStores(t *testing.T) {
	_, err := load(mapLookup(map[string]string{"ACR_ENVIRONMENT": "production"}))
	if err == nil {
		t.Fatal("expected production configuration error")
	}
}

func TestLoad_rejectsNonCanonicalEntitlementKey(t *testing.T) {
	_, err := load(mapLookup(map[string]string{"ACR_ENTITLEMENT_KEY": "different_policy"}))
	if err == nil || !strings.Contains(err.Error(), "agent_context_runtime") {
		t.Fatalf("load() error = %v, want canonical entitlement rejection", err)
	}
}

func TestProductionRequiresEvidenceKeyring(t *testing.T) {
	_, err := load(mapLookup(map[string]string{
		"ACR_ENVIRONMENT":    "production",
		"ACR_CLICKHOUSE_DSN": "clickhouse://redacted",
		"ACR_POSTGRES_DSN":   "postgres://redacted?sslmode=verify-full",
	}))
	if err == nil {
		t.Fatal("expected production evidence keyring configuration error")
	}
}

func TestProductionCannotDisableKeyringValidationWithBackingStoreOverride(t *testing.T) {
	_, err := load(mapLookup(map[string]string{
		"ACR_ENVIRONMENT":            "production",
		"ACR_REQUIRE_BACKING_STORES": "false",
	}))
	if err == nil {
		t.Fatal("production disabled backing stores without an evidence keyring")
	}
}

func TestProductionAcceptsConfiguredBackingStores(t *testing.T) {
	cfg, err := load(mapLookup(map[string]string{
		"ACR_ENVIRONMENT":                       "production",
		"ACR_CLICKHOUSE_DSN":                    "clickhouse://redacted",
		"ACR_POSTGRES_DSN":                      "postgres://redacted?sslmode=verify-full",
		"ACR_LOG_LEVEL":                         "warn",
		"ACR_EVIDENCE_ID_ACTIVE_KID":            "current",
		"ACR_EVIDENCE_ID_KEYS":                  "current=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		"ACR_DEV_HEALTH_ENTITLEMENT_URL":        "https://ops.example.test",
		"ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE": "/run/secrets/ops-token",
		"ACR_POSTGRES_CONNECTION_KIND":          "direct",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RequireBackingStores || cfg.LogLevel != slog.LevelWarn {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestProductionRequiresDevHealthEntitlementConfiguration(t *testing.T) {
	// Given
	base := map[string]string{
		"ACR_ENVIRONMENT":            "production",
		"ACR_REQUIRE_BACKING_STORES": "false",
		"ACR_CLICKHOUSE_DSN":         "clickhouse://redacted",
		"ACR_POSTGRES_DSN":           "postgres://redacted?sslmode=verify-full",
		"ACR_EVIDENCE_ID_ACTIVE_KID": "current",
		"ACR_EVIDENCE_ID_KEYS":       "current=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
	}

	for _, test := range []struct {
		name   string
		values map[string]string
		field  string
	}{
		{"missing URL", base, "ACR_DEV_HEALTH_ENTITLEMENT_URL"},
		{"missing token file", map[string]string{
			"ACR_ENVIRONMENT": "production", "ACR_REQUIRE_BACKING_STORES": "false",
			"ACR_CLICKHOUSE_DSN": "clickhouse://redacted", "ACR_POSTGRES_DSN": "postgres://redacted?sslmode=verify-full",
			"ACR_EVIDENCE_ID_ACTIVE_KID": "current", "ACR_EVIDENCE_ID_KEYS": "current=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
			"ACR_DEV_HEALTH_ENTITLEMENT_URL": "https://ops.example.test",
		}, "ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// When
			_, err := load(mapLookup(test.values))

			// Then
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("load() error = %v; want missing %s", err, test.field)
			}
		})
	}
}

func TestProductionRejectsInsecureDevHealthEntitlementURL(t *testing.T) {
	// Given
	values := map[string]string{
		"ACR_ENVIRONMENT": "production", "ACR_REQUIRE_BACKING_STORES": "false",
		"ACR_CLICKHOUSE_DSN": "clickhouse://redacted", "ACR_POSTGRES_DSN": "postgres://redacted?sslmode=verify-full",
		"ACR_EVIDENCE_ID_ACTIVE_KID": "current", "ACR_EVIDENCE_ID_KEYS": "current=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		"ACR_DEV_HEALTH_ENTITLEMENT_URL":                     "http://127.0.0.1:8080",
		"ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE":              "/run/secrets/ops-token",
		"ACR_DEV_HEALTH_ENTITLEMENT_ALLOW_INSECURE_LOOPBACK": "true",
		"ACR_POSTGRES_CONNECTION_KIND":                       "direct",
	}

	// When
	_, err := load(mapLookup(values))

	// Then
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("load() error = %v; want HTTPS requirement", err)
	}
}

func TestSafeAttributesDoNotContainDSNs(t *testing.T) {
	cfg, err := load(mapLookup(map[string]string{
		"ACR_CLICKHOUSE_DSN": "clickhouse://user:secret@example",
		"ACR_POSTGRES_DSN":   "postgres://user:secret@example",
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range cfg.SafeAttributes() {
		if text, ok := value.(string); ok && (text == cfg.ClickHouseDSN || text == cfg.PostgresDSN) {
			t.Fatal("safe attributes leaked a DSN")
		}
	}
}

func TestTrustedProxyCIDRsAreParsedWithoutLoggingValues(t *testing.T) {
	cfg, err := load(mapLookup(map[string]string{"ACR_TRUSTED_PROXY_CIDRS": "10.0.0.0/8, 192.0.2.0/24"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.TrustedProxyCIDRs) != 2 {
		t.Fatalf("trusted proxies = %#v", cfg.TrustedProxyCIDRs)
	}
	for _, attribute := range cfg.SafeAttributes() {
		if text, ok := attribute.(string); ok && strings.Contains(text, "10.0.0.0") {
			t.Fatal("safe attributes leaked trusted network details")
		}
	}
}

func TestTrustedProxyCIDRsRejectInvalidNetwork(t *testing.T) {
	if _, err := load(mapLookup(map[string]string{"ACR_TRUSTED_PROXY_CIDRS": "not-a-cidr"})); err == nil {
		t.Fatal("invalid trusted proxy network accepted")
	}
}

func TestRevokedClientVersionsRequireCanonicalSemVer(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "canonical versions", value: "1.2.3,2.0.0-rc.1+build.7", want: true},
		{name: "leading v", value: "v1.2.3"},
		{name: "development sentinel", value: "dev"},
		{name: "malformed", value: "latest"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// When
			_, err := load(mapLookup(map[string]string{"ACR_REVOKED_CLIENT_VERSIONS": test.value}))

			// Then
			if got := err == nil; got != test.want {
				t.Fatalf("load() success = %t, want %t (err=%v)", got, test.want, err)
			}
		})
	}
}
