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
// VALUES an independently controlled fixture expects that variant's
// matching line to carry. Want MUST include every field named in
// Event.Attribution (used to scope which attempt is being certified).
// Every other PresenceRequired field is checked for PRESENCE regardless of
// whether it appears in Want; a field appearing in Want is additionally
// checked for exact VALUE equality -- name a field in Want whenever the
// fixture pins a specific value, not just to make it exist.
type Assertion struct {
	Event eventspec.Event
	Want  map[string]any
}

// Result is what Certify found, for a caller that wants to inspect the
// matched line beyond what Want already asserted.
type Result struct {
	Line Line
}

// Certify locates the line(s) matching a.Event.Msg AND matching every one of
// the event's declared Attribution fields against a.Want (round r1's P2:
// production genuinely emits more than one line with the same msg for one
// REQUEST when a resolution re-decides across passes --
// TestRankedCutSummary_LastSummaryDescribesTheKeptPassAcrossReDecisionPasses
// is the pre-existing, already-shipped proof -- so multiplicity is scoped to
// the attempt Want identifies via Attribution, never to the whole supplied
// log), enforces multiplicity within that scope, asserts production level,
// asserts PRESENCE of every field the event declares PresenceRequired
// (round r1's P1: a field's value is only checked when the caller names it
// in Want, but a field silently DROPPED from production output must never
// pass unnoticed just because no test happened to pin its value), and
// asserts exact value equality (JSON-normalized) for every key in a.Want.
//
// For MultiplicityExactlyOnePerPass, more than one line in scope is not an
// error: the LAST one is certified, matching production's own documented
// rule (tracer.go's RankedCutSummary doc comment: "the LAST summary
// reaching the tracer for a request_id always describes the pass whose
// resolution was actually returned"). This does trade away detecting a
// genuine duplicate-emission defect within one pass via count alone; there
// is no pass-sequence field in the current spec to distinguish "two
// legitimate passes" from "one pass, emitted twice" -- callers that need
// that distinction must pin it via a producer-specific field in Want.
//
// It returns an error rather than calling testing.T directly so a caller
// can assert on the error text in a red-first control proof; production
// call sites wrap the error with t.Fatal/t.Error themselves.
func Certify(log *Log, a Assertion) (Result, error) {
	for _, attrKey := range a.Event.Attribution {
		if _, ok := a.Want[attrKey]; !ok {
			return Result{}, fmt.Errorf("certify: %s: Want must include %q (one of this event's declared Attribution fields) to scope which attempt is being certified -- multiplicity is asserted per attempt, never over the whole supplied log", a.Event.ID, attrKey)
		}
	}

	var scoped []Line
	for _, line := range log.linesWithMsg(a.Event.Msg) {
		match := true
		for _, attrKey := range a.Event.Attribution {
			if !jsonEqual(a.Want[attrKey], line[attrKey]) {
				match = false
				break
			}
		}
		if match {
			scoped = append(scoped, line)
		}
	}

	var line Line
	switch a.Event.Multiplicity {
	case eventspec.MultiplicityExactlyOnePerPass:
		if len(scoped) == 0 {
			return Result{}, fmt.Errorf("certify: %s: found 0 lines with msg %q for this attempt, want at least 1 (multiplicity=%s)",
				a.Event.ID, a.Event.Msg, a.Event.Multiplicity)
		}
		// round r2's P2: more than one line in scope is legitimate
		// multi-pass output ONLY when the passes actually differ. Two
		// lines identical apart from "time" are indistinguishable from
		// one pass emitted twice (a genuine duplicate-emission defect,
		// distinct from a real re-decision) -- refuse rather than
		// silently certifying the last of two identical copies.
		if len(scoped) > 1 && linesEqualExceptTime(scoped[len(scoped)-1], scoped[len(scoped)-2]) {
			return Result{}, fmt.Errorf("certify: %s: the last two lines with msg %q for this attempt are IDENTICAL apart from time -- indistinguishable from a single pass emitted twice; a real re-decision pass must differ in at least one other field",
				a.Event.ID, a.Event.Msg)
		}
		line = scoped[len(scoped)-1]
	case eventspec.MultiplicityZeroOrOnePerPass:
		if len(scoped) > 1 {
			return Result{}, fmt.Errorf("certify: %s: found %d lines with msg %q for this attempt, want at most 1 (multiplicity=%s)",
				a.Event.ID, len(scoped), a.Event.Msg, a.Event.Multiplicity)
		}
		if len(scoped) == 0 {
			return Result{}, fmt.Errorf("certify: %s: no line with msg %q for this attempt -- call CertifyAbsent when absence is the expectation being asserted", a.Event.ID, a.Event.Msg)
		}
		line = scoped[0]
	default:
		return Result{}, fmt.Errorf("certify: %s: unhandled multiplicity %q", a.Event.ID, a.Event.Multiplicity)
	}

	wantLevel := strings.ToUpper(string(a.Event.Level))
	gotLevel, _ := line["level"].(string)
	if strings.ToUpper(gotLevel) != wantLevel {
		return Result{}, fmt.Errorf("certify: %s: line at level %q, want %q (production visibility: a line declared Info that ships at Debug is a regression invisible in production, not a passing certificate)",
			a.Event.ID, gotLevel, wantLevel)
	}

	// PRESENCE, TYPE and CLOSED VOCABULARY of every declared field are
	// asserted, recursively into every nested (object_slice) field,
	// regardless of whether the caller named it in Want -- round r1's P1
	// (a field silently dropped from production output must never pass
	// because no fixture happened to pin its value) and round r2's P1s
	// (a nested field's own required children were never checked at all;
	// Field.Type was declared but never consulted for anything outside
	// Want). See validateFields' own doc comment for what each check
	// covers.
	if err := validateFields(a.Event.Fields, line, a.Event.ID); err != nil {
		return Result{}, err
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

// CertifyAbsent asserts the event's variant produced NO line FOR THE
// ATTEMPT `attribution` IDENTIFIES -- the explicit zero-multiplicity case
// for a MultiplicityZeroOrOnePerPass event, so an absence a caller expects
// is asserted as loudly as a presence it expects. `attribution` must carry
// a value for every one of the event's declared Attribution fields (same
// contract as Assertion.Want), so a log holding a line for a DIFFERENT
// attempt never causes a false refusal here (round r2's P2: a line for
// request_id=req_2 must never block asserting absence for req_1).
//
// It refuses for any Multiplicity other than MultiplicityZeroOrOnePerPass
// -- an ExactlyOnePerPass event is required on every pass by its own
// declaration, so "absent" can never be a legitimate expectation for one
// (round r1's P2); an unrecognised Multiplicity value refuses for the same
// reason Certify's own switch does (round r2's P2: "unknown
// multiplicities are also accepted" was a real gap -- this closes it by
// requiring the ONE multiplicity that legitimately allows absence,
// explicitly, rather than excluding only the one that doesn't).
func CertifyAbsent(log *Log, ev eventspec.Event, attribution map[string]any) error {
	if ev.Multiplicity != eventspec.MultiplicityZeroOrOnePerPass {
		return fmt.Errorf("certify: %s: declared multiplicity=%q is not zero_or_one_per_pass -- CertifyAbsent only applies to a zero_or_one_per_pass event", ev.ID, ev.Multiplicity)
	}
	for _, attrKey := range ev.Attribution {
		if _, ok := attribution[attrKey]; !ok {
			return fmt.Errorf("certify: %s: attribution must include %q (one of this event's declared Attribution fields) to scope which attempt's absence is being asserted", ev.ID, attrKey)
		}
	}
	for _, line := range log.linesWithMsg(ev.Msg) {
		match := true
		for _, attrKey := range ev.Attribution {
			if !jsonEqual(attribution[attrKey], line[attrKey]) {
				match = false
				break
			}
		}
		if match {
			return fmt.Errorf("certify: %s: found a line with msg %q for this attempt, want 0 (asserted absent)", ev.ID, ev.Msg)
		}
	}
	return nil
}

// validateFields asserts PRESENCE (for PresenceRequired fields), JSON TYPE,
// and CLOSED VOCABULARY membership for every field in `fields` against
// `obj`, recursing into each element of an object_slice field's own nested
// Fields. It is unconditional -- it runs over every declared field
// regardless of whether a caller ever names it in a Want/attribution map
// (round r2's P1s: nested fields were never checked at all, and
// Field.Type/ClosedVocabulary were consulted only for fields a caller
// happened to list in Want).
func validateFields(fields []eventspec.Field, obj map[string]any, eventID string) error {
	for _, field := range fields {
		got, present := obj[field.Key]
		if !present {
			if field.Presence == eventspec.PresenceRequired {
				return fmt.Errorf("certify: %s: line has no %q key (declared presence=%s) -- a required field must never be omitted, and missing is never equivalent to a measured zero",
					eventID, field.Key, field.Presence)
			}
			continue
		}
		switch field.Type {
		case eventspec.FieldString:
			gotStr, ok := got.(string)
			if !ok {
				return fmt.Errorf("certify: %s: %q = %v (%T), declared type=string", eventID, field.Key, got, got)
			}
			if len(field.ClosedVocabulary) > 0 && !contains(field.ClosedVocabulary, gotStr) {
				return fmt.Errorf("certify: %s: %q = %v is not in the declared closed vocabulary %v", eventID, field.Key, got, field.ClosedVocabulary)
			}
		case eventspec.FieldInt:
			if _, ok := got.(float64); !ok {
				return fmt.Errorf("certify: %s: %q = %v (%T), declared type=int", eventID, field.Key, got, got)
			}
		case eventspec.FieldStringSlice:
			arr, ok := got.([]any)
			if !ok {
				return fmt.Errorf("certify: %s: %q = %v (%T), declared type=string_slice", eventID, field.Key, got, got)
			}
			for i, elem := range arr {
				if _, ok := elem.(string); !ok {
					return fmt.Errorf("certify: %s: %q[%d] = %v (%T), declared element type=string", eventID, field.Key, i, elem, elem)
				}
			}
		case eventspec.FieldObjectSlice:
			arr, ok := got.([]any)
			if !ok {
				return fmt.Errorf("certify: %s: %q = %v (%T), declared type=object_slice", eventID, field.Key, got, got)
			}
			for i, elem := range arr {
				row, ok := elem.(map[string]any)
				if !ok {
					return fmt.Errorf("certify: %s: %q[%d] = %v (%T), declared element type=object", eventID, field.Key, i, elem, elem)
				}
				if err := validateFields(field.Fields, row, eventID); err != nil {
					return fmt.Errorf("%w (inside %q[%d])", err, field.Key, i)
				}
			}
		}
	}
	return nil
}

// linesEqualExceptTime reports whether a and b carry identical keys/values
// once "time" (which always differs, even for two genuinely identical
// emissions) is excluded from the comparison.
func linesEqualExceptTime(a, b Line) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if k == "time" {
			continue
		}
		bv, ok := b[k]
		if !ok || !reflect.DeepEqual(v, bv) {
			return false
		}
	}
	return true
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
