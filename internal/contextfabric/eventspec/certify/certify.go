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
	"math"
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

// eventHasPassField reports whether fields declares a "pass" key -- the one
// signal this package uses to decide whether an event's multiplicity is
// scoped per-pass (a duplicate/count check keyed on each distinct pass
// value) or per-request (the whole scope IS the one thing being counted,
// with no pass to key on at all).
func eventHasPassField(fields []eventspec.Field) bool {
	for _, f := range fields {
		if f.Key == "pass" {
			return true
		}
	}
	return false
}

// multiplicityRequiresPassField states, for a RECOGNISED Multiplicity,
// whether it requires (true) or forbids (false) a declared "pass" field --
// the executable consistency rule round r2's finding 5 asked for (a
// TestEveryEventsMultiplicityAgreesWithWhetherItDeclaresAPassField walks
// eventspec.All and asserts every event's own eventHasPassField() agrees
// with this). ok is false for an unrecognised Multiplicity value.
func multiplicityRequiresPassField(m eventspec.Multiplicity) (requiresPass bool, ok bool) {
	switch m {
	case eventspec.MultiplicityExactlyOnePerPass, eventspec.MultiplicityZeroOrOnePerPass:
		return true, true
	case eventspec.MultiplicityExactlyOnePerRequest:
		return false, true
	default:
		return false, false
	}
}

// scopeMatch reports whether line belongs to the attempt `scope` identifies
// -- every declared Attribution field, PLUS "pass" whenever `scope` itself
// carries a "pass" key (never unconditionally on eventHasPassField: Certify
// gathers the WHOLE request's lines across every pass first, to run its own
// per-pass duplicate check over the full set, so its own gather step omits
// "pass" from the scope map on purpose; CertifyAbsent asks about ONE
// specific pass, so its scope map always carries one). Certify and
// CertifyAbsent both call this SAME function (r3 fix, round r3 finding 1:
// CertifyAbsent used to scope by Attribution alone, so a real line for pass
// 1 wrongly blocked asserting absence for pass 2 of the SAME request -- the
// two functions had drifted on what "this attempt" means; sharing one
// function makes that drift impossible going forward).
func scopeMatch(ev eventspec.Event, scope map[string]any, line Line) bool {
	for _, attrKey := range ev.Attribution {
		if !jsonEqual(scope[attrKey], line[attrKey]) {
			return false
		}
	}
	if p, ok := scope["pass"]; ok {
		if !jsonEqual(p, line["pass"]) {
			return false
		}
	}
	return true
}

// groupLinesByPass partitions scoped lines by their own declared "pass"
// field value -- every real cell reaching this function has already had
// "pass" presence and type validated by validateFields (called on EVERY
// scoped line before this runs, in Certify below), so the !ok branch is a
// pure defensive fallback: a missing/malformed pass value is bucketed under
// its own always-unique negative key rather than silently grouped with a
// real pass number, so it can never mask (or be masked by) a genuine
// duplicate-pass defect.
func groupLinesByPass(scoped []Line) map[float64][]Line {
	groups := make(map[float64][]Line, len(scoped))
	sentinel := -1.0
	for _, l := range scoped {
		p, ok := l["pass"].(float64)
		if !ok {
			groups[sentinel] = []Line{l}
			sentinel--
			continue
		}
		groups[p] = append(groups[p], l)
	}
	return groups
}

