package embedprovider

import (
	"errors"
	"strings"
	"testing"
)

func lookupOf(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

// TestBodiesIncluded pins the §3 body-gate matrix (CHAOS-3833): the
// explicit gate wins when set; otherwise locality decides; unset locality
// means remote means bodies OFF; garbage in either variable is an error,
// never a default.
func TestBodiesIncluded(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		env     map[string]string
		want    bool
		wantErr bool
	}{
		{name: "unset everything fails closed to off", env: map[string]string{}, want: false},
		{name: "remote locality defaults off", env: map[string]string{EnvProviderLocality: "remote"}, want: false},
		{name: "local locality defaults on", env: map[string]string{EnvProviderLocality: "local"}, want: true},
		{name: "locality is case and whitespace tolerant", env: map[string]string{EnvProviderLocality: "  Local "}, want: true},
		{name: "explicit opt-in overrides remote", env: map[string]string{EnvProviderLocality: "remote", EnvIncludeBodies: "true"}, want: true},
		{name: "explicit opt-in with unset locality", env: map[string]string{EnvIncludeBodies: "true"}, want: true},
		{name: "explicit off overrides local", env: map[string]string{EnvProviderLocality: "local", EnvIncludeBodies: "false"}, want: false},
		{name: "URL-shaped locality is rejected, never inferred", env: map[string]string{EnvProviderLocality: "http://localhost:1234"}, wantErr: true},
		{name: "unknown locality is an error", env: map[string]string{EnvProviderLocality: "loopback"}, wantErr: true},
		{name: "garbage gate value is an error", env: map[string]string{EnvIncludeBodies: "yes please"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := BodiesIncluded(lookupOf(tc.env))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("BodiesIncluded = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("BodiesIncluded error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("BodiesIncluded = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMaxTextRunesFloorIsTheLargestCompleteTemplate pins CHAOS-3833's §0 (c)
// floor: a sub-2,000 cap must fail validation LOUDLY, because below the
// largest complete template the lexical arm would index text the vector arm
// silently truncated away.
func TestMaxTextRunesFloorIsTheLargestCompleteTemplate(t *testing.T) {
	t.Parallel()
	valid := Config{
		Provider: "test", BaseURL: "https://embed.example/v1/", Model: "test-model",
		Dimension: 768, SimilarityFloor: DefaultSimilarityFloor, Timeout: DefaultTimeout,
		MaxBatch: DefaultMaxBatch, MaxTextRunes: MinimumMaxTextRunes,
		// Not this test's concern (CHAOS-4192's credential check) -- opt
		// out explicitly so a MaxTextRunes-floor regression is never
		// masked by an unrelated validate() error.
		AllowNoCredential: true,
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("a cap at the floor must validate, got %v", err)
	}
	tooSmall := valid
	tooSmall.MaxTextRunes = MinimumMaxTextRunes - 1
	err := tooSmall.validate()
	if err == nil {
		t.Fatal("a cap below the floor must fail validation")
	}
	if !strings.Contains(err.Error(), "max text runes") {
		t.Fatalf("floor violation error = %q, want it to name max text runes", err)
	}
}

// TestBlankCredentialFailsClosed pins CHAOS-4192's fail-loud guard through
// the same entry point acr-projector/acr-api actually call
// (ConfigFromEnv), not just Config.validate() in isolation: a configured
// embedder (base URL set) with a blank credential and no explicit
// AllowNoCredential opt-in must be a hard error at config-construction
// time (composition aborts at startup, before any batch runs and before
// any vector is ever cleared) -- never a silent no-op, and never
// discoverable only via a per-batch "embedded:0, cleared:N" log. A real
// credential, or an explicit AllowNoCredential=true (the documented LM
// Studio/Ollama/TEI local no-auth shape), must both still construct
// cleanly. An UNCONFIGURED deployment (base URL unset) must stay the
// existing clean ErrNotConfigured no-op regardless of the credential/
// AllowNoCredential state -- this guard must never turn "vector retrieval
// deliberately off" into a startup failure.
func TestBlankCredentialFailsClosed(t *testing.T) {
	t.Parallel()
	baseEnv := map[string]string{
		EnvBaseURL:   "https://embed.example/v1/",
		EnvProvider:  "test-provider",
		EnvModel:     "test-model",
		EnvDimension: "768",
	}
	withEnv := func(overrides map[string]string) map[string]string {
		merged := make(map[string]string, len(baseEnv)+len(overrides))
		for k, v := range baseEnv {
			merged[k] = v
		}
		for k, v := range overrides {
			merged[k] = v
		}
		return merged
	}

	cases := []struct {
		name       string
		env        map[string]string
		wantErr    bool
		wantErrLog string // substring the error must contain, when wantErr
	}{
		{
			name:       "configured with blank credential and no opt-in fails closed",
			env:        withEnv(nil),
			wantErr:    true,
			wantErrLog: EnvAllowNoCredential,
		},
		{
			name: "configured with blank credential and explicit opt-in succeeds",
			env:  withEnv(map[string]string{EnvAllowNoCredential: "true"}),
		},
		{
			name: "configured with a real credential succeeds without the opt-in",
			env:  withEnv(map[string]string{EnvAPIKey: "sk-real-credential"}),
		},
		{
			name: "unconfigured (no base URL) stays a clean no-op regardless of credential state",
			env:  map[string]string{EnvModel: "test-model", EnvDimension: "768"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ConfigFromEnv(lookupOf(tc.env))
			switch {
			case tc.wantErr:
				if err == nil {
					t.Fatal("ConfigFromEnv = nil error, want a fail-closed error")
				}
				if tc.wantErrLog != "" && !strings.Contains(err.Error(), tc.wantErrLog) {
					t.Fatalf("ConfigFromEnv error = %q, want it to name %q", err, tc.wantErrLog)
				}
			case errors.Is(err, ErrNotConfigured):
				// The unconfigured case: ErrNotConfigured itself IS the
				// expected clean no-op signal, not a test failure.
			case err != nil:
				t.Fatalf("ConfigFromEnv error = %v, want a clean construction", err)
			}
		})
	}
}
