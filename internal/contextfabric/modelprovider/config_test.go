package modelprovider

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acrconfig "github.com/full-chaos/dev-health-acr/internal/config"
)

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestConfigured_isFalseWithoutAnyProviderSelection(t *testing.T) {
	// Given an environment that carries the Context Fabric read flag and an
	// ambient OpenAI credential, but no ACR model provider selection.
	lookup := lookupFrom(map[string]string{
		"ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED": "true",
		"OPENAI_API_KEY":                         "sk-ambient-must-not-be-consulted",
		"OPENAI_BASE_URL":                        "https://ambient.example.com/v1/",
	})

	// When / Then
	if Configured(lookup) {
		t.Fatal("Configured() = true from ambient OPENAI_* variables; opting into a paid provider must be an explicit ACR configuration decision")
	}
}

func TestConfigured_acceptsEitherCredentialOrBaseURL(t *testing.T) {
	// Given / When / Then
	cases := map[string]map[string]string{
		"direct credential": {EnvAPIKey: "sk-test"},
		"credential file":   {EnvAPIKey + "_FILE": "/run/secrets/model-key"},
		"base URL only":     {EnvBaseURL: "http://127.0.0.1:11434/v1/"},
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			if !Configured(lookupFrom(values)) {
				t.Fatalf("Configured(%v) = false, want true", values)
			}
		})
	}
	if Configured(lookupFrom(map[string]string{EnvAPIKey: "   "})) {
		t.Fatal("Configured() = true for a blank credential")
	}
}

// TestConfigured_treatsAnyModelVariableAsOptingIn is the CHAOS-3770 F5
// probe: setting ONLY a model name, or ONLY a tuning variable, with no
// credential and no base URL, must still opt into full config parsing.
// Before this fix, Configured() consulted only EnvAPIKey/EnvAPIKey_FILE/
// EnvBaseURL, so a model-only (or timeout-only, or provider-only)
// environment silently reported "unconfigured" -- newContextFabricModelRuntime
// then returned (nil, nil), and the caller's setting was discarded with a
// clean per-request 503 instead of the startup failure AC-3770-2 requires
// for a mis-specified configuration.
func TestConfigured_treatsAnyModelVariableAsOptingIn(t *testing.T) {
	cases := map[string]map[string]string{
		"model only":                   {EnvModel: "gpt-5-mini"},
		"provider only":                {EnvProvider: "acme-gateway"},
		"fallback model only":          {EnvFallbackModel: "gpt-5.6-luna"},
		"timeout only":                 {EnvTimeout: "60s"},
		"max attempts only":            {EnvMaxAttempts: "3"},
		"max transport retries only":   {EnvMaxTransportRetries: "0"},
		"allow insecure base url only": {EnvAllowInsecureBaseURL: "true"},
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			if !Configured(lookupFrom(values)) {
				t.Fatalf("Configured(%v) = false, want true -- a nonblank ACR_CONTEXT_FABRIC_MODEL* variable must opt in even alone", values)
			}
		})
	}
}

func TestConfigFromEnv_appliesProviderShapedDefaults(t *testing.T) {
	// Given only a credential -- the minimum a hosted deployment supplies.
	lookup := lookupFrom(map[string]string{EnvAPIKey: "sk-test"})

	// When
	cfg, err := ConfigFromEnv(lookup)
	if err != nil {
		t.Fatal(err)
	}

	// Then the CHAOS-3770 decision (OpenAI + gpt-5-nano) is the default,
	// and nothing else is inferred.
	if cfg.Provider != DefaultProvider || cfg.Model != DefaultModel {
		t.Fatalf("provider/model = %q/%q, want %q/%q", cfg.Provider, cfg.Model, DefaultProvider, DefaultModel)
	}
	if cfg.BaseURL != "" {
		t.Fatalf("base URL = %q, want empty (resolved to the provider default at client construction)", cfg.BaseURL)
	}
	if cfg.FallbackModel != "" {
		t.Fatalf("fallback model = %q, want empty (a fallback is a second billable call and must be opted into)", cfg.FallbackModel)
	}
	if cfg.Timeout != defaultTimeout || cfg.MaxAttempts != defaultMaxAttempts || cfg.MaxTransportRetries != defaultMaxTransportRetries {
		t.Fatalf("bounds = %v/%d/%d, want %v/%d/%d", cfg.Timeout, cfg.MaxAttempts, cfg.MaxTransportRetries,
			defaultTimeout, defaultMaxAttempts, defaultMaxTransportRetries)
	}
	if cfg.AllowInsecureBaseURL {
		t.Fatal("insecure base URLs are permitted by default")
	}
}

func TestConfigFromEnv_supportsBYOEndpointWithoutCredential(t *testing.T) {
	// Given a co-located OpenAI-compatible server with no authentication --
	// the BYO LLM case that must need no code change.
	lookup := lookupFrom(map[string]string{
		EnvProvider:             "local-llama",
		EnvBaseURL:              "http://127.0.0.1:11434/v1/",
		EnvModel:                "meta-llama/Llama-3.1-8B-Instruct",
		EnvAllowInsecureBaseURL: "true",
		EnvMaxTransportRetries:  "0",
	})

	// When
	cfg, err := ConfigFromEnv(lookup)
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if cfg.Provider != "local-llama" || cfg.Model != "meta-llama/Llama-3.1-8B-Instruct" || cfg.APIKey != "" {
		t.Fatalf("config = %#v, want the BYO endpoint accepted verbatim with no credential", cfg)
	}
}

