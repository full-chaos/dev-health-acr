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

// Absent, blank or zero leaves the cap off silently. A positive value below
// the smallest cap the runtime takes is raised to that minimum with one WARN.
// A value that is not a whole number or is negative leaves the cap off and logs
// one WARN naming the variable and the value.
func TestSynthesisMaxInputTokensFromEnvDomain(t *testing.T) {
	minimum := strconv.Itoa(genkitruntime.MinSynthesisInputTokens)
	below := strconv.Itoa(genkitruntime.MinSynthesisInputTokens - 1)
	cases := []struct {
		name     string
		present  bool
		value    string
		want     int
		wantWarn bool
	}{
		{name: "absent is off", present: false},
		{name: "blank is off", present: true, value: ""},
		{name: "whitespace is off", present: true, value: "  "},
		{name: "zero is off", present: true, value: "0"},
		{name: "a padded value parses", present: true, value: " 90000 ", want: 90000},
		{name: "the minimum is taken", present: true, value: minimum, want: genkitruntime.MinSynthesisInputTokens},
		{name: "below the minimum warns and is raised to it", present: true, value: below, want: genkitruntime.MinSynthesisInputTokens, wantWarn: true},
		{name: "one warns and is raised to the minimum", present: true, value: "1", want: genkitruntime.MinSynthesisInputTokens, wantWarn: true},
		{name: "negative warns and is off", present: true, value: "-90000", wantWarn: true},
		{name: "minus one warns and is off", present: true, value: "-1", wantWarn: true},
		{name: "fractional warns and is off", present: true, value: "90000.5", wantWarn: true},
		{name: "non-numeric warns and is off", present: true, value: "lots", wantWarn: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{}
			if tc.present {
				values[EnvSynthesisMaxInputTokens] = tc.value
			}
			var logs bytes.Buffer
			got := synthesisMaxInputTokensFromEnv(envLookup(values), slog.New(slog.NewTextHandler(&logs, nil)))
			if got != tc.want {
				t.Fatalf("synthesisMaxInputTokensFromEnv() = %d, want %d", got, tc.want)
			}
			logged := logs.String()
			if !tc.wantWarn {
				if logged != "" {
					t.Fatalf("log = %q, want silence", logged)
				}
				return
			}
			for _, part := range []string{"WARN", EnvSynthesisMaxInputTokens, strings.TrimSpace(tc.value)} {
				if !strings.Contains(logged, part) {
					t.Fatalf("log = %q, want it to name %q", logged, part)
				}
			}
		})
	}
}

// Both composition paths carry the parsed cap: the deployment default and
// the per-organization defaults, with and without a configured provider.
func TestBothModelCompositionPathsCarryTheSynthesisInputTokenCap(t *testing.T) {
	configured := envLookup(map[string]string{modelprovider.EnvAPIKey: "sk-test", EnvSynthesisMaxInputTokens: "90000"})
	unconfigured := envLookup(map[string]string{EnvSynthesisMaxInputTokens: "90000"})

	deployment, err := contextFabricModelConfigFromEnv(configured, nil, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if deployment.MaxSynthesisInputTokens != 90000 {
		t.Fatalf("deployment default cap = %d, want 90000", deployment.MaxSynthesisInputTokens)
	}
	for name, lookup := range map[string]func(string) (string, bool){"configured": configured, "unconfigured": unconfigured} {
		defaults, err := contextFabricModelDefaults(lookup, discardLogger())
		if err != nil {
			t.Fatal(err)
		}
		if defaults.MaxSynthesisInputTokens != 90000 {
			t.Fatalf("%s per-organization default cap = %d, want 90000", name, defaults.MaxSynthesisInputTokens)
		}
	}
}
