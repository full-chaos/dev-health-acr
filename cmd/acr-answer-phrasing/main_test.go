package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAnswer(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "answer.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const assertedAnswer = `{"schema_version":"context_fabric_investigation_result.v1","result_id":"result_cli_0001","status":"partial",` +
	`"claimed_facts":[{"claim_id":"claim-risk-0001","kind":"health","subject":{"kind":"team","canonical_id":"team:linear:CHAOS","label":"Fullchaos"},"field":"compounding_risk","value":{"number":0.73}}],` +
	`"strongest_pressures":["The compounding risk score is 0.73.","The compounding risk score appears to be 0.73."]}`

// TestRunCountsBareAndWrappedAnswers: a bare result and a stream result event
// give the same row, and the totals add them up.
func TestRunCountsBareAndWrappedAnswers(t *testing.T) {
	t.Parallel()
	bare := writeAnswer(t, assertedAnswer)
	wrapped := writeAnswer(t, `{"type":"result","result":`+assertedAnswer+`}`)
	var out bytes.Buffer
	if err := run([]string{bare, wrapped}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.Count(out.String(), "\tresult_cli_0001\tpartial\t\t1\t2\t1\n"); got != 2 {
		t.Fatalf("rows for the two answers = %d, want 2; output:\n%s", got, out.String())
	}
	if !strings.Contains(out.String(), "TOTAL\tanswers=2\tanswers_stating_a_derived_number=2\tanswers_asserting_one=2\tderived_number_sentences=4\tasserted=2") {
		t.Fatalf("totals wrong; output:\n%s", out.String())
	}
}

// TestRunFailsOnAnythingThatIsNotAnAnswer: a measurement that did not happen is
// never reported as an answer with nothing derived in it.
func TestRunFailsOnAnythingThatIsNotAnAnswer(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"empty wrapped result": `{"result":{}}`,
		"foreign wrapped":      `{"result":{"schema_version":"context_packet.v1"}}`,
		"empty object":         `{}`,
		"not json":             `result`,
	} {
		var out bytes.Buffer
		if err := run([]string{writeAnswer(t, body)}, &out); err == nil {
			t.Errorf("%s: run succeeded, want an error; output:\n%s", name, out.String())
		}
	}
	if err := run(nil, &bytes.Buffer{}); err == nil {
		t.Error("run with no files succeeded, want the usage error")
	}
}
