package config

import (
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"
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
		"ACR_DEVICE_VERIFICATION_URL":           "https://verify.example.test/device",
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

func TestProductionAcceptsPlainHTTPDevHealthEntitlementURL(t *testing.T) {
	// Given
	values := map[string]string{
		"ACR_ENVIRONMENT": "production", "ACR_REQUIRE_BACKING_STORES": "false",
		"ACR_CLICKHOUSE_DSN": "clickhouse://redacted", "ACR_POSTGRES_DSN": "postgres://redacted?sslmode=verify-full",
		"ACR_EVIDENCE_ID_ACTIVE_KID": "current", "ACR_EVIDENCE_ID_KEYS": "current=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		"ACR_DEV_HEALTH_ENTITLEMENT_URL":        "http://ops.internal:8000",
		"ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE": "/run/secrets/ops-token",
		"ACR_DEVICE_VERIFICATION_URL":           "https://verify.example.test/device",
		"ACR_POSTGRES_CONNECTION_KIND":          "direct",
	}

	// When
	cfg, err := load(mapLookup(values))

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DevHealthEntitlementURL != "http://ops.internal:8000" {
		t.Fatalf("entitlement URL = %q, want plain internal origin", cfg.DevHealthEntitlementURL)
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

func TestLoad_webAssertionsRequireCompleteConfiguration(t *testing.T) {
	// Given
	values := map[string]string{"ACR_WEB_ASSERTION_ISSUER": "https://web.example.test"}

	// When
	_, err := load(mapLookup(values))

	// Then
	if err == nil || !strings.Contains(err.Error(), "ACR_WEB_ASSERTION_AUDIENCE") {
		t.Fatalf("load() error = %v, want missing assertion audience", err)
	}
}

func TestLoad_webAssertionsRetainFixedIssuerAudienceAndJWKSPath(t *testing.T) {
	// Given
	values := map[string]string{
		"ACR_WEB_ASSERTION_ISSUER":    "https://web.example.test",
		"ACR_WEB_ASSERTION_AUDIENCE":  "acr-api",
		"ACR_WEB_ASSERTION_JWKS_FILE": "/run/secrets/acr-web-assertions.jwks.json",
	}

	// When
	cfg, err := load(mapLookup(values))

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebAssertionIssuer != values["ACR_WEB_ASSERTION_ISSUER"] || cfg.WebAssertionAudience != values["ACR_WEB_ASSERTION_AUDIENCE"] || cfg.WebAssertionJWKSFile != values["ACR_WEB_ASSERTION_JWKS_FILE"] {
		t.Fatalf("web assertion config = %#v", cfg)
	}
}

// TestLoad_answerReuseMaxAgeDefaultsToDisabled binds the CHAOS-3782
// correction: leaving ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE unset must
// leave AnswerReuseMaxAge at zero (disabled), never a default duration --
// answer reuse is opt-in.
func TestLoad_answerReuseMaxAgeDefaultsToDisabled(t *testing.T) {
	cfg, err := load(mapLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnswerReuseMaxAge != 0 {
		t.Fatalf("AnswerReuseMaxAge = %v, want 0 (disabled) when unset", cfg.AnswerReuseMaxAge)
	}
}

func TestLoad_answerReuseMaxAgeAcceptsAnExplicitWindow(t *testing.T) {
	cfg, err := load(mapLookup(map[string]string{"ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE": "30m"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnswerReuseMaxAge.String() != "30m0s" {
		t.Fatalf("AnswerReuseMaxAge = %v, want 30m0s", cfg.AnswerReuseMaxAge)
	}
}

func TestLoad_answerReuseMaxAgeRejectsBelowMinimum(t *testing.T) {
	_, err := load(mapLookup(map[string]string{"ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE": "30s"}))
	if err == nil || !strings.Contains(err.Error(), "ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE") {
		t.Fatalf("load() error = %v, want an ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE bounds error", err)
	}
}

func TestLoad_answerReuseMaxAgeRejectsAboveMaximum(t *testing.T) {
	_, err := load(mapLookup(map[string]string{"ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE": "48h"}))
	if err == nil || !strings.Contains(err.Error(), "ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE") {
		t.Fatalf("load() error = %v, want an ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE bounds error", err)
	}
}

func TestLoad_answerReuseMaxAgeRejectsNegativeDuration(t *testing.T) {
	_, err := load(mapLookup(map[string]string{"ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE": "-5m"}))
	if err == nil || !strings.Contains(err.Error(), "ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE") {
		t.Fatalf("load() error = %v, want an ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE bounds error", err)
	}
}

func TestLoad_answerReuseMaxAgeExplicitZeroStaysDisabled(t *testing.T) {
	cfg, err := load(mapLookup(map[string]string{"ACR_CONTEXT_FABRIC_ANSWER_REUSE_MAX_AGE": "0s"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnswerReuseMaxAge != 0 {
		t.Fatalf("AnswerReuseMaxAge = %v, want 0 (disabled)", cfg.AnswerReuseMaxAge)
	}
}

// CHAOS-4330: this is the exact live pair (ACR_REQUEST_TIMEOUT raised for
// real investigations, ACR_WRITE_TIMEOUT left at its default) that let a
// slow investigation get its connection closed by http.Server.WriteTimeout
// mid-write while the handler still logged a successful "request completed
// ... status 200" -- confirmed live before this fix existed.
func TestLoad_rejectsWriteTimeoutBelowRequestTimeoutPlusHeadroom(t *testing.T) {
	_, err := load(mapLookup(map[string]string{
		"ACR_REQUEST_TIMEOUT": "490s",
		// ACR_WRITE_TIMEOUT left unset -- defaults to 20s, far below 490s.
	}))
	if err == nil || !strings.Contains(err.Error(), "ACR_WRITE_TIMEOUT") ||
		!strings.Contains(err.Error(), "ACR_REQUEST_TIMEOUT") {
		t.Fatalf("load() error = %v, want an ACR_WRITE_TIMEOUT/ACR_REQUEST_TIMEOUT coherence error", err)
	}
}

// The default pair itself (15s/20s, exactly minWriteTimeoutHeadroom apart)
// must stay valid -- the ticket that added this check is explicit that
// changing the defaults is out of scope.
func TestLoad_defaultTimeoutPairRemainsValid(t *testing.T) {
	if _, err := load(mapLookup(nil)); err != nil {
		t.Fatalf("load() with default timeouts error = %v, want no error", err)
	}
}

// An explicit pair that clears the headroom exactly at the boundary (not
// merely by a wide margin) must be accepted -- this is an off-by-one check
// on the comparison operator itself.
func TestLoad_acceptsWriteTimeoutExactlyAtTheMinimumHeadroom(t *testing.T) {
	_, err := load(mapLookup(map[string]string{
		"ACR_REQUEST_TIMEOUT": "490s",
		"ACR_WRITE_TIMEOUT":   "495s",
	}))
	if err != nil {
		t.Fatalf("load() error = %v, want no error at exactly the minimum headroom", err)
	}
}

// CHAOS-4330 (codex review): the headroom check must not use plain
// addition (RequestTimeout + minWriteTimeoutHeadroom) -- time.Duration is
// signed int64 nanoseconds, and adding two independently-valid-but-large
// values near the type's own maximum could overflow and wrap negative,
// making an absurdly incoherent pair pass the check it exists to fail.
// ACR_REQUEST_TIMEOUT at time.Duration's own maximum, paired with a
// 1-second ACR_WRITE_TIMEOUT (about as incoherent as this pair can be),
// must still be REJECTED, not silently waved through by an overflow.
func TestLoad_rejectsIncoherentPairEvenAtDurationsMaximum(t *testing.T) {
	huge := time.Duration(math.MaxInt64).String()
	_, err := load(mapLookup(map[string]string{
		"ACR_REQUEST_TIMEOUT": huge,
		"ACR_WRITE_TIMEOUT":   "1s",
	}))
	if err == nil || !strings.Contains(err.Error(), "ACR_WRITE_TIMEOUT") {
		t.Fatalf("load() error = %v, want an ACR_WRITE_TIMEOUT/ACR_REQUEST_TIMEOUT coherence error even at the maximum representable ACR_REQUEST_TIMEOUT (not silently accepted via integer overflow)", err)
	}
}
