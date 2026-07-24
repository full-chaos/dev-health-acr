package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"strings"
)

// Check is one named assertion within a layer. Expected/Actual are always normalized,
// structured-field string renderings (e.g. a sorted comma-joined ID set, a single enum
// value) -- never a raw diff of full model output. Skipped marks a check that was
// deliberately not evaluated (e.g. an entire layer does not apply to this task) -- it never
// counts as a failure, but it must never render identically to a genuine pass either: a
// skipped layer must not read like a passed one.
type Check struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Skipped  bool   `json:"skipped,omitempty"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Message  string `json:"message,omitempty"`
}

// Layer is one assertion layer (L1 infrastructure .. L6 web). Name is the short human label
// used in failure output and the JUnit suite name; Layer is the "L1".."L6" tag docs/
// fullstack-acceptance.md section 7 numbers.
type Layer struct {
	Layer  string  `json:"layer"`
	Name   string  `json:"name"`
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// AssertionReport is written to assertion-report.json (docs/fullstack-acceptance.md section
// 9) and mirrored into junit.xml (one <testsuite> per layer, one <testcase> per check).
type AssertionReport struct {
	SchemaVersion string  `json:"schema_version"`
	RunID         string  `json:"run_id"`
	TaskID        string  `json:"task_id"`
	Layers        []Layer `json:"layers"`
	OK            bool    `json:"ok"`
}

const assertionReportSchema = "fullstack_assertion_report.v1"

// newLayer starts a Layer accumulator. Call add for each check, then append the result to
// the report; okOf(layer) derives Layer.OK from its checks, so callers never need to track it
// by hand.
func newLayer(tag, name string) *Layer {
	return &Layer{Layer: tag, Name: name, OK: true, Checks: []Check{}}
}

// add appends a check to the layer and folds its result into the layer's OK flag. It returns
// the check's own ok value so callers can short-circuit dependent checks.
func (l *Layer) add(name string, ok bool, expected, actual, message string) bool {
	l.Checks = append(l.Checks, Check{Name: name, OK: ok, Expected: redact(expected), Actual: redact(actual), Message: redact(message)})
	if !ok {
		l.OK = false
	}
	return ok
}

// addf is add with a printf-style message.
func (l *Layer) addf(name string, ok bool, expected, actual, format string, args ...any) bool {
	return l.add(name, ok, expected, actual, fmt.Sprintf(format, args...))
}

// skip records a check as deliberately not evaluated -- e.g. an entire layer does not apply
// to this task (task-004/005's denial path never runs an OpenCode session at all). It never
// fails the layer, but renders distinctly from a genuine pass in both assertion-report.json
// (skipped:true) and junit.xml (a <skipped> element, not silence) so a reviewer or CI
// dashboard cannot mistake "not applicable" for "verified".
func (l *Layer) skip(name, message string) {
	l.Checks = append(l.Checks, Check{Name: name, OK: true, Skipped: true, Message: redact(message)})
}

func buildReport(runID, taskID string, layers []*Layer) AssertionReport {
	report := AssertionReport{SchemaVersion: assertionReportSchema, RunID: runID, TaskID: taskID, OK: true}
	for _, l := range layers {
		report.Layers = append(report.Layers, *l)
		if !l.OK {
			report.OK = false
		}
	}
	return report
}

func writeJSONReport(path string, report AssertionReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode assertion report: %w", err)
	}
	data = redactBytes(data)
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write assertion report %s: %w", path, err)
	}
	return nil
}

// --- JUnit XML ---

type junitTestSuites struct {
	XMLName xml.Name         `xml:"testsuites"`
	Suites  []junitTestSuite `xml:"testsuite"`
}

type junitTestSuite struct {
	Name      string          `xml:"name,attr"`
	Tests     int             `xml:"tests,attr"`
	Failures  int             `xml:"failures,attr"`
	Skipped   int             `xml:"skipped,attr"`
	TestCases []junitTestCase `xml:"testcase"`
}

type junitTestCase struct {
	Name    string        `xml:"name,attr"`
	Failure *junitFailure `xml:"failure,omitempty"`
	Skipped *junitSkipped `xml:"skipped,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

type junitSkipped struct {
	Message string `xml:"message,attr"`
}

func buildJUnit(report AssertionReport) junitTestSuites {
	suites := junitTestSuites{}
	for _, layer := range report.Layers {
		suite := junitTestSuite{Name: fmt.Sprintf("%s.%s", layer.Layer, layer.Name)}
		for _, check := range layer.Checks {
			tc := junitTestCase{Name: check.Name}
			suite.Tests++
			switch {
			case check.Skipped:
				suite.Skipped++
				tc.Skipped = &junitSkipped{Message: check.Message}
			case !check.OK:
				suite.Failures++
				tc.Failure = &junitFailure{
					Message: summarizeFailure(check),
					Body:    diffBody(check),
				}
			}
			suite.TestCases = append(suite.TestCases, tc)
		}
		suites.Suites = append(suites.Suites, suite)
	}
	return suites
}

func summarizeFailure(check Check) string {
	if check.Message != "" {
		return check.Message
	}
	return fmt.Sprintf("expected %s, got %s", check.Expected, check.Actual)
}

func diffBody(check Check) string {
	var b strings.Builder
	if check.Message != "" {
		b.WriteString(check.Message)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "expected: %s\nactual:   %s\n", check.Expected, check.Actual)
	return b.String()
}

func writeJUnit(path string, report AssertionReport) error {
	suites := buildJUnit(report)
	// Redact before marshaling too: xml.Marshal escapes text content but not attribute
	// values against injection of secrets, and redaction must apply regardless.
	body, err := xml.MarshalIndent(suites, "", "  ")
	if err != nil {
		return fmt.Errorf("encode junit report: %w", err)
	}
	out := []byte(xml.Header)
	out = append(out, body...)
	out = append(out, '\n')
	out = redactBytes(out)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write junit report %s: %w", path, err)
	}
	return nil
}
