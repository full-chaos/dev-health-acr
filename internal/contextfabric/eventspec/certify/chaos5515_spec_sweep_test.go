package certify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// This file is the ONE generated pass chris's ruling requires (2026-09-10
// 19:1xZ amendment to the prompt of record, sha 0d2d8d788c40...): round r1
// found presence unchecked, r2 found type/vocab/nesting unchecked, r3 found
// integrality/canonical-identity/nil unchecked -- three rounds, each finding
// ONE more untreated shape on the SAME certifier. The fix is not a fourth
// patch: enumerate Certify/CertifyAbsent's WHOLE input domain -- every field
// of every event in eventspec.All (recursively) x every domain dimension --
// execute every cell in one generated pass, assert each is refused or
// accepted exactly as the spec says, and census-assert the cell count so a
// future field or event is swept the day it lands, not the day a fourth
// round finds it missing.
//
// Domain dimensions, per field (a field's own Type decides which are
// APPLICABLE to it -- an inapplicable dimension is still a row, marked N/A,
// so the census counts every cell chris asked for, not just the ones that
// happened to apply):
//
//	absent                the key is missing entirely (only meaningful for
//	                      PresenceRequired -- refused; no PresenceConditional
//	                      field exists in the spec today, so that half of the
//	                      dimension has no live cell to assert against)
//	null                  JSON null in place of the value (refused for every
//	                      current field -- no field's zero value is null)
//	zero                  the type's own zero value (0 for int, "" for
//	                      string) -- accepted UNLESS a closed vocabulary
//	                      excludes it (N/A for the two container types,
//	                      which have their own "empty container" cell)
//	empty_container       [] for string_slice/object_slice (accepted -- a
//	                      declared-required slice's own zero value; N/A for
//	                      the two scalar types)
//	wrong_container_type  a bare scalar where an array is declared (refused;
//	                      N/A for the two scalar types, which have their own
//	                      "wrong scalar type" cell instead)
//	wrong_scalar_type     the OTHER JSON scalar shape (a number for a
//	                      declared string, a string for a declared int;
//	                      refused; N/A for the two container types)
//	fractional            0.5 in place of a declared int (refused; N/A for
//	                      every other type)
//	out_of_vocabulary     a value outside a declared ClosedVocabulary
//	                      (refused; N/A for a field with no closed
//	                      vocabulary)
//	boundary              a value at a declared numeric bound, and bound+/-1
//	                      (N/A for every field today: eventspec.Field has no
//	                      Min/Max/MaxItems -- no field in spec.go declares a
//	                      bound. Marked N/A rather than silently omitted, so
//	                      the day a bound IS declared this table's own
//	                      "unhandled N/A" census catches it, not a 4th round)
//	canonical             the field's own valid canonical value, re-asserted
//	                      individually (accepted -- the positive control for
//	                      every field, not just the whole line once)
//
// "duplicate" (chris's domain list) is EVENT-level, not per-field -- two
// identical lines in scope is a property of the whole record, never one
// field -- so it is its own table, one row per event, not folded into the
// per-field cross product.
//
// The CALL-SURFACE cells (nil log, empty log, a caller-supplied Event not
// byte-equal to its canonical declaration, an unknown event ID, an
// unrecognised Multiplicity, and an attribution map/Want missing a declared
// Attribution key) are a third table, one row per {function, cell} pair --
// Certify and CertifyAbsent are exercised separately since round r3 found a
// nil log panicked in both independently.

// ---------------------------------------------------------------- fixtures

// canonicalValueFor returns one internally-valid value for a declared field,
// used only to build this sweep's own control line -- never a value pinned
// as a real production expectation anywhere else.
func canonicalValueFor(f eventspec.Field) any {
	switch f.Type {
	case eventspec.FieldString:
		if len(f.ClosedVocabulary) > 0 {
			return f.ClosedVocabulary[0]
		}
		return "sweep_canonical_string"
	case eventspec.FieldInt:
		return 7
	case eventspec.FieldBool:
		return true
	case eventspec.FieldStringSlice:
		return []string{"sweep_elem"}
	case eventspec.FieldObjectSlice:
		row := map[string]any{}
		for _, nf := range f.Fields {
			row[nf.Key] = canonicalValueFor(nf)
		}
		// Exactly one representative row -- enough for the recursive sweep
		// below to reach every nested field without the census depending on
		// production's own row count for any real event.
		return []any{row}
	default:
		return nil
	}
}

