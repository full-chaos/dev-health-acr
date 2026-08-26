package falkorgraph

import (
	"strings"
	"testing"
	"time"

	acrconfig "github.com/full-chaos/dev-health-acr/internal/config"
)

// This file mirrors zepgraph/config_test.go's shape (Config.validate(),
// Configured(), ConfigFromEnv()) for falkorgraph, which had zero tests of
// its own despite config.go carrying the same validation/env-wiring
// responsibility zepgraph's config.go does.
//
// One real behavioral difference from zepgraph, verified against the actual
// code rather than assumed: zepgraph.ConfigFromEnv calls cfg.validate()
// itself and returns an error for an invalid environment. falkorgraph.
// ConfigFromEnv does NOT call validate() -- it only builds the Config
// struct (see config.go). Validation happens later, at Adapter construction
// (newWithAPI applies defaults, then calls config.validate()). So there is
// no falkorgraph analogue of zepgraph's
// TestConfigFromEnvRejectsInvalidConfiguration: an incomplete environment
// does not make ConfigFromEnv itself fail; it produces a Config that later
// fails validate() when an Adapter is actually constructed from it. Tests
// below reflect this real behavior rather than the zepgraph shape.

func validFalkorConfig() Config {
	return Config{
		Addr: "127.0.0.1:6379", TLS: true, GraphPrefix: "acr-cf",
		RequestTimeout: 30 * time.Second, MaxAttempts: 3, MaxResults: 25, PoolSize: 10,
	}
}

