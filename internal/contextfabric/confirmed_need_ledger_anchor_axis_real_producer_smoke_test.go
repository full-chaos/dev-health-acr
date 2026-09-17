package contextfabric

// A same-package smoke test over RunCaptureAxisFieldRealProducerScenarioForTest:
// every field/value pair the scenario claims to produce must actually be on
// the emitted line, so a regression in the scenario wiring itself (not the
// eventspec declaration) fails HERE, close to the fixture.

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCaptureAxisFieldRealProducerScenariosEmitTheClaimedValues(t *testing.T) {
	t.Parallel()
	scenarios := CaptureAxisFieldRealProducerScenarios()
	if len(scenarios) == 0 {
		t.Fatal("CaptureAxisFieldRealProducerScenarios() is empty")
	}
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			raw, _, want := RunCaptureAxisFieldRealProducerScenarioForTest(t, scenario)
			if len(want) == 0 {
				t.Fatalf("scenario %q claims no field/value pairs", scenario)
			}
			var line map[string]any
			for _, rawLine := range bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n")) {
				if len(rawLine) == 0 {
					continue
				}
				var rec map[string]any
				if err := json.Unmarshal(rawLine, &rec); err != nil {
					t.Fatalf("captured line is not JSON: %v -- line: %s", err, rawLine)
				}
				if rec["msg"] == "context fabric confirmed need ledger" {
					line = rec
				}
			}
			if line == nil {
				t.Fatalf("no confirmed need ledger JSON line captured for scenario %q", scenario)
			}
			for key, wantValue := range want {
				if got, _ := line[key].(string); got != wantValue {
					t.Errorf("%s = %q, want %q", key, got, wantValue)
				}
			}
		})
	}
}
