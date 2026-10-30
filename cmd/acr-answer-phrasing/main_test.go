package main

import (
	"bytes"
	"encoding/json"
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

// assertedAnswer is the contract's example result, which passes the stored
// read validation, with one more claim, a compounding risk score of 0.73, and
// two pressures that state it: one plainly, one qualified.
func assertedAnswer(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "examples", "v1", "context_fabric_investigation_result.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["result_id"] = "result_cli_0001"
	doc["claimed_facts"] = append(doc["claimed_facts"].([]any), map[string]any{
		"claim_id": "claim_risk_cli_0001", "kind": "health", "field": "compounding_risk", "value": map[string]any{"number": 0.73},
		"subject": map[string]any{"kind": "team", "canonical_id": "team:CHAOS", "label": "CHAOS"},
	})
	completeness := doc["completeness"].(map[string]any)
	completeness["claimed_facts_count"] = len(doc["claimed_facts"].([]any))
	doc["strongest_pressures"] = []string{"The compounding risk score is 0.73.", "The compounding risk score appears to be 0.73."}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// TestRunCountsBareAndWrappedAnswers: a bare result and a stream result event
// give the same row, and the totals add them up.
func TestRunCountsBareAndWrappedAnswers(t *testing.T) {
	t.Parallel()
	answer := assertedAnswer(t)
	bare := writeAnswer(t, answer)
	wrapped := writeAnswer(t, `{"type":"result","result":`+answer+`}`)
	var out bytes.Buffer
	if err := run([]string{bare, wrapped}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.Count(out.String(), "\tresult_cli_0001\tcomplete\t\t1\t2\t1\n"); got != 2 {
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
		"schema version only":  `{"result":{"schema_version":"context_fabric_investigation_result.v1"}}`,
		"incomplete result":    `{"schema_version":"context_fabric_investigation_result.v1","result_id":"not-an-answer","status":"partial"}`,
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
