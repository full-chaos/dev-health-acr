package sidecar

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// This file is a dedicated canary suite for the exact leak class the
// CHAOS-2908 Oracle review found in durationOrDefault/int64OrDefault/
// boolOrDefault: those three functions used to wrap the underlying
// strconv/time parser's own error via
// fmt.Errorf("%s is invalid: %w", key, err), and that wrapped error text
// embeds the raw configured value verbatim (Go's strconv and time
// packages both include the offending input in their Error() strings by
// design). A configured value shaped like a bearer credential or an
// embedded userinfo secret -- placed in *any* parsed config field, not
// just ACR_API_URL -- must never reach a returned error, because
// acr-mcp doctor prints every config error's Error() text verbatim in
// its JSON output.
//
// Every case below plants a real, well-formed ACR API bearer token
// (auth.GenerateToken, "fcacr_"-prefixed) together with a userinfo-
// shaped secret in the field under test, then asserts the returned
// error's Error() text contains neither the token, the secret, nor the
// raw configured value as a whole -- while also proving the absent/
// malformed/valid status tri-state (no value set -> default applied;
// the canary value -> rejected; an ordinary valid value -> accepted) is
// unchanged by the sanitization. ClientName/ClientVersion/SidecarVersion
// are not covered here: stringOrDefault only trims and stores those
// values, with no parse/validation failure mode, so there is no error
// text for a canary to leak into.

// canaryToken returns a real, well-formed ACR API bearer token so canary
// values below are not merely bearer-*shaped* strings but the exact
// artifact doctor and support tooling must never echo.
func canaryToken(t *testing.T) string {
	t.Helper()
	token, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

const canaryUserinfoSecret = "tops3cr3t-userinfo-password-42"

// assertConfigErrorNeverLeaks fails the test unless err is non-nil (the
// canary value must always be rejected) and its Error() text contains
// neither the bearer token, the userinfo-shaped secret, nor the raw
// configured value supplied as raw.
func assertConfigErrorNeverLeaks(t *testing.T, err error, token, raw string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected the canary value to be rejected")
	}
	message := err.Error()
	if strings.Contains(message, token) {
		t.Fatalf("config error leaked the bearer token: %v", err)
	}
	if strings.Contains(message, canaryUserinfoSecret) {
		t.Fatalf("config error leaked the userinfo secret: %v", err)
	}
	if strings.Contains(message, raw) {
		t.Fatalf("config error leaked the raw configured value: %v", err)
	}
}

// TestLoadConfigCanaryValuesNeverLeakIntoParsedFieldErrors covers every
// parsed (non-URL, non-path) config field -- timeout, the two byte-size
// ceilings, the insecure-loopback flag, and the proxy URL override --
// with an absent/canary/valid table per field, so the sanitization fix
// is proven for the whole parsing surface at once, not just the field
// the Oracle review happened to name.
func TestLoadConfigCanaryValuesNeverLeakIntoParsedFieldErrors(t *testing.T) {
	token := canaryToken(t)
	canary := token + ":" + canaryUserinfoSecret + "@evil.example.com"

	type statusCase struct {
		name  string
		value string
		valid bool
	}

	fields := []struct {
		env   string
		cases []statusCase
	}{
		{TimeoutEnvironment, []statusCase{
			{"absent", "", true},
			{"canary", canary, false},
			{"valid", "5s", true},
		}},
		{MaxResponseBytesEnvironment, []statusCase{
			{"absent", "", true},
			{"canary", canary, false},
			{"valid", "65536", true},
		}},
		{MaxRequestBodyBytesEnvironment, []statusCase{
			{"absent", "", true},
			{"canary", canary, false},
			{"valid", "65536", true},
		}},
		{AllowInsecureLoopbackEnvironment, []statusCase{
			{"absent", "", true},
			{"canary", canary, false},
			{"valid", "false", true},
		}},
		{ProxyURLEnvironment, []statusCase{
			{"absent", "", true},
			// "://" forces url.Parse itself to fail (missing protocol
			// scheme), the same shape TestLoadConfigRejectsInvalidProxyURL
			// already proves is rejected -- here with a real token and
			// userinfo secret riding along in the invalid input.
			{"canary", "://" + canary, false},
			{"valid", "http://proxy.local:3128", true},
		}},
	}

	for _, field := range fields {
		for _, tc := range field.cases {
			t.Run(field.env+"/"+tc.name, func(t *testing.T) {
				env := map[string]string{APIURLEnvironment: "https://acr.example.com"}
				if tc.value != "" {
					env[field.env] = tc.value
				}
				_, err := loadConfig(lookupFromMap(env))
				switch {
				case tc.valid && err != nil:
					t.Fatalf("unexpected error for %s=%q: %v", field.env, tc.value, err)
				case !tc.valid:
					assertConfigErrorNeverLeaks(t, err, token, tc.value)
				}
			})
		}
	}
}

