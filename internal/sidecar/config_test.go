package sidecar

import (
	"strings"
	"testing"
)

// lookupFromMap adapts a plain map to the lookupEnv seam loadConfig uses in
// place of os.LookupEnv, so tests can exercise loadConfig deterministically
// without mutating real process environment variables. It is shared by
// every config_*_test.go file in this package.
func lookupFromMap(values map[string]string) lookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadConfigRequiresAPIURL(t *testing.T) {
	if _, err := loadConfig(lookupFromMap(nil)); err == nil {
		t.Fatal("missing ACR_API_URL was accepted")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment: "https://acr.example.com",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIBaseURL == nil || cfg.APIBaseURL.String() != "https://acr.example.com" {
		t.Fatalf("unexpected base URL: %#v", cfg.APIBaseURL)
	}
	if cfg.Timeout != defaultTimeout {
		t.Fatalf("unexpected default timeout: %s", cfg.Timeout)
	}
	if cfg.MaxResponseBytes != defaultMaxResponseBytes {
		t.Fatalf("unexpected default max response bytes: %d", cfg.MaxResponseBytes)
	}
	if cfg.MaxRequestBodyBytes != defaultMaxRequestBodyBytes {
		t.Fatalf("unexpected default max request body bytes: %d", cfg.MaxRequestBodyBytes)
	}
	if cfg.ClientName != defaultClientName || cfg.ClientVersion != defaultClientVersion || cfg.SidecarVersion != defaultSidecarVersion {
		t.Fatalf("unexpected default identity: %#v", cfg)
	}
	if cfg.AllowInsecureLoopback {
		t.Fatal("insecure loopback must default to false")
	}
	if cfg.EnableWriteback {
		t.Fatal("writeback must default to false")
	}
	if cfg.EnableTranscriptCapture {
		t.Fatal("transcript capture must default to false")
	}
}

func TestLoadConfigParsesTranscriptCaptureEnablement(t *testing.T) {
	// Given
	lookup := lookupFromMap(map[string]string{
		APIURLEnvironment:                  "https://acr.example.com",
		EnableTranscriptCaptureEnvironment: "true",
	})

	// When
	cfg, err := loadConfig(lookup)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EnableTranscriptCapture {
		t.Fatal("transcript capture was not enabled")
	}
}

func TestLoadConfigRejectsMalformedTranscriptCaptureEnablementWithoutEchoingValue(t *testing.T) {
	// Given
	const canary = "invalid-transcript-capture-canary"
	lookup := lookupFromMap(map[string]string{
		APIURLEnvironment:                  "https://acr.example.com",
		EnableTranscriptCaptureEnvironment: canary,
	})

	// When
	_, err := loadConfig(lookup)

	// Then
	if err == nil {
		t.Fatal("malformed transcript capture value was accepted")
	}
	if strings.Contains(DescribeConfigError(err), canary) {
		t.Fatal("malformed transcript capture value leaked")
	}
}

func TestLoadConfigParsesWritebackEnablement(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "true", raw: "true", want: true},
		{name: "false", raw: "false", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			lookup := lookupFromMap(map[string]string{
				APIURLEnvironment:          "https://acr.example.com",
				EnableWritebackEnvironment: tc.raw,
			})

			// When
			cfg, err := loadConfig(lookup)

			// Then
			if err != nil {
				t.Fatal(err)
			}
			if cfg.EnableWriteback != tc.want {
				t.Fatalf("EnableWriteback=%t, want %t", cfg.EnableWriteback, tc.want)
			}
		})
	}
}

func TestLoadConfigRejectsMalformedWritebackEnablementWithoutEchoingValue(t *testing.T) {
	// Given
	const canary = "not-a-boolean-secret-canary"
	lookup := lookupFromMap(map[string]string{
		APIURLEnvironment:          "https://acr.example.com",
		EnableWritebackEnvironment: canary,
	})

	// When
	_, err := loadConfig(lookup)

	// Then
	if err == nil {
		t.Fatal("malformed writeback value was accepted")
	}
	if strings.Contains(DescribeConfigError(err), canary) {
		t.Fatal("malformed writeback value leaked")
	}
}

func TestLoadConfigRejectsWhitespacePaddedWritebackEnablement(t *testing.T) {
	// Given
	lookup := lookupFromMap(map[string]string{
		APIURLEnvironment:          "https://acr.example.com",
		EnableWritebackEnvironment: " true ",
	})

	// When
	_, err := loadConfig(lookup)

	// Then
	if err == nil {
		t.Fatal("whitespace-padded writeback value was accepted")
	}
}

func TestConfigValidateRejectsNilBaseURL(t *testing.T) {
	cfg := Config{
		Timeout:             defaultTimeout,
		MaxResponseBytes:    defaultMaxResponseBytes,
		MaxRequestBodyBytes: defaultMaxRequestBodyBytes,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("nil base URL was accepted")
	}
}

func TestLoadConfigClientIdentityOverrides(t *testing.T) {
	cfg, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:         "https://acr.example.com",
		ClientNameEnvironment:     "custom-agent",
		ClientVersionEnvironment:  "9.9.9",
		SidecarVersionEnvironment: "1.2.3",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientName != "custom-agent" || cfg.ClientVersion != "9.9.9" || cfg.SidecarVersion != "1.2.3" {
		t.Fatalf("unexpected identity overrides: %#v", cfg)
	}
}