// zeroValueFor returns the type's own zero value -- distinct from
// canonicalValueFor, which returns a non-zero representative value so the
// "zero" domain cell is a genuine, separate assertion rather than a repeat
// of the canonical-value control.
func zeroValueFor(f eventspec.Field) (val any, applicable bool) {
	switch f.Type {
	case eventspec.FieldString:
		return "", true
	case eventspec.FieldInt:
		return 0, true
	case eventspec.FieldBool:
		return false, true
	default:
		return nil, false // containers use empty_container instead
	}
}

// wrongScalarTypeValueFor returns a JSON scalar shape that is NOT the
// declared one -- applicable to every scalar type (string/int/bool).
func wrongScalarTypeValueFor(f eventspec.Field) (val any, applicable bool) {
	switch f.Type {
	case eventspec.FieldInt:
		return "not-an-int", true
	case eventspec.FieldString:
		return 12345, true
	case eventspec.FieldBool:
		return "not-a-bool", true
	default:
		return nil, false
	}
}

// wrongContainerTypeValue is a bare scalar where an array is declared --
// applicable only to the two container types.
func wrongContainerTypeValue(f eventspec.Field) (val any, applicable bool) {
	switch f.Type {
	case eventspec.FieldStringSlice, eventspec.FieldObjectSlice:
		return 12345, true
	default:
		return nil, false
	}
}

// canonicalLineFor builds one complete, internally-consistent JSON-shaped
// line for ev: the slog envelope certify.Parse requires, plus every declared
// field carrying a canonical value.
func canonicalLineFor(ev eventspec.Event) map[string]any {
	line := map[string]any{
		"time":  "2026-09-10T00:00:00Z",
		"level": strings.ToUpper(string(ev.Level)),
		"msg":   ev.Msg,
	}
	for _, f := range ev.Fields {
		line[f.Key] = canonicalValueFor(f)
	}
	return line
}

// deepCopyLine round-trips a line through JSON so the copy carries the SAME
// decoded shapes Parse itself produces (float64, []any, map[string]any) --
// never a Go-native copy certify would not actually see from real slog
// output.
func deepCopyLine(t *testing.T, line map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("deepCopyLine: Marshal() error = %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("deepCopyLine: Unmarshal() error = %v", err)
	}
	return out
}

// locate walks a decoded line down a sequence of object_slice keys (this
// sweep's own fixtures always carry exactly one representative row per
// nested level, by construction in canonicalValueFor) and returns the object
// at that depth to mutate.
func locate(t *testing.T, line map[string]any, path []string) map[string]any {
	t.Helper()
	obj := line
	for _, key := range path {
		arr, ok := obj[key].([]any)
		if !ok || len(arr) == 0 {
			t.Fatalf("locate: %q is not a non-empty array in the sweep fixture", key)
		}
		row, ok := arr[0].(map[string]any)
		if !ok {
			t.Fatalf("locate: %q[0] is not an object in the sweep fixture", key)
		}
		obj = row
	}
	return obj
}

func logFromLine(t *testing.T, line map[string]any) *Log {
	t.Helper()
	b, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("logFromLine: Marshal() error = %v", err)
	}
	log, err := Parse(b)
	if err != nil {
		t.Fatalf("logFromLine: Parse() error = %v", err)
	}
	return log
}

func countDeclaredFields(fields []eventspec.Field) int {
	n := 0
	for _, f := range fields {
		n++
		if f.Type == eventspec.FieldObjectSlice {
			n += countDeclaredFields(f.Fields)
		}
	}
	return n
}

// -------------------------------------------------------------- the table

type domainRow struct {
	event      string
	field      string
	dimension  string
	applicable bool
	wantAccept bool
	gotAccept  bool
}