// Certify locates the line(s) matching a.Event.Msg AND matching every one of
// the event's declared Attribution fields against a.Want (round r1's P2:
// production genuinely emits more than one line with the same msg for one
// REQUEST when a resolution re-decides across passes --
// TestRankedCutSummary_LastSummaryDescribesTheKeptPassAcrossReDecisionPasses
// is the pre-existing, already-shipped proof -- so multiplicity is scoped to
// the attempt Want identifies via Attribution, never to the whole supplied
// log), enforces multiplicity within that scope, asserts production level,
// asserts PRESENCE/TYPE/VOCABULARY of every declared field on EVERY line in
// scope (round r3's P1: an EARLIER malformed line used to be skipped
// entirely -- only the last-selected line was ever field-validated), and
// asserts exact value equality (JSON-normalized) for every key in a.Want.
//
// Multiplicity is now uniformly PASS-KEYED for any event that declares a
// "pass" field (MultiplicityExactlyOnePerPass, MultiplicityZeroOrOnePerPass
// -- round r2's finding 1: the zero-or-one branch used to refuse ANY second
// line in scope, never examining pass, so two legitimate DISTINCT-pass
// zero-or-one lines were wrongly refused as a duplicate) and PASS-LESS for
// MultiplicityExactlyOnePerRequest (decision_summary: the whole scope IS
// the one thing being counted). For a pass-keyed multiplicity, a duplicate
// PASS NUMBER within scope is always a defect regardless of whether the
// lines' other fields agree or differ; a DISTINCT pass number is always
// legitimate, regardless of whether the lines happen to coincide on every
// other field (replaces round r2's byte-identical-except-time heuristic).
// The line CERTIFIED (checked against a.Want's value assertions) is always
// the LAST one in scope, matching production's own documented rule
// (tracer.go's RankedCutSummary doc comment: "the LAST summary reaching the
// tracer for a request_id always describes the pass whose resolution was
// actually returned").
//
// Round r3's finding 5 (Want.pass required): for a pass-keyed event, a.Want
// must include "pass" -- the caller must say WHICH pass it is certifying,
// the same requirement Attribution fields already carry, because "the last
// line" is a selection rule for FINDING the line, not a substitute for the
// caller stating which attempt it means to assert values against.
//
// It returns an error rather than calling testing.T directly so a caller
// can assert on the error text in a red-first control proof; production
// call sites wrap the error with t.Fatal/t.Error themselves.
func Certify(log *Log, a Assertion) (Result, error) {
	if log == nil {
		return Result{}, fmt.Errorf("certify: %s: log is nil -- Certify judges a real Parse()d log, never a nil placeholder", a.Event.ID)
	}
	if err := requireCanonicalEvent(a.Event); err != nil {
		return Result{}, err
	}

	for _, attrKey := range a.Event.Attribution {
		if _, ok := a.Want[attrKey]; !ok {
			return Result{}, fmt.Errorf("certify: %s: Want must include %q (one of this event's declared Attribution fields) to scope which attempt is being certified -- multiplicity is asserted per attempt, never over the whole supplied log", a.Event.ID, attrKey)
		}
	}

	requiresPass, recognisedMultiplicity := multiplicityRequiresPassField(a.Event.Multiplicity)
	if !recognisedMultiplicity {
		return Result{}, fmt.Errorf("certify: %s: unhandled multiplicity %q", a.Event.ID, a.Event.Multiplicity)
	}
	hasPassField := eventHasPassField(a.Event.Fields)
	if requiresPass != hasPassField {
		// Defensive: the SAME consistency rule
		// TestEveryEventsMultiplicityAgreesWithWhetherItDeclaresAPassField
		// asserts statically over eventspec.All. Reaching here means a
		// caller passed a canonical event whose own declaration is
		// internally inconsistent -- refuse rather than guess which side is
		// wrong.
		return Result{}, fmt.Errorf("certify: %s: declared multiplicity=%q requires pass-field=%v but the event's own Fields declare pass-field=%v -- the declaration itself is inconsistent",
			a.Event.ID, a.Event.Multiplicity, requiresPass, hasPassField)
	}
	if hasPassField {
		if _, ok := a.Want["pass"]; !ok {
			return Result{}, fmt.Errorf("certify: %s: Want must include \"pass\" for a pass-keyed event -- the caller must state WHICH pass it is certifying, not rely on \"the last line\" alone", a.Event.ID)
		}
	}

	// Attribution-only scope for the GATHER step -- deliberately omits
	// "pass" even though a.Want carries one (required above), because this
	// step must collect every pass in the REQUEST so the per-pass
	// duplicate/distinct check below can see the whole set. scopeMatch only
	// checks "pass" when the scope map passed to it carries the key.
	requestScope := make(map[string]any, len(a.Event.Attribution))
	for _, attrKey := range a.Event.Attribution {
		requestScope[attrKey] = a.Want[attrKey]
	}
	var scoped []Line
	for _, line := range log.linesWithMsg(a.Event.Msg) {
		if scopeMatch(a.Event, requestScope, line) {
			scoped = append(scoped, line)
		}
	}

	// EVERY line in scope is field-validated, not only the one ultimately
	// selected (round r3's P1) -- an earlier, malformed line is a defect
	// regardless of whether a later line in the same scope looks fine.
	for i, l := range scoped {
		if err := validateFields(a.Event.Fields, l, a.Event.ID); err != nil {
			return Result{}, fmt.Errorf("%w (line %d of %d in scope)", err, i+1, len(scoped))
		}
	}

	var line Line
	if !hasPassField {
		// MultiplicityExactlyOnePerRequest: the whole scope is the one
		// thing being counted -- no pass to key on, so more than one line
		// is unconditionally a defect, same as before this event's own
		// mislabeling was fixed.
		if len(scoped) == 0 {
			return Result{}, fmt.Errorf("certify: %s: found 0 lines with msg %q for this attempt, want exactly 1 (multiplicity=%s)",
				a.Event.ID, a.Event.Msg, a.Event.Multiplicity)
		}
		if len(scoped) > 1 {
			return Result{}, fmt.Errorf("certify: %s: found %d lines with msg %q for this attempt, want exactly 1 (multiplicity=%s, no pass field declared -- this event has no legitimate multi-line shape to distinguish from a duplicate)",
				a.Event.ID, len(scoped), a.Event.Msg, a.Event.Multiplicity)
		}
		line = scoped[0]
	} else {
		switch a.Event.Multiplicity {
		case eventspec.MultiplicityExactlyOnePerPass:
			if len(scoped) == 0 {
				return Result{}, fmt.Errorf("certify: %s: found 0 lines with msg %q for this attempt, want at least 1 (multiplicity=%s)",
					a.Event.ID, a.Event.Msg, a.Event.Multiplicity)
			}
		case eventspec.MultiplicityZeroOrOnePerPass:
			if len(scoped) == 0 {
				return Result{}, fmt.Errorf("certify: %s: no line with msg %q for this attempt -- call CertifyAbsent when absence is the expectation being asserted", a.Event.ID, a.Event.Msg)
			}
		}
		// PASS-KEYED for EITHER multiplicity that declares a pass field
		// (round r2 finding 1: zero_or_one_per_pass used to refuse any
		// second line without ever examining pass -- two DISTINCT passes
		// each legitimately carrying zero-or-one line were wrongly refused
		// as a duplicate). A duplicate PASS NUMBER within scope is always a
		// defect for either multiplicity; a distinct one is always fine.
		for p, group := range groupLinesByPass(scoped) {
			if len(group) > 1 {
				return Result{}, fmt.Errorf("certify: %s: %d lines with msg %q share pass=%v for this attempt -- a duplicate pass number is always a defect, regardless of whether the lines' other fields agree",
					a.Event.ID, len(group), a.Event.Msg, p)
			}
		}
		line = scoped[len(scoped)-1]
	}

	wantLevel := strings.ToUpper(string(a.Event.Level))
	gotLevel, _ := line["level"].(string)
	if strings.ToUpper(gotLevel) != wantLevel {
		return Result{}, fmt.Errorf("certify: %s: line at level %q, want %q (production visibility: a line declared Info that ships at Debug is a regression invisible in production, not a passing certificate)",
			a.Event.ID, gotLevel, wantLevel)
	}

	// Every declared field's PRESENCE/TYPE/VOCABULARY was already asserted,
	// unconditionally and for every line in scope, above -- a second call
	// here would be dead code, never able to fire on a value the loop above
	// would not already have refused.

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
		// Type and closed-vocabulary membership for this field were already
		// asserted, unconditionally, by validateFields above (every key
		// reaching this loop is a declared field, by the isDeclared check
		// above -- validateFields already ran over that same declared set).
		// A second check here would be dead code: it can never fire on a
		// value validateFields would not already have refused.
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
// r3 fix (round r3 finding 1): MultiplicityZeroOrOnePerPass always
// declares a pass field (multiplicityRequiresPassField), so `attribution`
// must ALSO include "pass" -- absence is asserted for ONE SPECIFIC pass of
// the request, never "the whole request has no line at all". Before this
// fix, a genuine line for pass 1 wrongly blocked asserting absence for pass
// 2 of the SAME request_id -- exactly the request_id-scoping bug round r2
// fixed for a DIFFERENT request, now fixed for a different pass of the SAME
// request, using the SAME scopeMatch function Certify's own pass-keyed
// duplicate check uses, so the two can never drift apart again.
//
// It refuses for any Multiplicity other than MultiplicityZeroOrOnePerPass
// -- an ExactlyOnePerPass/ExactlyOnePerRequest event is required on every
// pass/request by its own declaration, so "absent" can never be a
// legitimate expectation for one (round r1's P2); an unrecognised
// Multiplicity value refuses for the same reason Certify's own switch does
// (round r2's P2: "unknown multiplicities are also accepted" was a real gap
// -- this closes it by requiring the ONE multiplicity that legitimately
// allows absence, explicitly, rather than excluding only the ones that
// don't).
func CertifyAbsent(log *Log, ev eventspec.Event, attribution map[string]any) error {
	if log == nil {
		return fmt.Errorf("certify: %s: log is nil -- CertifyAbsent judges a real Parse()d log, never a nil placeholder", ev.ID)
	}
	if err := requireCanonicalEvent(ev); err != nil {
		return err
	}
	if ev.Multiplicity != eventspec.MultiplicityZeroOrOnePerPass {
		return fmt.Errorf("certify: %s: declared multiplicity=%q is not zero_or_one_per_pass -- CertifyAbsent only applies to a zero_or_one_per_pass event", ev.ID, ev.Multiplicity)
	}
	for _, attrKey := range ev.Attribution {
		if _, ok := attribution[attrKey]; !ok {
			return fmt.Errorf("certify: %s: attribution must include %q (one of this event's declared Attribution fields) to scope which attempt's absence is being asserted", ev.ID, attrKey)
		}
	}
	if _, ok := attribution["pass"]; !ok {
		return fmt.Errorf("certify: %s: attribution must include \"pass\" -- a zero_or_one_per_pass event's absence is asserted for ONE specific pass, never the whole request", ev.ID)
	}
	for _, line := range log.linesWithMsg(ev.Msg) {
		if scopeMatch(ev, attribution, line) {
			return fmt.Errorf("certify: %s: found a line with msg %q for this attempt (pass=%v), want 0 (asserted absent)", ev.ID, ev.Msg, attribution["pass"])
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
			gotFloat, ok := got.(float64)
			if !ok {
				return fmt.Errorf("certify: %s: %q = %v (%T), declared type=int", eventID, field.Key, got, got)
			}
			// round r3's P1: JSON has no separate integer type, so a real
			// slog.JSONHandler line carrying a genuinely fractional number
			// (e.g. a producer bug computing candidate_count as a ratio)
			// decodes to the SAME float64 shape as a legitimate integer --
			// only checking the Go type let candidate_count=92.5 certify as
			// declared type=int. Refuse anything that is not a whole number.
			if gotFloat != math.Trunc(gotFloat) {
				return fmt.Errorf("certify: %s: %q = %v, declared type=int but is not a whole number", eventID, field.Key, got)
			}
		case eventspec.FieldBool:
			if _, ok := got.(bool); !ok {
				return fmt.Errorf("certify: %s: %q = %v (%T), declared type=bool", eventID, field.Key, got, got)
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

// requireCanonicalEvent refuses unless ev is byte-for-byte the eventspec.All
// entry for its own ID -- a caller must pass eventspec.RankedCutSummary (or
// eventspec.AnchorSlotDisplaced) directly, never a copy, subset, or
// hand-built value with the same ID. Every legitimate call site already
// does this (the two production Assertion/CertifyAbsent call sites in
// graphrank/falkorgraph reference the exported eventspec vars directly), so
// this refuses nothing real -- only a weakened or invented Event value.
//
// Looks up eventspec.ByID (generated from spec.go by eventspec/gen, part of
// zz_generated.go) rather than a second, hand-built index -- CHAOS-5516
// cleanup: PR1's own r3 fix built its own package-level map here instead of
// using the generated one that already existed, exactly the "second,
// competing list" clause 1 forbids.
func requireCanonicalEvent(ev eventspec.Event) error {
	canon, ok := eventspec.ByID[ev.ID]
	if !ok {
		return fmt.Errorf("certify: %q is not a declared event ID -- eventspec.All is the one declaration authority; pass eventspec.RankedCutSummary/eventspec.AnchorSlotDisplaced (or a future registered event) directly, never a hand-built Event", ev.ID)
	}
	if !reflect.DeepEqual(canon, ev) {
		return fmt.Errorf("certify: %s: the supplied Event does not match its canonical declaration in eventspec.All -- pass the exported eventspec value directly (e.g. eventspec.RankedCutSummary), never a caller-modified or hand-built copy with the same ID", ev.ID)
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
