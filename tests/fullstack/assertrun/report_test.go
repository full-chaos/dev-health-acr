package main

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLayerAddTracksOK(t *testing.T) {
	l := newLayer("L2", "acr_api")
	if !l.add("check-a", true, "x", "x", "") {
		t.Fatal("add should return the check's own ok value")
	}
	if !l.OK {
		t.Fatal("layer should still be ok after a passing check")
	}
	if l.add("check-b", false, "expected", "actual", "mismatch") {
		t.Fatal("add should return false for a failing check")
	}
	if l.OK {
		t.Fatal("layer must flip to not-ok once any check fails")
	}
	if len(l.Checks) != 2 {
		t.Fatalf("expected 2 recorded checks, got %d", len(l.Checks))
	}
}

// TestLayerSkipDoesNotReadAsAPass is the case Codex's finding 13 flagged: a deliberately
// skipped layer (task-005's denial path never runs an OpenCode session, so L3/L4/L5's
// event-stream/evidence/result checks don't apply) must be distinguishable from a genuinely
// verified pass, both in the JSON report and in junit.xml.
func TestLayerSkipDoesNotReadAsAPass(t *testing.T) {
	l := newLayer("L3", "mcp")
	l.skip("opencode_events_not_applicable", "SKIPPED: denied task, no OpenCode session ran")
	if !l.OK {
		t.Fatal("a skip must never fail the layer")
	}
	if len(l.Checks) != 1 {
		t.Fatalf("expected 1 recorded check, got %d", len(l.Checks))
	}
	check := l.Checks[0]
	if !check.Skipped {
		t.Fatal("check.Skipped must be true")
	}
	if !check.OK {
		t.Fatal("a skipped check should still report OK (it did not fail), but Skipped must distinguish it from a verified pass")
	}

	report := buildReport("run-1", "task-005", []*Layer{l})
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "assertion-report.json")
	if err := writeJSONReport(reportPath, report); err != nil {
		t.Fatalf("writeJSONReport: %v", err)
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"skipped": true`) && !strings.Contains(string(data), `"skipped":true`) {
		t.Fatalf("assertion-report.json must mark the check skipped: %s", data)
	}

	junitPath := filepath.Join(dir, "junit.xml")
	if err := writeJUnit(junitPath, report); err != nil {
		t.Fatalf("writeJUnit: %v", err)
	}
	junitData, err := os.ReadFile(junitPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded junitTestSuites
	if err := xml.Unmarshal(junitData, &decoded); err != nil {
		t.Fatalf("junit.xml is not well-formed: %v", err)
	}
	if len(decoded.Suites) != 1 || len(decoded.Suites[0].TestCases) != 1 {
		t.Fatalf("unexpected decoded shape: %#v", decoded)
	}
	tc := decoded.Suites[0].TestCases[0]
	if tc.Skipped == nil {
		t.Fatal("junit.xml testcase must carry a <skipped> element, not render as an ordinary pass")
	}
	if tc.Failure != nil {
		t.Fatal("a skip must never render as a <failure>")
	}
	if decoded.Suites[0].Skipped != 1 {
		t.Fatalf("suite skipped count = %d, want 1", decoded.Suites[0].Skipped)
	}
}

func TestBuildReportAggregatesLayerOK(t *testing.T) {
	passing := newLayer("L1", "infrastructure")
	passing.add("ok-check", true, "", "", "")
	failing := newLayer("L5", "agent_result")
	failing.add("bad-check", false, "a", "b", "mismatch")

	report := buildReport("run-1", "task-001", []*Layer{passing, failing})
	if report.OK {
		t.Fatal("report must be not-ok when any layer failed")
	}
	if len(report.Layers) != 2 {
		t.Fatalf("expected 2 layers, got %d", len(report.Layers))
	}

	onlyPassing := buildReport("run-1", "task-001", []*Layer{passing})
	if !onlyPassing.OK {
		t.Fatal("report should be ok when every layer passed")
	}
}

// TestJUnitEscaping is the case the team lead explicitly asked to cover: check names and
// failure messages containing XML metacharacters (from redacted model output, SQL, or JSON)
// must not corrupt junit.xml. The document must remain well-formed and must decode back to
// the original (redacted) text exactly.
func TestJUnitEscaping(t *testing.T) {
	layer := newLayer("L5", "agent_result")
	layer.add(
		`claim "c1" cites <invented> & unknown evidence`,
		false,
		`ids <= ["a", "b"]`,
		`ids <= ["a" & "c"]`,
		`finding claim_id="c1" cites an ID absent from the packet: <script>alert(1)</script> & 'quoted'`,
	)
	report := buildReport("run-1", "task-005", []*Layer{layer})

	dir := t.TempDir()
	path := filepath.Join(dir, "junit.xml")
	if err := writeJUnit(path, report); err != nil {
		t.Fatalf("writeJUnit: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read junit.xml: %v", err)
	}

	if !strings.HasPrefix(string(data), xml.Header) {
		t.Fatal("junit.xml must start with an XML declaration")
	}
	// A raw, unescaped "<" or "&" in the source text must not appear literally in the
	// output; it must be replaced by its entity form. This is the actual well-formedness
	// property under test, independent of decoding.
	if strings.Contains(string(data), "<invented>") {
		t.Fatalf("raw unescaped angle brackets leaked into junit.xml:\n%s", data)
	}

	var decoded junitTestSuites
	if err := xml.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("junit.xml is not well-formed: %v\n%s", err, data)
	}
	if len(decoded.Suites) != 1 || len(decoded.Suites[0].TestCases) != 1 {
		t.Fatalf("unexpected decoded shape: %#v", decoded)
	}
	tc := decoded.Suites[0].TestCases[0]
	if tc.Name != `claim "c1" cites <invented> & unknown evidence` {
		t.Fatalf("test case name round-tripped incorrectly: %q", tc.Name)
	}
	if tc.Failure == nil {
		t.Fatal("expected a <failure> element for the failing check")
	}
	if !strings.Contains(tc.Failure.Body, "<script>alert(1)</script>") {
		t.Fatalf("failure body did not round-trip the message text: %q", tc.Failure.Body)
	}
	if decoded.Suites[0].Failures != 1 || decoded.Suites[0].Tests != 1 {
		t.Fatalf("unexpected suite counters: tests=%d failures=%d", decoded.Suites[0].Tests, decoded.Suites[0].Failures)
	}
}

func TestJUnitRedactsSecretsInMessages(t *testing.T) {
	layer := newLayer("L1", "infrastructure")
	layer.add("probe", false, "postgres://user:pass@host/db", "1", "connection used svc_acr_leaked123")
	report := buildReport("run-1", "task-001", []*Layer{layer})

	dir := t.TempDir()
	path := filepath.Join(dir, "junit.xml")
	if err := writeJUnit(path, report); err != nil {
		t.Fatalf("writeJUnit: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read junit.xml: %v", err)
	}
	if strings.Contains(string(data), "svc_acr_leaked123") || strings.Contains(string(data), "user:pass") {
		t.Fatalf("junit.xml leaked a secret:\n%s", data)
	}
}

func TestWriteJSONReportRedactsSecrets(t *testing.T) {
	layer := newLayer("L2", "acr_api")
	layer.add("capabilities", false, "", "token fcacr_shouldnotleak", "")
	report := buildReport("run-1", "task-001", []*Layer{layer})

	dir := t.TempDir()
	path := filepath.Join(dir, "assertion-report.json")
	if err := writeJSONReport(path, report); err != nil {
		t.Fatalf("writeJSONReport: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read assertion-report.json: %v", err)
	}
	if strings.Contains(string(data), "fcacr_shouldnotleak") {
		t.Fatalf("assertion-report.json leaked a secret:\n%s", data)
	}
}