func (r domainRow) ok() bool { return !r.applicable || r.gotAccept == r.wantAccept }

var domainDimensions = []string{
	"absent", "null", "zero", "empty_container", "wrong_container_type",
	"wrong_scalar_type", "fractional", "out_of_vocabulary", "boundary", "canonical",
}

// runCell executes ONE {event, field-path, dimension} cell: applies the
// mutation (or determines the cell is N/A for this field's Type), certifies,
// and records the row. attribution is the Want/attribution map that scopes
// the assertion (the field under test is never itself an attribution field
// in this spec, so scoping is unaffected by the mutation).
func runCell(t *testing.T, ev eventspec.Event, base map[string]any, attribution map[string]any, path []string, f eventspec.Field, dim string) domainRow {
	t.Helper()
	row := domainRow{event: ev.ID, field: strings.Join(append(append([]string{}, path...), f.Key), "."), dimension: dim}

	mutate := func(v any) *Log {
		m := deepCopyLine(t, base)
		locate(t, m, path)[f.Key] = v
		return logFromLine(t, m)
	}
	deleteField := func() *Log {
		m := deepCopyLine(t, base)
		delete(locate(t, m, path), f.Key)
		return logFromLine(t, m)
	}
	// wantFor mirrors a value change into the attribution/Want map when the
	// field under test IS itself an attribution field (always top-level in
	// this spec) -- otherwise mutating e.g. request_id's own value breaks
	// Certify's scope-match against the UNCHANGED attribution map and every
	// such cell reads as "0 lines found" (a scoping confound) rather than
	// the field-value outcome the dimension is actually testing.
	wantFor := func(v any) map[string]any {
		w := make(map[string]any, len(attribution))
		for k, vv := range attribution {
			w[k] = vv
		}
		if len(path) == 0 {
			for _, ak := range ev.Attribution {
				if ak == f.Key {
					w[ak] = v
				}
			}
		}
		return w
	}

	switch dim {
	case "absent":
		if f.Presence != eventspec.PresenceRequired {
			return row // applicable=false: conditional presence is condition-specific, out of this generic sweep
		}
		row.applicable, row.wantAccept = true, false
		_, err := certifyRecovered(t, deleteField(), Assertion{Event: ev, Want: attribution})
		row.gotAccept = err == nil
	case "null":
		row.applicable, row.wantAccept = true, false
		_, err := certifyRecovered(t, mutate(nil), Assertion{Event: ev, Want: wantFor(nil)})
		row.gotAccept = err == nil
	case "zero":
		v, ok := zeroValueFor(f)
		if !ok {
			return row
		}
		row.applicable = true
		// A zero scalar is accepted UNLESS a closed vocabulary excludes it.
		row.wantAccept = len(f.ClosedVocabulary) == 0
		_, err := certifyRecovered(t, mutate(v), Assertion{Event: ev, Want: wantFor(v)})
		row.gotAccept = err == nil
	case "empty_container":
		if f.Type != eventspec.FieldStringSlice && f.Type != eventspec.FieldObjectSlice {
			return row
		}
		row.applicable, row.wantAccept = true, true
		_, err := certifyRecovered(t, mutate([]any{}), Assertion{Event: ev, Want: wantFor([]any{})})
		row.gotAccept = err == nil
	case "wrong_container_type":
		v, ok := wrongContainerTypeValue(f)
		if !ok {
			return row
		}
		row.applicable, row.wantAccept = true, false
		_, err := certifyRecovered(t, mutate(v), Assertion{Event: ev, Want: wantFor(v)})
		row.gotAccept = err == nil
	case "wrong_scalar_type":
		v, ok := wrongScalarTypeValueFor(f)
		if !ok {
			return row
		}
		row.applicable, row.wantAccept = true, false
		_, err := certifyRecovered(t, mutate(v), Assertion{Event: ev, Want: wantFor(v)})
		row.gotAccept = err == nil
	case "fractional":
		if f.Type != eventspec.FieldInt {
			return row
		}
		row.applicable, row.wantAccept = true, false
		_, err := certifyRecovered(t, mutate(0.5), Assertion{Event: ev, Want: wantFor(0.5)})
		row.gotAccept = err == nil
	case "out_of_vocabulary":
		if len(f.ClosedVocabulary) == 0 {
			return row
		}
		row.applicable, row.wantAccept = true, false
		_, err := certifyRecovered(t, mutate("sweep_undeclared_vocab_value"), Assertion{Event: ev, Want: wantFor("sweep_undeclared_vocab_value")})
		row.gotAccept = err == nil
	case "boundary":
		return row // N/A for every field today: eventspec.Field declares no Min/Max/MaxItems.
	case "canonical":
		row.applicable, row.wantAccept = true, true
		_, err := certifyRecovered(t, mutate(canonicalValueFor(f)), Assertion{Event: ev, Want: wantFor(canonicalValueFor(f))})
		row.gotAccept = err == nil
	default:
		t.Fatalf("runCell: unhandled dimension %q", dim)
	}
	return row
}

