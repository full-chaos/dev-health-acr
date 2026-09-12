package hosted

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// THE ENV NAME IS A CONTRACT WITH THE MEASUREMENT RIG.
//
// The corpus run that decides whether N>1 ships preflights on this exact
// string. If it is ever renamed without renaming it there too, the rig sets a
// variable nothing reads and reports an N=1 result as an N=3 one -- a silently
// wrong measurement, which is worse than a failed one.
func TestTheEnsembleSizeEnvNameIsStable(t *testing.T) {
	t.Parallel()
	if InterpretationEnsembleSizeEnv != "ACR_CONTEXT_FABRIC_INTERPRETATION_ENSEMBLE_SIZE" {
		t.Fatalf("env name = %q; the measurement rig preflights on the literal string", InterpretationEnsembleSizeEnv)
	}
}

func TestInterpretationEnsembleSizeFromEnv(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		set      bool
		value    string
		want     int
		wantWarn bool
	}{
		{name: "unset is one", set: false, want: 1},
		{name: "empty is one", set: true, value: "", want: 1},
		{name: "whitespace only is one", set: true, value: "   ", want: 1},
		{name: "one is one", set: true, value: "1", want: 1},
		{name: "three is three", set: true, value: "3", want: 3},
		{name: "surrounding whitespace is tolerated", set: true, value: "  3  ", want: 3},
		// A malformed knob must not take the API down. It must also not be
		// silent: an operator who set it believing it took effect learns
		// otherwise from the log.
		{name: "non-numeric warns and defaults", set: true, value: "three", want: 1, wantWarn: true},
		{name: "zero warns and defaults", set: true, value: "0", want: 1, wantWarn: true},
		{name: "negative warns and defaults", set: true, value: "-2", want: 1, wantWarn: true},
		{name: "float warns and defaults", set: true, value: "2.5", want: 1, wantWarn: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
			lookup := func(key string) (string, bool) {
				if key != InterpretationEnsembleSizeEnv {
					t.Fatalf("looked up %q, want only the ensemble size variable", key)
				}
				return testCase.value, testCase.set
			}
			got := InterpretationEnsembleSizeFromEnv(lookup, logger)
			if got != testCase.want {
				t.Fatalf("size = %d, want %d", got, testCase.want)
			}
			warned := buf.Len() > 0
			if warned != testCase.wantWarn {
				t.Fatalf("warned = %v, want %v (log %q)", warned, testCase.wantWarn, buf.String())
			}
			if !testCase.wantWarn {
				return
			}
			var line map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
				t.Fatalf("warning is not one JSON line: %v", err)
			}
			if line["env"] != InterpretationEnsembleSizeEnv {
				t.Fatalf("warning does not name the variable: %v", line["env"])
			}
			if value, _ := line["value"].(string); !strings.Contains(value, strings.TrimSpace(testCase.value)) {
				t.Fatalf("warning does not carry the rejected value: %v", line["value"])
			}
		})
	}
}

// A nil lookup is the composition never having wired one. It defaults rather
// than panicking: this is a performance knob, and no configuration source is
// the same statement as no configuration.
func TestANilLookupDefaultsToOneSample(t *testing.T) {
	t.Parallel()
	if got := InterpretationEnsembleSizeFromEnv(nil, nil); got != 1 {
		t.Fatalf("size = %d, want 1", got)
	}
}
