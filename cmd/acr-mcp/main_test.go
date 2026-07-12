package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/version"
)

func validDoctorToken(fill byte) string {
	value := make([]byte, 32)
	for index := range value {
		value[index] = fill
	}
	return "fcacr_" + base64.RawURLEncoding.EncodeToString(value)
}

func TestDoctorReportsCredentialPresenceWithoutValue(t *testing.T) {
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	token := validDoctorToken(7)
	t.Setenv("ACR_API_TOKEN", token)
	result := runDoctor()
	if !result.CredentialSet || !result.CredentialShapeValid || result.CredentialSource != "environment" || result.Status != "ok" {
		t.Fatalf("unexpected doctor result: %#v", result)
	}
	for _, check := range result.Checks {
		if strings.Contains(check.Detail, token) {
			t.Fatal("doctor leaked the credential")
		}
	}
}

func TestMetadataAndDoctorUseInjectedBuildIdentity(t *testing.T) {
	// Given
	originalVersion, originalCommit, originalDate := version.Version, version.Commit, version.Date
	version.Version = "1.2.3-rc.1+build.7"
	version.Commit = "0123456789abcdef0123456789abcdef01234567"
	version.Date = "2026-07-12T15:04:05Z"
	t.Cleanup(func() { version.Version, version.Commit, version.Date = originalVersion, originalCommit, originalDate })
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_TOKEN", validDoctorToken(18))

	// When
	metadata := currentMetadata()
	doctor := runDoctor()

	// Then
	if metadata.Version != version.Version || metadata.Commit != version.Commit || metadata.BuildDate != version.Date {
		t.Fatalf("metadata identity = %#v", metadata)
	}
	if doctor.Version != version.Version || doctor.Commit != version.Commit || doctor.BuildDate != version.Date {
		t.Fatalf("doctor identity = %#v", doctor)
	}
}

