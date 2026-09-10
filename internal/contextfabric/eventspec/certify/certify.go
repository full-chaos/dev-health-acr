// Package certify is CHAOS-5515's reusable JSON assertion runner (clause 5,
// A2): it judges REAL production log output -- bytes a production
// slog.JSONHandler actually wrote -- against an eventspec.Event declaration,
// and refuses to certify anything that is not that.
//
// It does not replace the existing value pins (chaos5434_anchor_slot_test.go's
// logLineWithMsg and its assertions) -- it CONSUMES the same real JSON a
// producer's own test drives through NewSlogResolutionTracer +
// slog.NewJSONHandler, and adds: exact record identity, multiplicity against
// the event's declared Multiplicity, level against the event's declared
// Level, and every field in the caller's `want` against the event's
// declaration (closed-vocabulary membership when the field has one).
package certify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// Line is one decoded slog JSON log line.
type Line map[string]any

// Log is production slog.JSONHandler output: one JSON object per line, each
// object required to carry "time", "level" and "msg" -- the shape ONLY a
// real slog.JSONHandler produces. A recorder/capture struct's Go values, fed
// in as text, do not decode to this shape and Parse refuses them; that
// refusal IS control (a) of A2.
type Log struct {
	lines []Line
}

// Parse decodes production slog JSON output. It refuses (returns an error,
// never a partial Log) anything that is not line-delimited JSON where every
// non-blank line carries "time", "level" and "msg" -- the minimum shape
// log/slog's own JSONHandler guarantees and a capture/recorder struct's
// stringified fields do not.
func Parse(log []byte) (*Log, error) {
	var lines []Line
	for i, raw := range bytes.Split(log, []byte("\n")) {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}
		var l Line
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, fmt.Errorf("certify: line %d is not a JSON object (this runner judges real slog.JSONHandler output, not a recorder/capture struct's Go values): %w", i, err)
		}
		for _, required := range []string{"time", "level", "msg"} {
			if _, ok := l[required]; !ok {
				return nil, fmt.Errorf("certify: line %d has no %q key -- this is not slog.JSONHandler output (a capture struct or hand-built fixture does not carry the handler's own envelope): %s", i, required, string(raw))
			}
		}
		lines = append(lines, l)
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("certify: no JSON log lines found -- an empty or non-JSON log cannot be certified")
	}
	return &Log{lines: lines}, nil
}

// linesWithMsg returns every line whose "msg" equals msg, in log order.
func (l *Log) linesWithMsg(msg string) []Line {
	var out []Line
	for _, line := range l.lines {
		if m, _ := line["msg"].(string); m == msg {
			out = append(out, line)
		}
	}
	return out
}

// LinesWithMsg is linesWithMsg, exported for a caller that needs to read a
// line this package has not (yet) declared as an eventspec.Event -- a
// decision-graph reconstruction that walks pre-entry/pre-decision/
// decision/post-decision lines spanning a pass necessarily reads lines
// beyond whatever subset is certified so far (clause 6 migrates the rest of
// the emission population PR by PR; the graph as a whole is observable at
// every pass from day one, certified line by certified line as each seam
// migrates).
func (l *Log) LinesWithMsg(msg string) []Line { return l.linesWithMsg(msg) }

// Assertion is one certified expectation: an event variant, and the field
// values an independently controlled fixture expects that variant's ONE
// matching line to carry. Fields the event declares but the caller omits
// from Want are not checked -- name every field the fixture pins.
type Assertion struct {
	Event eventspec.Event
	Want  map[string]any
}

// Result is what Certify found, for a caller that wants to inspect the
// matched line beyond what Want already asserted.
type Result struct {
	Line Line
}