func TestConfigFromEnv_readsCredentialFromSecretFile(t *testing.T) {
	// Given a mounted secret file, the KEY_FILE half of the internal/config
	// convention.
	path := filepath.Join(t.TempDir(), "model-key")
	if err := os.WriteFile(path, []byte("sk-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// When
	cfg, err := ConfigFromEnv(lookupFrom(map[string]string{EnvAPIKey + "_FILE": path}))
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if cfg.APIKey != "sk-from-file" {
		t.Fatalf("APIKey = %q, want the trimmed file contents", cfg.APIKey)
	}
}

func TestConfigFromEnv_rejectsConflictingCredentialSources(t *testing.T) {
	// Given both halves of the secret convention.
	lookup := lookupFrom(map[string]string{EnvAPIKey: "sk-direct", EnvAPIKey + "_FILE": "/run/secrets/model-key"})

	// When
	_, err := ConfigFromEnv(lookup)

	// Then
	if !errors.Is(err, acrconfig.ErrSecretSourceConflict) {
		t.Fatalf("err = %v, want ErrSecretSourceConflict", err)
	}
}

func TestConfigFromEnv_rejectsInvalidConfigurations(t *testing.T) {
	// Given / When / Then
	cases := map[string]struct {
		values map[string]string
		want   string
	}{
		"plaintext base URL without the insecure opt-in": {
			values: map[string]string{EnvBaseURL: "http://models.example.com/v1/"},
			want:   EnvAllowInsecureBaseURL,
		},
		"non-http base URL": {
			values: map[string]string{EnvBaseURL: "ftp://models.example.com/v1/"},
			want:   "http or https",
		},
		"relative base URL": {
			values: map[string]string{EnvBaseURL: "/v1/"},
			want:   "absolute URL",
		},
		"hostless base URL": {
			values: map[string]string{EnvBaseURL: "unix:///var/run/model.sock"},
			want:   "absolute URL",
		},
		"insecure opt-in with no base URL": {
			values: map[string]string{EnvAPIKey: "sk-test", EnvAllowInsecureBaseURL: "true"},
			want:   "no effect",
		},
		"provider containing a path separator": {
			values: map[string]string{EnvAPIKey: "sk-test", EnvProvider: "acme/gateway"},
			want:   "path separator",
		},
		"model with an empty path segment": {
			values: map[string]string{EnvAPIKey: "sk-test", EnvModel: "/gpt-5-nano"},
			want:   "empty path segment",
		},
		"fallback identical to the primary model": {
			values: map[string]string{EnvAPIKey: "sk-test", EnvFallbackModel: DefaultModel},
			want:   "different model",
		},
		"timeout outside the supported band": {
			values: map[string]string{EnvAPIKey: "sk-test", EnvTimeout: "10m"},
			want:   EnvTimeout,
		},
		"unparseable timeout": {
			values: map[string]string{EnvAPIKey: "sk-test", EnvTimeout: "soon"},
			want:   "valid duration",
		},
		"attempts outside the supported band": {
			values: map[string]string{EnvAPIKey: "sk-test", EnvMaxAttempts: "9"},
			want:   EnvMaxAttempts,
		},
		"negative transport retries": {
			values: map[string]string{EnvAPIKey: "sk-test", EnvMaxTransportRetries: "-1"},
			want:   EnvMaxTransportRetries,
		},
		"unparseable insecure flag": {
			values: map[string]string{EnvAPIKey: "sk-test", EnvAllowInsecureBaseURL: "yes-please"},
			want:   "boolean",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ConfigFromEnv(lookupFrom(testCase.values))
			if err == nil {
				t.Fatalf("ConfigFromEnv(%v) = nil error, want a failure naming %q", testCase.values, testCase.want)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("err = %q, want it to name %q", err, testCase.want)
			}
		})
	}
}

func TestConfigFromEnv_requiresCredentialOnTheDefaultEndpoint(t *testing.T) {
	// Given a base-URL-free selection reached through the credential-file
	// half of the convention pointing at a file that resolves to nothing.
	path := filepath.Join(t.TempDir(), "empty-key")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// When
	_, err := ConfigFromEnv(lookupFrom(map[string]string{EnvAPIKey + "_FILE": path}))

	// Then the default provider endpoint authenticates every request, so an
	// empty credential must fail composition rather than produce a runtime
	// that 401s on every investigation.
	if err == nil || !strings.Contains(err.Error(), EnvAPIKey) {
		t.Fatalf("err = %v, want a failure naming %s", err, EnvAPIKey)
	}
}

func TestNewClientOptions_neutralizesAmbientProviderEnvironment(t *testing.T) {
	// Given ambient OPENAI_* variables that the OpenAI SDK seeds itself
	// from, and an ACR configuration that names neither a base URL nor a
	// credential override for them.
	t.Setenv("OPENAI_API_KEY", "sk-ambient-must-not-be-sent")
	t.Setenv("OPENAI_BASE_URL", "https://ambient.example.com/v1/")
	cfg := Config{
		Provider: DefaultProvider, Model: DefaultModel, APIKey: "sk-configured",
		Timeout: time.Minute, MaxAttempts: 1, MaxTransportRetries: 0,
	}

	// When
	server := recordingProvider(t, chatCompletion(t, `{}`))
	cfg.BaseURL, cfg.AllowInsecureBaseURL = server.baseURL, true
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}

	// Then the explicit options must be the last word: the request reaches
	// the configured host with the configured credential, not the ambient
	// ones.
	callProvider(t, cfg)
	if got := server.lastAuthorization(); got != "Bearer sk-configured" {
		t.Fatalf("authorization = %q, want the configured credential, not the ambient one", got)
	}
	if server.calls() == 0 {
		t.Fatal("no request reached the configured base URL; an ambient OPENAI_BASE_URL redirected the traffic")
	}
}
