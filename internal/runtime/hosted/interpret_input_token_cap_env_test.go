package hosted

import (
	"bytes"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

func TestInterpretMaxInputTokensFromEnvDomain(t *testing.T) {
	minimum := genkitruntime.MinInterpretInputTokens()
	cases := []struct {
		name     string
		present  bool
		value    string
		want     int
		wantWarn bool
	}{
		{name: "absent is off", present: false},
		{name: "blank is off", present: true, value: ""},
		{name: "zero is off", present: true, value: "0"},
		{name: "a padded value parses", present: true, value: " 90000 ", want: 90000},
		{name: "the minimum is taken", present: true, value: strconv.Itoa(minimum), want: minimum},
		{name: "below the minimum warns and is raised to it", present: true, value: strconv.Itoa(minimum - 1), want: minimum, wantWarn: true},
		{name: "one warns and is raised to the minimum", present: true, value: "1", want: minimum, wantWarn: true},
		{name: "negative warns and is off", present: true, value: "-90000", wantWarn: true},
		{name: "fractional warns and is off", present: true, value: "90000.5", wantWarn: true},
		{name: "non-numeric warns and is off", present: true, value: "lots", wantWarn: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{}
			if tc.present {
				values[EnvInterpretMaxInputTokens] = tc.value
			}
			var logs bytes.Buffer
			got := interpretMaxInputTokensFromEnv(envLookup(values), slog.New(slog.NewTextHandler(&logs, nil)))
			if got != tc.want {
				t.Fatalf("interpretMaxInputTokensFromEnv() = %d, want %d", got, tc.want)
			}
			logged := logs.String()
			if !tc.wantWarn {
				if logged != "" {
					t.Fatalf("log = %q, want silence", logged)
				}
				return
			}
			for _, part := range []string{"WARN", EnvInterpretMaxInputTokens, "interpret", strings.TrimSpace(tc.value)} {
				if !strings.Contains(logged, part) {
					t.Fatalf("log = %q, want it to name %q", logged, part)
				}
			}
		})
	}
}

// The interpret variable and the synthesis variable are separate: each cap
// reads only its own variable.
func TestTheInterpretAndSynthesisTokenCapsReadTheirOwnVariables(t *testing.T) {
	lookup := envLookup(map[string]string{EnvInterpretMaxInputTokens: "90000"})
	if got := synthesisMaxInputTokensFromEnv(lookup, discardLogger()); got != 0 {
		t.Fatalf("synthesis cap = %d, want 0 when only the interpret variable is set", got)
	}
	lookup = envLookup(map[string]string{EnvSynthesisMaxInputTokens: "90000"})
	if got := interpretMaxInputTokensFromEnv(lookup, discardLogger()); got != 0 {
		t.Fatalf("interpret cap = %d, want 0 when only the synthesis variable is set", got)
	}
}

func TestBothModelCompositionPathsCarryTheInterpretInputTokenCap(t *testing.T) {
	configured := envLookup(map[string]string{modelprovider.EnvAPIKey: "sk-test", EnvInterpretMaxInputTokens: "90000"})
	unconfigured := envLookup(map[string]string{EnvInterpretMaxInputTokens: "90000"})

	deployment, err := contextFabricModelConfigFromEnv(configured, nil, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if deployment.MaxInterpretInputTokens != 90000 {
		t.Fatalf("deployment default cap = %d, want 90000", deployment.MaxInterpretInputTokens)
	}
	for name, lookup := range map[string]func(string) (string, bool){"configured": configured, "unconfigured": unconfigured} {
		defaults, err := contextFabricModelDefaults(lookup, discardLogger())
		if err != nil {
			t.Fatal(err)
		}
		if defaults.MaxInterpretInputTokens != 90000 {
			t.Fatalf("%s per-organization default cap = %d, want 90000", name, defaults.MaxInterpretInputTokens)
		}
	}
}