// Certify locates the line(s) matching a.Event.Msg, enforces multiplicity,
// asserts production level, and asserts every key in a.Want against the
// decoded line with exact value equality (JSON-normalized: int/float
// distinctions collapse the same way encoding/json already collapses them,
// so a caller writes plain Go literals in Want).
//
// It returns an error rather than calling testing.T directly so a caller
// can assert on the error text in a red-first control proof; production
// call sites wrap the error with t.Fatal/t.Error themselves.
func Certify(log *Log, a Assertion) (Result, error) {
	matches := log.linesWithMsg(a.Event.Msg)

	switch a.Event.Multiplicity {
	case eventspec.MultiplicityExactlyOnePerPass:
		if len(matches) != 1 {
			return Result{}, fmt.Errorf("certify: %s: found %d lines with msg %q, want exactly 1 (multiplicity=%s)",
				a.Event.ID, len(matches), a.Event.Msg, a.Event.Multiplicity)
		}
	case eventspec.MultiplicityZeroOrOnePerPass:
		if len(matches) > 1 {
			return Result{}, fmt.Errorf("certify: %s: found %d lines with msg %q, want at most 1 (multiplicity=%s)",
				a.Event.ID, len(matches), a.Event.Msg, a.Event.Multiplicity)
		}
		if len(matches) == 0 {
			return Result{}, fmt.Errorf("certify: %s: no line with msg %q -- call CertifyAbsent when absence is the expectation being asserted", a.Event.ID, a.Event.Msg)
		}
	default:
		return Result{}, fmt.Errorf("certify: %s: unhandled multiplicity %q", a.Event.ID, a.Event.Multiplicity)
	}

	line := matches[0]

	wantLevel := strings.ToUpper(string(a.Event.Level))
	gotLevel, _ := line["level"].(string)
	if strings.ToUpper(gotLevel) != wantLevel {
		return Result{}, fmt.Errorf("certify: %s: line at level %q, want %q (production visibility: a line declared Info that ships at Debug is a regression invisible in production, not a passing certificate)",
			a.Event.ID, gotLevel, wantLevel)
	}

	declared := make(map[string]eventspec.Field, len(a.Event.Fields))
	for _, f := range a.Event.Fields {
		declared[f.Key] = f
	}

	keys := make([]string, 0, len(a.Want))
	for k := range a.Want {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		want := a.Want[key]
		field, isDeclared := declared[key]
		if !isDeclared {
			return Result{}, fmt.Errorf("certify: %s: %q is asserted in Want but not declared on the event -- the specification is the one declaration authority; add the field to eventspec.spec.go first", a.Event.ID, key)
		}
		got, present := line[key]
		if !present {
			return Result{}, fmt.Errorf("certify: %s: line has no %q key (declared presence=%s) -- a required field must never be omitted, and missing is never equivalent to a measured zero",
				a.Event.ID, key, field.Presence)
		}
		if len(field.ClosedVocabulary) > 0 {
			gotStr, ok := got.(string)
			if !ok || !contains(field.ClosedVocabulary, gotStr) {
				return Result{}, fmt.Errorf("certify: %s: %q = %v is not in the declared closed vocabulary %v", a.Event.ID, key, got, field.ClosedVocabulary)
			}
		}
		if !jsonEqual(want, got) {
			return Result{}, fmt.Errorf("certify: %s: %q = %v, want %v", a.Event.ID, key, got, want)
		}
	}

	return Result{Line: line}, nil
}

// CertifyAbsent asserts the event's variant produced NO line -- the explicit
// zero-multiplicity case for a MultiplicityZeroOrOnePerPass event, so an
// absence a caller expects is asserted as loudly as a presence it expects.
func CertifyAbsent(log *Log, ev eventspec.Event) error {
	matches := log.linesWithMsg(ev.Msg)
	if len(matches) != 0 {
		return fmt.Errorf("certify: %s: found %d lines with msg %q, want 0 (asserted absent)", ev.ID, len(matches), ev.Msg)
	}
	return nil
}

func contains(vocab []string, v string) bool {
	for _, c := range vocab {
		if c == v {
			return true
		}
	}
	return false
}

// jsonEqual compares a caller's plain Go literal (int, string, []string, ...)
// against a value decoded from JSON (float64, string, []any, ...) by
// round-tripping the want side through encoding/json first, so both sides
// use the same normalized representation.
func jsonEqual(want, got any) bool {
	wb, err := json.Marshal(want)
	if err != nil {
		return false
	}
	var wantNorm any
	if err := json.Unmarshal(wb, &wantNorm); err != nil {
		return false
	}
	return reflect.DeepEqual(wantNorm, got)
}
