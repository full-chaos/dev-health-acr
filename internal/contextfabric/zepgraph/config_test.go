package zepgraph

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	zep "github.com/getzep/zep-go/v3"
	zepcore "github.com/getzep/zep-go/v3/core"
)

func validConfig() Config {
	return Config{
		BaseURL: "https://api.getzep.com/api/v2", APIKey: "test-key", GraphPrefix: "acr-cf",
		RequestTimeout: 30 * time.Second, MaxAttempts: 3, MaxResults: 25,
	}
}

func TestConfigValidateRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(Config) Config
		wantErr bool
	}{
		{"valid", func(c Config) Config { return c }, false},
		{"missing base URL", func(c Config) Config { c.BaseURL = ""; return c }, true},
		{"non-absolute base URL", func(c Config) Config { c.BaseURL = "not-a-url"; return c }, true},
		{"http without allow-insecure", func(c Config) Config { c.BaseURL = "http://zep.internal/api/v2"; return c }, true},
		{"http with allow-insecure", func(c Config) Config { c.BaseURL = "http://127.0.0.1:9999/api/v2"; c.AllowInsecure = true; return c }, false},
		{"missing API key", func(c Config) Config { c.APIKey = ""; return c }, true},
		{"timeout too low", func(c Config) Config { c.RequestTimeout = 500 * time.Millisecond; return c }, true},
		{"timeout too high", func(c Config) Config { c.RequestTimeout = 3 * time.Minute; return c }, true},
		{"attempts zero", func(c Config) Config { c.MaxAttempts = 0; return c }, true},
		{"attempts too high", func(c Config) Config { c.MaxAttempts = 6; return c }, true},
		{"max results zero", func(c Config) Config { c.MaxResults = 0; return c }, true},
		{"max results too high", func(c Config) Config { c.MaxResults = 51; return c }, true},
		{"missing graph prefix", func(c Config) Config { c.GraphPrefix = ""; return c }, true},
		{"graph prefix too long", func(c Config) Config { c.GraphPrefix = string(make([]byte, 33)); return c }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.mutate(validConfig()).validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestZepStatusCodeClassifiesTypedSDKErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"not found", &zep.NotFoundError{}, http.StatusNotFound},
		{"bad request", &zep.BadRequestError{}, http.StatusBadRequest},
		{"forbidden", &zep.ForbiddenError{}, http.StatusForbidden},
		{"conflict", &zep.ConflictError{}, http.StatusConflict},
		{"internal", &zep.InternalServerError{}, http.StatusInternalServerError},
		{"generic API error", &zepcore.APIError{StatusCode: http.StatusTooManyRequests}, http.StatusTooManyRequests},
		{"unclassified", errors.New("boom"), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := zepStatusCode(tc.err); got != tc.want {
				t.Fatalf("zepStatusCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestSafeDependencyErrorClassifiesAndHidesDependencyBodies(t *testing.T) {
	t.Parallel()
	secretMessage := "internal-secret-payload-should-never-leak"
	rawBody := &zep.APIError{Message: ptr(secretMessage)}
	cases := []struct {
		name   string
		err    error
		wantIs error
	}{
		{"not found", &zep.NotFoundError{Body: rawBody}, ErrNotFound},
		{"unauthorized", &zep.ForbiddenError{Body: rawBody}, ErrUnauthorized},
		{"rate limited", zepcore.NewAPIError(http.StatusTooManyRequests, nil, errors.New(secretMessage)), ErrRateLimited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := safeDependencyError("op", tc.err)
			if !errors.Is(got, tc.wantIs) {
				t.Fatalf("safeDependencyError() = %v, want Is(%v)", got, tc.wantIs)
			}
			if got.Error() == "" || strings.Contains(got.Error(), secretMessage) {
				t.Fatalf("safeDependencyError() = %q must not embed the dependency body", got.Error())
			}
		})
	}
	unclassified := safeDependencyError("op", &zep.InternalServerError{Body: rawBody})
	if unclassified == nil || unclassified.Error() != "op: graph dependency unavailable" {
		t.Fatalf("unclassified safeDependencyError() = %v", unclassified)
	}
}

func TestSafeDependencyErrorPassesThroughContextErrors(t *testing.T) {
	t.Parallel()
	if got := safeDependencyError("op", context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatalf("safeDependencyError(context.Canceled) = %v", got)
	}
	if got := safeDependencyError("op", context.DeadlineExceeded); !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("safeDependencyError(context.DeadlineExceeded) = %v", got)
	}
	if safeDependencyError("op", nil) != nil {
		t.Fatal("safeDependencyError(nil) must be nil")
	}
}

func TestConfiguredReportsWhetherZepIsSelected(t *testing.T) {
	t.Parallel()
	empty := func(string) (string, bool) { return "", false }
	if Configured(empty) {
		t.Fatal("Configured() = true for an unset environment")
	}
	set := func(key string) (string, bool) {
		if key == EnvBaseURL {
			return "https://api.getzep.com/api/v2", true
		}
		return "", false
	}
	if !Configured(set) {
		t.Fatal("Configured() = false when base URL is set")
	}
	blank := func(key string) (string, bool) {
		if key == EnvBaseURL {
			return "   ", true
		}
		return "", false
	}
	if Configured(blank) {
		t.Fatal("Configured() = true for a blank base URL")
	}
}

func TestConfigFromEnvUsesConventionalNamesAndDefaults(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		EnvBaseURL: "https://api.getzep.com/api/v2",
		EnvAPIKey:  "env-key",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	cfg, err := ConfigFromEnv(lookup)
	if err != nil {
		t.Fatalf("ConfigFromEnv() error = %v", err)
	}
	if cfg.BaseURL != values[EnvBaseURL] || cfg.APIKey != "env-key" || cfg.GraphPrefix != "acr-cf" {
		t.Fatalf("cfg = %#v", cfg)
	}
	if cfg.RequestTimeout != 30*time.Second || cfg.MaxAttempts != 3 || cfg.MaxResults != 25 || cfg.AllowInsecure {
		t.Fatalf("cfg defaults = %#v", cfg)
	}
}

func TestConfigFromEnvHonorsOverridesAndSecretFileConvention(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		EnvBaseURL:        "http://127.0.0.1:9999/api/v2",
		EnvAPIKey:         "direct-key",
		EnvGraphPrefix:    "acr-cf-staging",
		EnvRequestTimeout: "45s",
		EnvMaxAttempts:    "2",
		EnvMaxResults:     "10",
		EnvAllowInsecure:  "true",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	cfg, err := ConfigFromEnv(lookup)
	if err != nil {
		t.Fatalf("ConfigFromEnv() error = %v", err)
	}
	if cfg.GraphPrefix != "acr-cf-staging" || cfg.RequestTimeout != 45*time.Second || cfg.MaxAttempts != 2 || cfg.MaxResults != 10 || !cfg.AllowInsecure {
		t.Fatalf("cfg overrides = %#v", cfg)
	}
}

func TestConfigFromEnvRejectsConflictingAPIKeySources(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		EnvBaseURL:          "https://api.getzep.com/api/v2",
		EnvAPIKey:           "direct-key",
		EnvAPIKey + "_FILE": "/tmp/does-not-matter",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	if _, err := ConfigFromEnv(lookup); err == nil {
		t.Fatal("ConfigFromEnv() error = nil, want conflicting secret source error")
	}
}

func TestConfigFromEnvRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	values := map[string]string{EnvAPIKey: "env-key"}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	if _, err := ConfigFromEnv(lookup); err == nil {
		t.Fatal("ConfigFromEnv() error = nil, want missing base URL error")
	}
}
