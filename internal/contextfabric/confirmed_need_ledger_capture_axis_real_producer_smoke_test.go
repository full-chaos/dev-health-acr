package contextfabric

// A same-package smoke test over RunCaptureSkipReasonRealProducerScenarioForTest:
// the emitted line's own "capture_skip_reason" key must equal the scenario
// name, so a regression in the scenario wiring itself (not the eventspec
// declaration) fails HERE, close to the fixture.

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCaptureSkipReasonRealProducerScenariosEmitTheNamedReason(t *testing.T) {
	t.Parallel()
	scenarios := CaptureSkipReasonRealProducerScenarios()
	if len(scenarios) != len(captureSkipReasons()) {
		t.Fatalf("scenario count = %d, want %d (one per CaptureSkipReason)", len(scenarios), len(captureSkipReasons()))
	}
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			raw, _ := RunCaptureSkipReasonRealProducerScenarioForTest(t, scenario)
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
			if got, _ := line["capture_skip_reason"].(string); got != scenario {
				t.Fatalf("capture_skip_reason = %q, want %q", got, scenario)
			}
		})
	}
}