func TestDoctorSupportsRestrictedCredentialFile(t *testing.T) {
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_TOKEN", "")
	path := filepath.Join(t.TempDir(), "acr-token")
	if err := os.WriteFile(path, []byte(validDoctorToken(8)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACR_API_TOKEN_FILE", path)
	result := runDoctor()
	if result.Status != "ok" || result.CredentialSource != "file" || !result.CredentialShapeValid {
		t.Fatalf("unexpected doctor result: %#v", result)
	}
}

func TestDoctorRejectsMalformedCredentialWithoutEchoingIt(t *testing.T) {
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_TOKEN", "fcacr_not-valid")
	result := runDoctor()
	if result.Status != "invalid_configuration" || result.CredentialShapeValid {
		t.Fatalf("unexpected doctor result: %#v", result)
	}
	for _, check := range result.Checks {
		if strings.Contains(check.Detail, "fcacr_not-valid") {
			t.Fatal("doctor leaked malformed credential")
		}
	}
}

func TestDoctorReportsAbsentAPIURLWithoutTouchingNetwork(t *testing.T) {
	t.Setenv("ACR_API_URL", "")
	t.Setenv("ACR_API_TOKEN", validDoctorToken(9))
	result := runDoctor()
	if result.APIURLSet || result.APIURLValid {
		t.Fatalf("unexpected doctor result: %#v", result)
	}
	if result.Status != "incomplete_configuration" {
		t.Fatalf("unexpected status: %q", result.Status)
	}
	for _, check := range result.Checks {
		if check.Name == "api_url" && check.Status != "warning" {
			t.Fatalf("unexpected api_url check: %#v", check)
		}
	}
}

func TestDoctorRejectsMalformedAPIURLInsteadOfReportingOk(t *testing.T) {
	t.Setenv("ACR_API_URL", "http://acr.example.test")
	t.Setenv("ACR_API_TOKEN", validDoctorToken(10))
	result := runDoctor()
	if !result.APIURLSet {
		t.Fatal("expected APIURLSet to be true for a nonblank ACR_API_URL")
	}
	if result.APIURLValid {
		t.Fatal("expected APIURLValid to be false for plain HTTP against a non-loopback host")
	}
	if result.Status != "invalid_configuration" {
		t.Fatalf("unexpected status: %q", result.Status)
	}
	found := false
	for _, check := range result.Checks {
		if check.Name == "api_url" {
			found = true
			if check.Status != "error" {
				t.Fatalf("expected api_url check to be an error, got: %#v", check)
			}
		}
	}
	if !found {
		t.Fatal("expected an api_url check to be present")
	}
}

func TestDoctorRejectsAPIURLWithEmbeddedUserinfo(t *testing.T) {
	t.Setenv("ACR_API_URL", "https://user:pass@acr.example.test")
	t.Setenv("ACR_API_TOKEN", validDoctorToken(11))
	result := runDoctor()
	if result.APIURLValid {
		t.Fatal("expected APIURLValid to be false for a URL with embedded userinfo")
	}
	for _, check := range result.Checks {
		if strings.Contains(check.Detail, "user:pass") {
			t.Fatal("doctor echoed embedded URL credentials")
		}
	}
}

func TestDoctorReportsValidAPIURLAsOk(t *testing.T) {
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_TOKEN", validDoctorToken(12))
	result := runDoctor()
	if !result.APIURLSet || !result.APIURLValid {
		t.Fatalf("unexpected doctor result: %#v", result)
	}
	if result.Status != "ok" {
		t.Fatalf("unexpected status: %q", result.Status)
	}
}

func TestDoctorReportsWritebackOnlyForStrictTrueConfig(t *testing.T) {
	// Given
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_TOKEN", validDoctorToken(16))
	t.Setenv("ACR_ENABLE_WRITEBACK", "true")

	// When
	report := runDoctor()

	// Then
	if !report.WriteEnabled {
		t.Fatalf("expected strict writeback opt-in to be reported: %#v", report)
	}
}

func TestDoctorReportsTranscriptCaptureOnlyForStrictTrueConfig(t *testing.T) {
	// Given
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_TOKEN", validDoctorToken(17))
	t.Setenv("ACR_ENABLE_TRANSCRIPT_CAPTURE", "true")

	// When
	report := runDoctor()

	// Then
	if !report.TranscriptCaptureEnabled {
		t.Fatalf("expected strict transcript-capture opt-in to be reported: %#v", report)
	}
}

// TestDoctorRejectsMalformedUserinfoWithoutLeakingSecret is a canary for
// the leak this doctor surface used to be exposed to: url.Parse's own
// error text embeds the full raw input verbatim, so a malformed userinfo
// component (here, one with an invalid percent-escape so url.Parse itself
// fails rather than validateOriginOnly's already-covered well-formed-
// userinfo path) must not have its secret-shaped password reach any
// check's Detail text.
func TestDoctorRejectsMalformedUserinfoWithoutLeakingSecret(t *testing.T) {
	const secret = "tops3cr3t-do-not-leak"
	t.Setenv("ACR_API_URL", "https://user:"+secret+"%zz@acr.example.test")
	t.Setenv("ACR_API_TOKEN", validDoctorToken(13))
	result := runDoctor()
	if result.APIURLValid {
		t.Fatal("expected APIURLValid to be false for a malformed URL")
	}
	for _, check := range result.Checks {
		if strings.Contains(check.Detail, secret) {
			t.Fatalf("doctor leaked the malformed URL's userinfo secret: %#v", check)
		}
	}
}

// TestDoctorRejectsForceQueryAPIURL proves a bare trailing "?" (RawQuery
// empty but url.URL.ForceQuery true) is rejected rather than reported ok.
func TestDoctorRejectsForceQueryAPIURL(t *testing.T) {
	t.Setenv("ACR_API_URL", "https://acr.example.test?")
	t.Setenv("ACR_API_TOKEN", validDoctorToken(14))
	result := runDoctor()
	if result.APIURLValid {
		t.Fatal("expected APIURLValid to be false for a bare trailing '?' URL")
	}
}

// TestDoctorRejectsUnusableCABundleWithoutLeakingPath proves an invalid
// CA bundle is now caught by LoadConfig itself (Config.Validate's parity
// check with loadCACertPool), and that the configured path is never
// echoed into the diagnostic.
func TestDoctorRejectsUnusableCABundleWithoutLeakingPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unusable-ca.pem")
	if err := os.WriteFile(path, []byte("not a real certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACR_API_URL", "https://acr.example.test")
	t.Setenv("ACR_API_CA_BUNDLE", path)
	t.Setenv("ACR_API_TOKEN", validDoctorToken(15))
	result := runDoctor()
	if result.APIURLValid {
		t.Fatal("expected APIURLValid to be false for an unusable CA bundle")
	}
	for _, check := range result.Checks {
		if strings.Contains(check.Detail, path) {
			t.Fatalf("doctor leaked the CA bundle path: %#v", check)
		}
	}
}

// doctorCanaryUserinfoSecret is a userinfo-shaped secret planted
// alongside a real bearer token in every case of
// TestDoctorRejectsCanaryConfigValuesWithoutLeakingBearerTokenOrUserinfoSecret
// below.
const doctorCanaryUserinfoSecret = "d0ct0r-userinfo-password-99"

// TestDoctorRejectsCanaryConfigValuesWithoutLeakingBearerTokenOrUserinfoSecret
// is the process-level canary for the exact leak the CHAOS-2908 Oracle
// review found: durationOrDefault/int64OrDefault/boolOrDefault used to
// wrap the underlying strconv/time parser's own error text, which
// embeds the raw configured value verbatim, so a full valid "fcacr_"
// bearer token (or a userinfo-shaped secret) placed in ACR_API_TIMEOUT,
// ACR_API_MAX_RESPONSE_BYTES, ACR_API_MAX_REQUEST_BODY_BYTES,
// ACR_API_ALLOW_INSECURE_LOOPBACK, or ACR_API_PROXY_URL would previously
// have reached this JSON-encoded, operator-facing surface verbatim.
// Every case here asserts the malformed/invalid_configuration status is
// still reported correctly (the sanitization must not turn a rejected
// value into a false "ok") while none of the check details ever
// contain the token, the userinfo secret, or the raw configured value.
func TestDoctorRejectsCanaryConfigValuesWithoutLeakingBearerTokenOrUserinfoSecret(t *testing.T) {
	canaryToken := validDoctorToken(99)
	canary := canaryToken + ":" + doctorCanaryUserinfoSecret + "@evil.example.test"

	cases := []struct {
		name  string
		env   string
		value string
	}{
		{"timeout", "ACR_API_TIMEOUT", canary},
		{"max_response_bytes", "ACR_API_MAX_RESPONSE_BYTES", canary},
		{"max_request_body_bytes", "ACR_API_MAX_REQUEST_BODY_BYTES", canary},
		{"allow_insecure_loopback", "ACR_API_ALLOW_INSECURE_LOOPBACK", canary},
		{"proxy_url", "ACR_API_PROXY_URL", "://" + canary},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ACR_API_URL", "https://acr.example.test")
			t.Setenv("ACR_API_TOKEN", validDoctorToken(100))
			t.Setenv(tc.env, tc.value)
			result := runDoctor()
			if result.APIURLValid {
				t.Fatalf("expected APIURLValid to be false for a malformed %s", tc.env)
			}
			if result.Status != "invalid_configuration" {
				t.Fatalf("expected status invalid_configuration for a malformed %s, got %q", tc.env, result.Status)
			}
			for _, check := range result.Checks {
				if strings.Contains(check.Detail, canaryToken) {
					t.Fatalf("doctor leaked the bearer token via %s: %#v", tc.env, check)
				}
				if strings.Contains(check.Detail, doctorCanaryUserinfoSecret) {
					t.Fatalf("doctor leaked the userinfo secret via %s: %#v", tc.env, check)
				}
				if strings.Contains(check.Detail, tc.value) {
					t.Fatalf("doctor leaked the raw configured value via %s: %#v", tc.env, check)
				}
			}
		})
	}
}