// TestConfigValidateRejectsUnsafeConfiguration is the direct port of
// zepgraph's same-named test, table-driven over every validate() branch in
// falkorgraph's config.go.
func TestConfigValidateRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(Config) Config
		wantErr bool
	}{
		{"valid", func(c Config) Config { return c }, false},
		{"missing addr", func(c Config) Config { c.Addr = ""; return c }, true},
		{"addr not host:port", func(c Config) Config { c.Addr = "not-a-host-port"; return c }, true},
		{"addr missing port", func(c Config) Config { c.Addr = "127.0.0.1"; return c }, true},
		{"TLS false without allow-insecure", func(c Config) Config { c.TLS = false; c.AllowInsecure = false; return c }, true},
		{"TLS false with allow-insecure", func(c Config) Config { c.TLS = false; c.AllowInsecure = true; return c }, false},
		{"TLS true needs no allow-insecure", func(c Config) Config { c.TLS = true; c.AllowInsecure = false; return c }, false},
		// CHAOS-3809: TLS=true + AllowInsecure=true is contradictory --
		// AllowInsecure only relaxes the "must use TLS" check above, it does
		// NOT turn TLS off (client.go only omits TLSConfig when c.TLS is
		// false). Before this case, this pair silently passed validate() and
		// produced a client that TLS-handshakes a server the operator
		// explicitly declared might not speak TLS -- a 30s silent hang, not
		// a diagnosable startup error.
		{"TLS true with allow-insecure is contradictory", func(c Config) Config { c.TLS = true; c.AllowInsecure = true; return c }, true},
		{"timeout too low", func(c Config) Config { c.RequestTimeout = 500 * time.Millisecond; return c }, true},
		{"timeout too high", func(c Config) Config { c.RequestTimeout = 3 * time.Minute; return c }, true},
		{"attempts zero", func(c Config) Config { c.MaxAttempts = 0; return c }, true},
		{"attempts too high", func(c Config) Config { c.MaxAttempts = 6; return c }, true},
		{"max results zero", func(c Config) Config { c.MaxResults = 0; return c }, true},
		{"max results too high", func(c Config) Config { c.MaxResults = 51; return c }, true},
		{"missing graph prefix", func(c Config) Config { c.GraphPrefix = ""; return c }, true},
		{"graph prefix too long", func(c Config) Config { c.GraphPrefix = string(make([]byte, 33)); return c }, true},
		{"pool size zero", func(c Config) Config { c.PoolSize = 0; return c }, true},
		{"pool size too high", func(c Config) Config { c.PoolSize = 101; return c }, true},
		{"pool size at upper bound", func(c Config) Config { c.PoolSize = 100; return c }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.mutate(validFalkorConfig()).validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestConfiguredReportsWhetherFalkorIsSelected is the direct port of
// zepgraph's TestConfiguredReportsWhetherZepIsSelected: an unset or blank
// ACR_CONTEXT_FABRIC_FALKOR_ADDR means the deployment did not opt in.
func TestConfiguredReportsWhetherFalkorIsSelected(t *testing.T) {
	t.Parallel()
	empty := func(string) (string, bool) { return "", false }
	if Configured(empty) {
		t.Fatal("Configured() = true for an unset environment")
	}
	set := func(key string) (string, bool) {
		if key == EnvAddr {
			return "falkordb:6379", true
		}
		return "", false
	}
	if !Configured(set) {
		t.Fatal("Configured() = false when addr is set")
	}
	blank := func(key string) (string, bool) {
		if key == EnvAddr {
			return "   ", true
		}
		return "", false
	}
	if Configured(blank) {
		t.Fatal("Configured() = true for a blank addr")
	}
}

// TestConfigFromEnvUsesConventionalNamesAndDefaults is the direct port of
// zepgraph's same-named test: an environment with only the address set must
// produce every other field at its documented default.
func TestConfigFromEnvUsesConventionalNamesAndDefaults(t *testing.T) {
	t.Parallel()
	values := map[string]string{EnvAddr: "falkordb:6379"}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	cfg, err := ConfigFromEnv(lookup)
	if err != nil {
		t.Fatalf("ConfigFromEnv() error = %v", err)
	}
	if cfg.Addr != "falkordb:6379" || cfg.Password != "" || cfg.GraphPrefix != "acr-cf" {
		t.Fatalf("cfg = %#v", cfg)
	}
	if !cfg.TLS {
		t.Fatal("cfg.TLS default = false, want true outside development (design doc §8 environment table)")
	}
	if cfg.RequestTimeout != 30*time.Second || cfg.MaxAttempts != 3 || cfg.MaxResults != 25 || cfg.PoolSize != 10 || cfg.AllowInsecure {
		t.Fatalf("cfg defaults = %#v", cfg)
	}
}

// TestConfigFromEnvHonorsOverridesAndSecretFileConvention is the direct port
// of zepgraph's same-named test, adapted to falkorgraph's real env var names.
func TestConfigFromEnvHonorsOverridesAndSecretFileConvention(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		EnvAddr:           "falkordb-staging:6379",
		EnvPassword:       "direct-password",
		EnvTLS:            "false",
		EnvGraphPrefix:    "acr-cf-staging",
		EnvRequestTimeout: "45s",
		EnvMaxAttempts:    "2",
		EnvMaxResults:     "10",
		EnvPoolSize:       "5",
		EnvAllowInsecure:  "true",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	cfg, err := ConfigFromEnv(lookup)
	if err != nil {
		t.Fatalf("ConfigFromEnv() error = %v", err)
	}
	if cfg.Password != "direct-password" || cfg.TLS || cfg.GraphPrefix != "acr-cf-staging" {
		t.Fatalf("cfg overrides = %#v", cfg)
	}
	if cfg.RequestTimeout != 45*time.Second || cfg.MaxAttempts != 2 || cfg.MaxResults != 10 || cfg.PoolSize != 5 || !cfg.AllowInsecure {
		t.Fatalf("cfg overrides = %#v", cfg)
	}
	// The overridden config (TLS=false, AllowInsecure=true) must still pass
	// validate() -- proving the two knobs are read and wired consistently,
	// not just parsed.
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate() on the overridden config error = %v", err)
	}
}

// TestConfigFromEnvRejectsConflictingPasswordSources is the direct port of
// zepgraph's TestConfigFromEnvRejectsConflictingAPIKeySources, mirrored for
// falkorgraph's EnvPassword secret using acrconfig.SecretValue's documented
// KEY/KEY_FILE conflict behavior (internal/config/secret_file.go).
func TestConfigFromEnvRejectsConflictingPasswordSources(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		EnvAddr:               "falkordb:6379",
		EnvPassword:           "direct-password",
		EnvPassword + "_FILE": "/tmp/does-not-matter",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	_, err := ConfigFromEnv(lookup)
	if err == nil {
		t.Fatal("ConfigFromEnv() error = nil, want conflicting secret source error")
	}
	if !errorsIsSecretSourceConflict(err) {
		t.Fatalf("ConfigFromEnv() error = %v, want it to wrap acrconfig.ErrSecretSourceConflict", err)
	}
}

func errorsIsSecretSourceConflict(err error) bool {
	for err != nil {
		if err == acrconfig.ErrSecretSourceConflict {
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// TestConfigFromEnvPasswordOptionalUnlikeZepAPIKey proves the documented
// difference from zepgraph (config.go's own doc comment): FalkorDB needs no
// external credential to deploy locally, so an entirely unset
// ACR_CONTEXT_FABRIC_FALKOR_PASSWORD must produce an empty password with no
// error, unlike zepgraph's mandatory API key.
func TestConfigFromEnvPasswordOptionalUnlikeZepAPIKey(t *testing.T) {
	t.Parallel()
	values := map[string]string{EnvAddr: "falkordb:6379"}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	cfg, err := ConfigFromEnv(lookup)
	if err != nil {
		t.Fatalf("ConfigFromEnv() error = %v", err)
	}
	if cfg.Password != "" {
		t.Fatalf("cfg.Password = %q, want empty when unset", cfg.Password)
	}
}

// TestConfigFromEnvContradictoryTLSAndAllowInsecureFailsValidation is CHAOS-
// 3809's red-first regression: it drives the contradiction through
// ConfigFromEnv (the real composition path, matching the env-var naming
// convention every deployment actually sets), not a bare Config{} literal --
// the ticket's own root-cause finding is that a bare Config{} zero-values TLS
// to false and never reproduces this trap, which is exactly why the existing
// unit tests never caught it. TLS is left UNSET here (defaults to true per
// TestConfigFromEnvUsesConventionalNamesAndDefaults) alongside an explicit
// ALLOW_INSECURE=true, mirroring an operator who read ALLOW_INSECURE as "make
// this connection insecure" without realizing TLS independently defaults on.
func TestConfigFromEnvContradictoryTLSAndAllowInsecureFailsValidation(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		EnvAddr:          "falkordb:6379",
		EnvAllowInsecure: "true",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	cfg, err := ConfigFromEnv(lookup)
	if err != nil {
		t.Fatalf("ConfigFromEnv() error = %v", err)
	}
	if !cfg.TLS || !cfg.AllowInsecure {
		t.Fatalf("cfg = %#v, want TLS=true (default) and AllowInsecure=true", cfg)
	}
	err = cfg.validate()
	if err == nil {
		t.Fatal("validate() error = nil, want a contradictory-configuration error naming both env vars")
	}
	if !strings.Contains(err.Error(), EnvTLS) || !strings.Contains(err.Error(), EnvAllowInsecure) {
		t.Fatalf("validate() error = %q, want it to name both %s and %s", err.Error(), EnvTLS, EnvAllowInsecure)
	}
}

// TestConfigFromEnvExplicitTLSTrueAndAllowInsecureFailsValidation is the same
// contradiction with TLS set EXPLICITLY to "true" rather than relying on the
// default, proving the check fires on the value, not merely on the default
// path.
func TestConfigFromEnvExplicitTLSTrueAndAllowInsecureFailsValidation(t *testing.T) {
	t.Parallel()
	values := map[string]string{
		EnvAddr:          "falkordb:6379",
		EnvTLS:           "true",
		EnvAllowInsecure: "true",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	cfg, err := ConfigFromEnv(lookup)
	if err != nil {
		t.Fatalf("ConfigFromEnv() error = %v", err)
	}
	err = cfg.validate()
	if err == nil {
		t.Fatal("validate() error = nil, want a contradictory-configuration error naming both env vars")
	}
	if !strings.Contains(err.Error(), EnvTLS) || !strings.Contains(err.Error(), EnvAllowInsecure) {
		t.Fatalf("validate() error = %q, want it to name both %s and %s", err.Error(), EnvTLS, EnvAllowInsecure)
	}
}

// TestConfigFromEnvDoesNotValidate documents (and locks in) the real
// behavioral difference from zepgraph.ConfigFromEnv noted at the top of this
// file: falkorgraph.ConfigFromEnv never calls validate(), so an environment
// missing the mandatory address still produces a (later-invalid) Config with
// a nil error -- validation is deferred to Adapter construction.
func TestConfigFromEnvDoesNotValidate(t *testing.T) {
	t.Parallel()
	lookup := func(string) (string, bool) { return "", false }
	cfg, err := ConfigFromEnv(lookup)
	if err != nil {
		t.Fatalf("ConfigFromEnv() with an entirely empty environment error = %v, want nil (validation is deferred)", err)
	}
	if err := cfg.validate(); err == nil {
		t.Fatal("cfg.validate() on the resulting Config error = nil, want an error (empty Addr) -- proving validation really is deferred, not silently skipped")
	}
}