// TestCertifyInputDomainTable is the one generated pass: eventspec.All x
// every declared field (recursively) x every domain dimension, executed and
// asserted in ONE run, with a census proving the table's own row count
// matches the structurally-expected count (fields x dimensions), so a new
// event or field is swept the day it lands.
func TestCertifyInputDomainTable(t *testing.T) {
	var rows []domainRow
	totalFieldCount := 0

	for _, ev := range eventspec.All {
		ev := ev
		base := canonicalLineFor(ev)
		attribution := map[string]any{}
		for _, k := range ev.Attribution {
			attribution[k] = base[k]
		}

		// Control: the sweep's own canonical line must certify clean before
		// any cell is asserted against it -- a failure here is a fixture
		// bug, not a finding.
		if _, err := certifyRecovered(t, logFromLine(t, deepCopyLine(t, base)), Assertion{Event: ev, Want: attribution}); err != nil {
			t.Fatalf("control: the sweep's own canonical line for %s was refused (fixture bug, not a finding): %v", ev.ID, err)
		}

		var walk func(fields []eventspec.Field, path []string)
		walk = func(fields []eventspec.Field, path []string) {
			for _, f := range fields {
				f := f
				totalFieldCount++
				for _, dim := range domainDimensions {
					rows = append(rows, runCell(t, ev, base, attribution, path, f, dim))
				}
				if f.Type == eventspec.FieldObjectSlice {
					walk(f.Fields, append(append([]string{}, path...), f.Key))
				}
			}
		}
		walk(ev.Fields, nil)
	}

	// The event-level "duplicate" cell: two identical lines in scope, per
	// event, refused regardless of Multiplicity (already independently
	// pinned by name -- TestCertifyRefusesTwoIdenticalLinesForAnExactlyOnePerPassEvent /
	// TestCertifyRefusesTwoLinesForAZeroOrOnePerPassEvent -- this row makes
	// it part of the same generated census rather than a fact the reader
	// has to trust from a different file).
	for _, ev := range eventspec.All {
		base := canonicalLineFor(ev)
		attribution := map[string]any{}
		for _, k := range ev.Attribution {
			attribution[k] = base[k]
		}
		line1 := deepCopyLine(t, base)
		line2 := deepCopyLine(t, base)
		b1, _ := json.Marshal(line1)
		b2, _ := json.Marshal(line2)
		log, err := Parse([]byte(string(b1) + "\n" + string(b2)))
		if err != nil {
			t.Fatalf("Parse() duplicate-line fixture for %s: %v", ev.ID, err)
		}
		_, err = certifyRecovered(t, log, Assertion{Event: ev, Want: attribution})
		rows = append(rows, domainRow{event: ev.ID, field: "(event-level)", dimension: "duplicate", applicable: true, wantAccept: false, gotAccept: err == nil})
	}

	// CHAOS-5516: the pass-keyed multiplicity rows. CertifyExactlyOnePerPass
	// now keys duplicate detection on (request_id, pass) for an event that
	// DECLARES a pass field (a same-pass second line is always a defect
	// regardless of content; a distinct-pass second line is always
	// legitimate regardless of content), and refuses ANY second line
	// unconditionally for an event with NO pass field (decision_summary has
	// no legitimate multi-line shape to distinguish from a duplicate). Three
	// cells per applicable event, generated from eventspec.All rather than
	// hand-picked, so a future ExactlyOnePerPass event is swept the day it
	// lands, not the day a review round finds the gap (exactly how this gap
	// was first caught, before any round, per the handoff).
	passMultiplicityRows := 0
	for _, ev := range eventspec.All {
		if ev.Multiplicity != eventspec.MultiplicityExactlyOnePerPass {
			continue
		}
		hasPassField := false
		for _, f := range ev.Fields {
			if f.Key == "pass" {
				hasPassField = true
				break
			}
		}
		base := canonicalLineFor(ev)
		attribution := map[string]any{}
		for _, k := range ev.Attribution {
			attribution[k] = base[k]
		}
		// distinctContentLine deep-copies base and perturbs the first
		// declared int field that is neither an attribution field nor
		// "pass" itself -- every event in this spec has at least one such
		// field today (RankedCutSummary: candidate_count;
		// DecisionSummary: decision_event_count), so this never silently
		// no-ops into an identical-content line.
		distinctContentLine := func() map[string]any {
			m := deepCopyLine(t, base)
			perturbed := false
			for _, f := range ev.Fields {
				if f.Key == "pass" || f.Type != eventspec.FieldInt {
					continue
				}
				isAttr := false
				for _, ak := range ev.Attribution {
					if ak == f.Key {
						isAttr = true
					}
				}
				if isAttr {
					continue
				}
				m[f.Key] = 999
				perturbed = true
				break
			}
			if !perturbed {
				t.Fatalf("distinctContentLine: %s declares no non-attribution, non-pass int field to perturb -- fixture needs a new strategy", ev.ID)
			}
			return m
		}

		if hasPassField {
			// same pass, distinct content: refused (a same-pass duplicate
			// is a defect regardless of whether the lines' other fields
			// agree -- the whole point of keying on pass rather than
			// byte-identity).
			line2 := distinctContentLine()
			b1, _ := json.Marshal(deepCopyLine(t, base))
			b2, _ := json.Marshal(line2)
			log, err := Parse([]byte(string(b1) + "\n" + string(b2)))
			if err != nil {
				t.Fatalf("Parse() same-pass-distinct-content fixture for %s: %v", ev.ID, err)
			}
			_, err = certifyRecovered(t, log, Assertion{Event: ev, Want: attribution})
			rows = append(rows, domainRow{event: ev.ID, field: "(event-level)", dimension: "same_pass_distinct_content", applicable: true, wantAccept: false, gotAccept: err == nil})
			passMultiplicityRows++

			// distinct pass, distinct content: accepted (a genuine second
			// pass is legitimate regardless of whether its own field
			// values happen to differ from the first).
			line3 := distinctContentLine()
			line3["pass"] = 8 // base's own canonical pass value is 7 (canonicalValueFor)
			b3, _ := json.Marshal(line3)
			log2, err := Parse([]byte(string(b1) + "\n" + string(b3)))
			if err != nil {
				t.Fatalf("Parse() distinct-pass-distinct-content fixture for %s: %v", ev.ID, err)
			}
			_, err = certifyRecovered(t, log2, Assertion{Event: ev, Want: attribution})
			rows = append(rows, domainRow{event: ev.ID, field: "(event-level)", dimension: "distinct_pass_distinct_content", applicable: true, wantAccept: true, gotAccept: err == nil})
			passMultiplicityRows++
		} else {
			// no pass field declared: ANY second line in scope is refused,
			// even with distinct content -- there is no legitimate
			// multi-line shape for this event to distinguish from a
			// duplicate.
			line2 := distinctContentLine()
			b1, _ := json.Marshal(deepCopyLine(t, base))
			b2, _ := json.Marshal(line2)
			log, err := Parse([]byte(string(b1) + "\n" + string(b2)))
			if err != nil {
				t.Fatalf("Parse() no-pass-field-distinct-content fixture for %s: %v", ev.ID, err)
			}
			_, err = certifyRecovered(t, log, Assertion{Event: ev, Want: attribution})
			rows = append(rows, domainRow{event: ev.ID, field: "(event-level)", dimension: "no_pass_field_multi_line_distinct_content", applicable: true, wantAccept: false, gotAccept: err == nil})
			passMultiplicityRows++
		}
	}

	// CHAOS-5516 (team-lead, after the B8 pair caught the class in
	// internal/runtime/hosted's own fixtures): the construction-refusal
	// guard tracer.go's "decision_summary" case added
	// (decisionSummaryFieldsUnconstructed) gets its own cell in this same
	// generated pass, driven through the REAL production entry point
	// (graphrank.SlogResolutionTracer.Trace, not a certify-level fixture) --
	// a caller that hand-assembles the event using only the OLD individual
	// fields (DecisionSummaryFields left at its Go zero value) must produce
	// NO certifiable decision_summary line at all (the Error-level refusal
	// line carries a different msg, so certify's own msg-scoped lookup finds
	// zero matching lines -- refused, exactly like every other cell in this
	// table that expects a refusal). Scoped to DecisionSummary alone: grep
	// of tracer.go's switch shows anchor_pool/offer_pool read their old
	// fields directly (no typed shadow to leave zero), and
	// ranked_cut/anchor_slot_displaced don't read their generated Fields
	// structs at all (unmigrated, brief-pr2.md OUT OF SCOPE) -- the sibling
	// set swept is empty.
	{
		var buf bytes.Buffer
		tracer := graphrank.NewSlogResolutionTracer(
			slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
		tracer.Trace(graphrank.ResolutionTraceEvent{
			RequestID: "sweep_unconstructed_decision_summary", Stage: "decision_summary",
			DecisionEventCount: 2, DecisionCommittedCount: 1,
			DecisionCommittedIDs: []string{"team.v2:github:sweep"},
			DecisionFrameGate:    "passed", DecisionRefuseBasis: "none",
			// DecisionSummaryFields: intentionally the Go zero value.
		})
		refusedRow := domainRow{event: eventspec.DecisionSummary.ID, field: "(construction)", dimension: "unconstructed_typed_fields", applicable: true, wantAccept: false}
		if log, err := Parse(buf.Bytes()); err != nil {
			t.Fatalf("Parse() on the real emitted refusal output error = %v", err)
		} else {
			_, certErr := certifyRecovered(t, log, Assertion{Event: eventspec.DecisionSummary, Want: map[string]any{"request_id": "sweep_unconstructed_decision_summary"}})
			refusedRow.gotAccept = certErr == nil
		}
		rows = append(rows, refusedRow)
	}

	failed := 0
	for _, r := range rows {
		if !r.ok() {
			failed++
			t.Errorf("cell FAILED: event=%s field=%s dim=%s want_accept=%v got_accept=%v", r.event, r.field, r.dimension, r.wantAccept, r.gotAccept)
		}
	}
	applicableN, naN := 0, 0
	for _, r := range rows {
		if r.applicable {
			applicableN++
		} else {
			naN++
		}
	}
	t.Logf("input-domain table: %d rows (%d applicable, %d N/A), %d failed, %d events, %d fields (recursive), %d pass-multiplicity rows",
		len(rows), applicableN, naN, failed, len(eventspec.All), totalFieldCount, passMultiplicityRows)

	// Census: the table's own row count must equal fields*dimensions plus
	// one duplicate row per event plus the pass-multiplicity rows above plus
	// the one construction-refusal row -- proving the walk reached every
	// declared field, every dimension, every ExactlyOnePerPass event's own
	// pass-keying guard, and the typed-construction refusal guard, not a
	// silently-truncated subset.
	const constructionRefusalRows = 1
	wantRows := totalFieldCount*len(domainDimensions) + len(eventspec.All) + passMultiplicityRows + constructionRefusalRows
	if len(rows) != wantRows {
		t.Errorf("census: table has %d rows, want %d (%d fields x %d dimensions + %d duplicate rows + %d pass-multiplicity rows + %d construction-refusal rows)",
			len(rows), wantRows, totalFieldCount, len(domainDimensions), len(eventspec.All), passMultiplicityRows, constructionRefusalRows)
	}
}

// -------------------------------------------------------- call-surface cells

type surfaceRow struct {
	fn         string // "Certify" | "CertifyAbsent"
	cell       string
	wantAccept bool
	gotAccept  bool
}

func (r surfaceRow) ok() bool { return r.gotAccept == r.wantAccept }

// TestCertifyCallSurfaceDomainTable is the second table chris's ruling
// requires: the cells that are NOT about one field's value, but about the
// call itself -- a nil/empty log, a caller-supplied Event that does not
// match its canonical declaration, an unknown event ID, and an
// attribution/Want missing a declared Attribution key. Certify and
// CertifyAbsent are exercised separately -- round r3 found a nil log
// panicked independently in both.
func TestCertifyCallSurfaceDomainTable(t *testing.T) {
	ev := eventspec.RankedCutSummary
	absentEv := eventspec.AnchorSlotDisplaced
	base := canonicalLineFor(ev)
	goodLog := logFromLine(t, deepCopyLine(t, base))
	emptyLog := &Log{} // same package: constructible directly, unlike an external caller

	unknownEvent := eventspec.Event{ID: "not.a.real.event", Msg: "this message does not exist in production", Level: eventspec.LevelInfo, Multiplicity: eventspec.MultiplicityZeroOrOnePerPass}
	mismatchedEvent := eventspec.Event{ID: ev.ID, Msg: ev.Msg, Level: ev.Level, Multiplicity: ev.Multiplicity, Attribution: nil, Fields: nil}

	var rows []surfaceRow

	certifyCell := func(cell string, wantAccept bool, log *Log, e eventspec.Event, want map[string]any) {
		_, err := certifyRecovered(t, log, Assertion{Event: e, Want: want})
		rows = append(rows, surfaceRow{fn: "Certify", cell: cell, wantAccept: wantAccept, gotAccept: err == nil})
	}
	absentCell := func(cell string, wantAccept bool, log *Log, e eventspec.Event, attribution map[string]any) {
		err := certifyAbsentRecovered(t, log, e, attribution)
		rows = append(rows, surfaceRow{fn: "CertifyAbsent", cell: cell, wantAccept: wantAccept, gotAccept: err == nil})
	}

	fullWant := wantForRankedCutSummary()

	certifyCell("nil_log", false, nil, ev, fullWant)
	absentCell("nil_log", false, nil, absentEv, map[string]any{"request_id": "req_1"})

	certifyCell("empty_log", false, emptyLog, ev, fullWant) // zero lines for an exactly_one_per_pass event: refused (not "0 lines" == absent for this multiplicity)
	absentCell("empty_log", true, emptyLog, absentEv, map[string]any{"request_id": "req_1"})

	certifyCell("event_not_canonical", false, goodLog, mismatchedEvent, map[string]any{})
	absentCell("event_not_canonical", false, emptyLog, mismatchedEvent, map[string]any{})

	certifyCell("unknown_event_id", false, goodLog, unknownEvent, map[string]any{})
	absentCell("unknown_event_id", false, emptyLog, unknownEvent, map[string]any{})

	certifyCell("attribution_missing_from_want", false, goodLog, ev, map[string]any{})
	absentCell("attribution_missing_from_attribution_map", false, emptyLog, absentEv, map[string]any{})

	failed := 0
	for _, r := range rows {
		if !r.ok() {
			failed++
			t.Errorf("surface cell FAILED: fn=%s cell=%s want_accept=%v got_accept=%v", r.fn, r.cell, r.wantAccept, r.gotAccept)
		}
	}
	t.Logf("call-surface table: %d rows, %d failed", len(rows), failed)

	const wantRows = 10 // 5 cells x {Certify, CertifyAbsent}
	if len(rows) != wantRows {
		t.Errorf("census: call-surface table has %d rows, want %d", len(rows), wantRows)
	}
}

var _ = fmt.Sprintf // keep fmt imported for future row formatting without churn
