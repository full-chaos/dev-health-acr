package embedprovider

import (
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
