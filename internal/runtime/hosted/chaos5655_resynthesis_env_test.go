package hosted

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

// TestSynthesisResynthesisAttemptsFromEnvDomain is the construction-time
// INPUT DOMAIN for EnvSynthesisResynthesisAttempts: absent, blank,
// whitespace-padded, the documented default, the ceiling, zero, negative,
// fractional, non-numeric, and one past the ceiling -- every cell
// synthesisResynthesisAttemptsFromEnv's own guard can reach. A malformed
// cell must default AND log exactly one WARN naming the variable; a missing
// or blank cell must default SILENTLY (no WARN), matching
// newContextFabricModelRuntime's own "an operator who never touched this
// knob gets no line about it" convention for every other zero-value default
// in this composition.
func TestSynthesisResynthesisAttemptsFromEnvDomain(t *testing.T) {
	cases := []struct {
		name      string
		present   bool
		value     string
		want      int
		wantWarn  bool
		wantValue string // substring the WARN line must name, when wantWarn
	}{
		{name: "absent defaults silently", present: false, want: genkitruntime.DefaultMaxSynthesisResynthesisAttempts},
		{name: "blank defaults silently", present: true, value: "", want: genkitruntime.DefaultMaxSynthesisResynthesisAttempts},
		{name: "whitespace-only defaults silently", present: true, value: "   ", want: genkitruntime.DefaultMaxSynthesisResynthesisAttempts},
		{name: "whitespace-padded valid value parses", present: true, value: " 2 ", want: 2},
		{name: "the documented default as a literal", present: true, value: "3", want: 3},
		{name: "one is the minimum, accepted", present: true, value: "1", want: 1},
		{name: "the ceiling itself is accepted", present: true, value: "5", want: genkitruntime.MaxSynthesisResynthesisAttemptsCeiling},
		{name: "zero is malformed, warns and defaults", present: true, value: "0", want: genkitruntime.DefaultMaxSynthesisResynthesisAttempts, wantWarn: true, wantValue: "0"},
		{name: "negative is malformed, warns and defaults", present: true, value: "-1", want: genkitruntime.DefaultMaxSynthesisResynthesisAttempts, wantWarn: true, wantValue: "-1"},
		{name: "fractional is malformed, warns and defaults", present: true, value: "1.5", want: genkitruntime.DefaultMaxSynthesisResynthesisAttempts, wantWarn: true, wantValue: "1.5"},
		{name: "non-numeric is malformed, warns and defaults", present: true, value: "many", want: genkitruntime.DefaultMaxSynthesisResynthesisAttempts, wantWarn: true, wantValue: "many"},
		{name: "one past the ceiling is malformed, warns and defaults", present: true, value: "6", want: genkitruntime.DefaultMaxSynthesisResynthesisAttempts, wantWarn: true, wantValue: "6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{}
			if tc.present {
				values[EnvSynthesisResynthesisAttempts] = tc.value
			}
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))

			got := synthesisResynthesisAttemptsFromEnv(envLookup(values), logger)

			if got != tc.want {
				t.Fatalf("synthesisResynthesisAttemptsFromEnv() = %d, want %d", got, tc.want)
			}
			logged := logs.String()
			if tc.wantWarn {
				if !strings.Contains(logged, "WARN") {
					t.Fatalf("startup log = %q, want a WARN level record", logged)
				}
				if !strings.Contains(logged, EnvSynthesisResynthesisAttempts) {
					t.Fatalf("startup log = %q, want it to name %s", logged, EnvSynthesisResynthesisAttempts)
				}
				if !strings.Contains(logged, tc.wantValue) {
					t.Fatalf("startup log = %q, want it to carry the malformed value %q so an operator can see what they set", logged, tc.wantValue)
				}
			} else if logged != "" {
				t.Fatalf("startup log = %q, want silence -- an operator who never set (or left this knob at a valid value) must get no line about it", logged)
			}
		})
	}
}

// TestContextFabricModelConfigFromEnvWiresResynthesisAttempts is the
// composition-root integration proof: the deployment-default runtime's own
// modelprovider.Config actually carries the parsed value, not only the
// standalone parser above.
func TestContextFabricModelConfigFromEnvWiresResynthesisAttempts(t *testing.T) {
	lookup := envLookup(map[string]string{
		modelprovider.EnvAPIKey:         "sk-test",
		EnvSynthesisResynthesisAttempts: "2",
	})

	cfg, err := contextFabricModelConfigFromEnv(lookup, nil, discardLogger())
	if err != nil {
		t.Fatalf("contextFabricModelConfigFromEnv() error = %v", err)
	}
	if cfg.MaxSynthesisResynthesisAttempts != 2 {
		t.Fatalf("cfg.MaxSynthesisResynthesisAttempts = %d, want 2", cfg.MaxSynthesisResynthesisAttempts)
	}
}