// TestLoadConfigCanaryValueNeverLeaksFromAPIURLUserinfo extends the
// existing userinfo-leak canaries (config_url_test.go,
// TestLoadConfigRejectsMalformedURLWithoutLeakingUserinfoSecret) with a
// real bearer token riding alongside the userinfo secret, covering both
// leak surfaces this field has: url.Parse's own error text on a
// malformed component, and validateOriginOnly's fixed rejection of a
// well-formed one.
func TestLoadConfigCanaryValueNeverLeaksFromAPIURLUserinfo(t *testing.T) {
	token := canaryToken(t)

	malformed := "https://" + token + ":" + canaryUserinfoSecret + "%zz@acr.example.com"
	_, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment: malformed,
	}))
	assertConfigErrorNeverLeaks(t, err, token, malformed)

	wellFormed := "https://" + token + ":" + canaryUserinfoSecret + "@acr.example.com"
	_, err = loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment: wellFormed,
	}))
	assertConfigErrorNeverLeaks(t, err, token, wellFormed)
}

// TestLoadConfigCanaryValueNeverLeaksFromCACertPath covers the two
// distinct leak surfaces ACR_API_CA_BUNDLE has: the configured *path*
// itself (describeFileError, shared with loadCACertPool in
// api_client.go), and the bundle *content* once a real file is read.
// Neither must ever have a bearer token or userinfo secret riding along
// in it reach a returned error, and a genuinely valid bundle must still
// be accepted afterward -- the "valid" status leg of the tri-state this
// canary suite exists to preserve.
func TestLoadConfigCanaryValueNeverLeaksFromCACertPath(t *testing.T) {
	token := canaryToken(t)

	nonexistent := filepath.Join(t.TempDir(), token+"-"+canaryUserinfoSecret+".pem")
	_, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:     "https://acr.example.com",
		CACertPathEnvironment: nonexistent,
	}))
	assertConfigErrorNeverLeaks(t, err, token, nonexistent)

	invalidPEMPath := filepath.Join(t.TempDir(), "ca.pem")
	invalidContent := "not a certificate: " + token + " " + canaryUserinfoSecret
	if err := os.WriteFile(invalidPEMPath, []byte(invalidContent), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:     "https://acr.example.com",
		CACertPathEnvironment: invalidPEMPath,
	}))
	assertConfigErrorNeverLeaks(t, err, token, invalidContent)

	validPath := filepath.Join(t.TempDir(), "valid-ca.pem")
	if err := os.WriteFile(validPath, generateTestCACertPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:     "https://acr.example.com",
		CACertPathEnvironment: validPath,
	})); err != nil {
		t.Fatalf("unexpected error for a valid CA bundle: %v", err)
	}
}

// TestDescribeConfigErrorFallsBackForNonConfigErrors proves the doctor-
// facing defense-in-depth: DescribeConfigError only trusts a *ConfigError
// to already be safe. Any other error type -- the shape a future config-
// parsing code path would produce if it forgot to build a *ConfigError,
// the same mistake durationOrDefault/int64OrDefault/boolOrDefault made
// before this fix -- collapses to a fixed, generic description instead
// of having its Error() text (which might embed a raw configured value)
// surfaced verbatim.
func TestDescribeConfigErrorFallsBackForNonConfigErrors(t *testing.T) {
	if got := DescribeConfigError(nil); got != "" {
		t.Fatalf("expected an empty description for a nil error, got %q", got)
	}

	token := canaryToken(t)
	leaky := &leakyConfigLikeError{detail: "ACR_API_TIMEOUT is invalid: " + token}
	got := DescribeConfigError(leaky)
	if strings.Contains(got, token) {
		t.Fatalf("DescribeConfigError leaked a non-ConfigError's text: %q", got)
	}
	if got != "configuration is invalid (unclassified error)" {
		t.Fatalf("unexpected fallback description: %q", got)
	}

	safe := &ConfigError{Field: TimeoutEnvironment, Detail: "must be a valid Go duration (e.g. \"30s\", \"2m\")"}
	if got := DescribeConfigError(safe); got != safe.Error() {
		t.Fatalf("expected a *ConfigError's own Error() text, got %q", got)
	}
}

// leakyConfigLikeError simulates the exact mistake this fix removes: an
// error type that is not *ConfigError but whose Error() text embeds a
// raw value. DescribeConfigError must not be fooled by it.
type leakyConfigLikeError struct{ detail string }

func (e *leakyConfigLikeError) Error() string { return e.detail }
