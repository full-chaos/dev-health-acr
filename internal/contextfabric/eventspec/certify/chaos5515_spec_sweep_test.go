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
// vocabularyContainsValue reports whether a zero value is itself a declared
// member of a closed string vocabulary. Non-string zeros never are.
func vocabularyContainsValue(vocabulary []string, value any) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	for _, member := range vocabulary {
		if member == s {
			return true
		}
	}
	return false
}

func canonicalValueFor(f eventspec.Field) any {
	switch f.Type {
	case eventspec.FieldString:
		if len(f.ClosedVocabulary) > 0 {
			return f.ClosedVocabulary[0]
		}
		return "sweep_canonical_string"
	case eventspec.FieldInt:
		// CHAOS-5517: "index"/"total" are the ONE pair of int fields whose
		// own VALUES have a cross-field meaning (certifyBoundedMany asserts
		// the observed line count equals "total", and "index" must fall in
		// 1..total) -- a single canonical line is self-consistent only at
		// index=1, total=1 (a bounded-many scope of exactly one line).
		// Every other int field keeps the arbitrary, non-zero 7 the rest of
		// this sweep already depends on to distinguish "canonical" from
		// "zero".
		if f.Key == "index" || f.Key == "total" {
			return 1
		}
		return 7
	case eventspec.FieldFloat:
		return 0.5
	case eventspec.FieldBool:
		return true
	case eventspec.FieldStringSlice:
		return []string{"sweep_elem"}
	case eventspec.FieldObject:
		// ONE nested object, built from its declared members exactly as an
		// object_slice row is -- so the sweep reaches every member key and a
		// member added to the declaration without a canonical shape here
		// fails this control rather than passing unmeasured.
		obj := map[string]any{}
		for _, nf := range f.Fields {
			obj[nf.Key] = canonicalValueFor(nf)
		}
		return obj
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
	case eventspec.FieldFloat:
		return 0.0, true
	case eventspec.FieldBool:
		return false, true
	default:
		return nil, false // containers use empty_container instead
	}
}

// wrongScalarTypeValueFor returns a JSON scalar shape that is NOT the
// declared one -- applicable to every scalar type (string/int/float/bool).
func wrongScalarTypeValueFor(f eventspec.Field) (val any, applicable bool) {
	switch f.Type {
	case eventspec.FieldInt:
		return "not-an-int", true
	case eventspec.FieldFloat:
		return "not-a-float", true
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
	case eventspec.FieldStringSlice, eventspec.FieldObjectSlice, eventspec.FieldObject:
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

// marshalJoinLines is the package-level form of the per-event marshalJoin
// closure -- used by the CHAOS-5517 bounded-many/zero-or-one-per-request
// cells, which iterate eventspec.All independently of the main
// per-event multiplicity loop and so cannot reach that loop's own local
// closure.
func marshalJoinLines(t *testing.T, eventID string, lines ...map[string]any) *Log {
	t.Helper()
	parts := make([]string, len(lines))
	for i, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		parts[i] = string(b)
	}
	log, err := Parse([]byte(strings.Join(parts, "\n")))
	if err != nil {
		t.Fatalf("Parse() fixture for %s error = %v", eventID, err)
	}
	return log
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

// wantFor builds the Want/attribution map Certify now requires: every
// declared Attribution field, PLUS "pass" (read off base) whenever ev
// declares a pass field -- r3's own new rule (Certify refuses a pass-keyed
// event's Want that omits "pass": the caller must state which attempt it
// certifies, same as an Attribution field).
func attributionWant(ev eventspec.Event, base map[string]any) map[string]any {
	w := map[string]any{}
	for _, k := range ev.Attribution {
		w[k] = base[k]
	}
	if eventHasPassField(ev.Fields) {
		w["pass"] = base["pass"]
	}
	if ev.Multiplicity == eventspec.MultiplicityBoundedManyPerPass {
		// CHAOS-5517: certifyBoundedMany requires Want["index"] to select
		// which of the scope's own lines is being value-asserted -- the
		// canonical single-line fixture is self-consistent at index=1
		// (canonicalValueFor's own "index"/"total" special case above).
		w["index"] = base["index"]
	}
	return w
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
	// the field-value outcome the dimension is actually testing. "pass" gets
	// the SAME treatment as of r3: Certify now requires Want["pass"] for a
	// pass-keyed event, so a cell that mutates pass's own value must mirror
	// it into Want too, or every non-canonical "pass" cell reads as a value
	// mismatch against the stale canonical Want["pass"] rather than the
	// outcome the dimension is actually testing.
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
			if f.Key == "pass" {
				w["pass"] = v
			}
			// CHAOS-5517 (hosted battery finding, G16/G18/G19 survived):
			// mutating "index" on the fixture LINE without also updating
			// Want["index"] makes the cell refuse for the WRONG reason (a
			// selection mismatch: certifyBoundedMany can't find a line at
			// the stale canonical index) rather than the guard the cell
			// claims to exercise (verifyBoundedManyGroup's own range/
			// duplicate/count checks) -- a vacuous pin that a mutant
			// disabling the real guard still passes. Track it the same way
			// "pass" already is.
			if f.Key == "index" {
				w["index"] = v
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
		// A zero scalar is accepted UNLESS a closed vocabulary excludes it,
		// or (CHAOS-5517) the field is "index"/"total" -- the one pair
		// whose valid range is constrained by a CROSS-LINE invariant
		// (certifyBoundedMany's own consistency check) rather than a
		// per-field ClosedVocabulary: index=0 falls outside the declared
		// 1..total range, and total=0 disagrees with the sweep's own
		// single-line fixture (which always carries exactly one line).
		//
		// CHAOS-5582: "excludes" is MEMBERSHIP, not the presence of a
		// vocabulary -- a closed field whose declared members include the
		// explicit empty string (a family, an axis that never applied)
		// accepts it, and one whose members do not refuses it. The two
		// rules are independent and both must hold, so they conjoin: the
		// cross-line index/total constraint is not a vocabulary and no
		// membership test can satisfy it.
		row.wantAccept = (len(f.ClosedVocabulary) == 0 || vocabularyContainsValue(f.ClosedVocabulary, v)) &&
			f.Key != "index" && f.Key != "total"
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
		attribution := attributionWant(ev, base)

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
		attribution := attributionWant(ev, base)
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

	// r3: the multiplicity domain -- every event, generated from
	// eventspec.All rather than hand-picked (round r2's own class was
	// caught this way, before any review round: adding an event to All
	// automatically swept it here). Categorised by eventHasPassField, NOT
	// by which Multiplicity constant is declared -- round r3's finding 1
	// (zero_or_one_per_pass never got the pass-keyed treatment) is exactly
	// what a Multiplicity-keyed filter would have kept missing; a
	// pass-bearing filter covers BOTH exactly_one_per_pass
	// (RankedCutSummary) and zero_or_one_per_pass (AnchorSlotDisplaced)
	// uniformly, matching what Certify itself now does.
	passMultiplicityRows := 0
	for _, ev := range eventspec.All {
		hasPassField := eventHasPassField(ev.Fields)
		base := canonicalLineFor(ev)
		attribution := attributionWant(ev, base)

		// distinctContentLine deep-copies base and perturbs the first
		// declared int field that is neither an attribution field nor
		// "pass" itself -- every event in this spec has at least one such
		// field today (RankedCutSummary: candidate_count;
		// AnchorSlotDisplaced: anchor_slot_displaced;
		// DecisionSummary: decision_event_count), so this never silently
		// no-ops into an identical-content line.
		// perturbableField returns the first declared field eligible for
		// this sweep's own content-perturbation fixtures: an int/float
		// field is preferred (the original strategy every earlier event
		// used); CHAOS-5517's OfferPool declares none (its only
		// non-attribution, non-pass/index/total fields are subject_kind/
		// subject_canonical_id/disposition, all strings) -- an
		// open-vocabulary string field (no ClosedVocabulary; "stage" and
		// "disposition" are closed and excluded) is the fallback, so this
		// never silently no-ops into an identical-content line.
		perturbableField := func() (eventspec.Field, bool) {
			isAttr := func(key string) bool {
				for _, ak := range ev.Attribution {
					if ak == key {
						return true
					}
				}
				return false
			}
			for _, f := range ev.Fields {
				if f.Key == "pass" || f.Key == "index" || f.Key == "total" || isAttr(f.Key) {
					continue
				}
				if f.Type == eventspec.FieldInt || f.Type == eventspec.FieldFloat {
					return f, true
				}
			}
			for _, f := range ev.Fields {
				if f.Key == "pass" || f.Key == "index" || f.Key == "total" || isAttr(f.Key) {
					continue
				}
				if f.Type == eventspec.FieldString && len(f.ClosedVocabulary) == 0 {
					return f, true
				}
			}
			// CHAOS-5517: IdentityUniverse declares only request_id (attr),
			// stage (closed vocab, excluded), and "complete" (bool) -- no
			// int/float/open-string field at all, so bool is the last
			// fallback.
			for _, f := range ev.Fields {
				if f.Key == "pass" || f.Key == "index" || f.Key == "total" || isAttr(f.Key) {
					continue
				}
				if f.Type == eventspec.FieldBool {
					return f, true
				}
			}
			// CHAOS-5636: LowPopulationKindScopeSummary declares only
			// request_id (attr), stage (closed vocab, excluded, single
			// member), and "outcome" -- a MULTI-member closed-vocab string,
			// no int/float/open-string/bool field at all. A closed-vocab
			// field with at least two members is still perturbable to
			// distinct, VALID content by picking a different member (see
			// distinctContentLine/malformedLine's own ClosedVocabulary
			// handling below) -- this is the last fallback, after every
			// unconstrained type has already been tried.
			for _, f := range ev.Fields {
				if f.Key == "pass" || f.Key == "index" || f.Key == "total" || isAttr(f.Key) {
					continue
				}
				if f.Type == eventspec.FieldString && len(f.ClosedVocabulary) >= 2 {
					return f, true
				}
			}
			return eventspec.Field{}, false
		}
		distinctContentLine := func(perturbValue int) map[string]any {
			m := deepCopyLine(t, base)
			f, ok := perturbableField()
			if !ok {
				t.Fatalf("distinctContentLine: %s declares no non-attribution, non-pass field to perturb -- fixture needs a new strategy", ev.ID)
			}
			switch f.Type {
			case eventspec.FieldInt:
				m[f.Key] = perturbValue
			case eventspec.FieldFloat:
				m[f.Key] = float64(perturbValue) + 0.5
			case eventspec.FieldString:
				if len(f.ClosedVocabulary) > 0 {
					// canonicalValueFor always seeds a closed-vocab string
					// with ClosedVocabulary[0] -- "distinct content" that
					// stays VALID (never outside the declared vocabulary,
					// which would trip a different check entirely) is the
					// next member.
					m[f.Key] = f.ClosedVocabulary[1]
					break
				}
				m[f.Key] = fmt.Sprintf("sweep_distinct_content_%d", perturbValue)
			case eventspec.FieldBool:
				// canonicalValueFor's own bool value is always true --
				// "distinct content" is simply the other value.
				m[f.Key] = false
			}
			return m
		}
		// malformedLine corrupts the SAME field distinctContentLine would
		// perturb, but with a wrong SCALAR TYPE (a string in place of a
		// declared int/float, a number in place of a declared string) -- a
		// validateFields-catchable defect, for the "malformed earlier line"
		// cells (round r3 finding 2).
		malformedLine := func(pass any) map[string]any {
			m := deepCopyLine(t, base)
			if pass != nil {
				m["pass"] = pass
			}
			f, ok := perturbableField()
			if !ok {
				t.Fatalf("malformedLine: %s declares no non-attribution, non-pass field to corrupt -- fixture needs a new strategy", ev.ID)
			}
			switch f.Type {
			case eventspec.FieldInt, eventspec.FieldFloat:
				m[f.Key] = "malformed-not-a-number"
			case eventspec.FieldString:
				m[f.Key] = 999
			case eventspec.FieldBool:
				m[f.Key] = "malformed-not-a-bool"
			}
			return m
		}
		marshalJoin := func(lines ...map[string]any) *Log {
			return marshalJoinLines(t, ev.ID, lines...)
		}
		record := func(dim string, wantAccept bool, log *Log, want map[string]any) {
			_, err := certifyRecovered(t, log, Assertion{Event: ev, Want: want})
			rows = append(rows, domainRow{event: ev.ID, field: "(multiplicity)", dimension: dim, applicable: true, wantAccept: wantAccept, gotAccept: err == nil})
			passMultiplicityRows++
		}

		// 0 lines: refused for every event (Certify asserts PRESENCE;
		// CertifyAbsent, unchanged, is the absence assertion).
		record("zero_lines", false, &Log{}, attribution)

		if hasPassField {
			base1 := deepCopyLine(t, base)

			// same pass, distinct content: refused (a same-pass duplicate is
			// a defect regardless of whether the lines' other fields agree
			// -- the whole point of keying on pass rather than
			// byte-identity). Covers BOTH pass-bearing multiplicities
			// (round r3 finding 1: zero_or_one_per_pass used to refuse
			// unconditionally here, never keying on pass at all).
			samePass := distinctContentLine(999)
			record("same_pass_distinct_content", false, marshalJoin(base1, samePass), attribution)

			// distinct pass, distinct content: accepted (a genuine second
			// pass is legitimate regardless of whether its own field
			// values happen to differ from the first) -- for EITHER
			// pass-bearing multiplicity.
			distinctPass := distinctContentLine(999)
			distinctPass["pass"] = 8 // base's own canonical pass value is 7 (canonicalValueFor)
			wantLastPass := map[string]any{}
			for k, v := range attribution {
				wantLastPass[k] = v
			}
			wantLastPass["pass"] = 8
			record("distinct_pass_distinct_content", true, marshalJoin(base1, distinctPass), wantLastPass)

			// malformed EARLIER line, distinct (valid) later pass: refused
			// (round r3 finding 2 -- every line in scope is now
			// field-validated, not only the one ultimately selected).
			malformedEarlier := malformedLine(6.0)
			validLater := deepCopyLine(t, base)
			validLater["pass"] = 9
			wantLaterPass := map[string]any{}
			for k, v := range attribution {
				wantLaterPass[k] = v
			}
			wantLaterPass["pass"] = 9
			record("malformed_earlier_line", false, marshalJoin(malformedEarlier, validLater), wantLaterPass)
		} else {
			// no pass field declared: ANY second line in scope is refused,
			// even with distinct content -- there is no legitimate
			// multi-line shape for this event to distinguish from a
			// duplicate (MultiplicityExactlyOnePerRequest).
			base1 := deepCopyLine(t, base)
			second := distinctContentLine(999)
			record("no_pass_field_multi_line_distinct_content", false, marshalJoin(base1, second), attribution)

			// malformed earlier line, pass-less event: refused (round r3
			// finding 2, the pass-less side of the same fix).
			malformedEarlier := malformedLine(nil)
			validLater := deepCopyLine(t, base)
			record("malformed_earlier_line_no_pass", false, marshalJoin(malformedEarlier, validLater), attribution)
		}
	}

	// CHAOS-5517 (hosted battery finding, run 34624597754: G17/G18/G19
	// survived): a single-canonical-line fixture can NEVER exercise
	// verifyBoundedManyGroup's own cross-line checks (total-agreement,
	// duplicate-index, count-equals-total) -- with one line, "total" is
	// read FROM that line, so count trivially equals it regardless of
	// whether the guard runs at all. These three cells are the dedicated,
	// deliberately MULTI-line fixtures each guard needs, generated over
	// every BoundedManyPerPass event so a future one is swept
	// automatically.
	boundedManyExtraRows := 0
	for _, ev := range eventspec.All {
		if ev.Multiplicity != eventspec.MultiplicityBoundedManyPerPass {
			continue
		}
		boundedManyExtraRows += 3
		base := canonicalLineFor(ev)
		attribution := attributionWant(ev, base)

		// Total disagreement: two lines in the same scope, each with an
		// index inside ITS OWN declared total's range (so the range check
		// alone cannot also catch this), but naming a DIFFERENT total --
		// refused regardless of which line's total is "right". lineB's
		// index=2 sits inside 1..3 (its own total) AND inside 1..2 (the
		// scope's total taken from lineA, i==0) -- isolating the
		// disagreement check from the range check, which lineB's index
		// would otherwise also trip if it were e.g. 3.
		lineA := deepCopyLine(t, base)
		lineA["index"], lineA["total"] = 1, 2
		lineB := deepCopyLine(t, base)
		lineB["index"], lineB["total"] = 2, 3
		wantA := map[string]any{}
		for k, v := range attribution {
			wantA[k] = v
		}
		wantA["index"] = 1
		_, err := certifyRecovered(t, marshalJoinLines(t, ev.ID, lineA, lineB), Assertion{Event: ev, Want: wantA})
		rows = append(rows, domainRow{event: ev.ID, field: "(bounded-many)", dimension: "total_disagreement", applicable: true, wantAccept: false, gotAccept: err == nil})

		// Duplicate index: two lines both claiming index=1 of a total=2
		// scope -- count(2)==total(2) so verifyBoundedManyGroup's OTHER
		// checks pass trivially; only the duplicate-index check itself can
		// refuse this.
		dupA := deepCopyLine(t, base)
		dupA["index"], dupA["total"] = 1, 2
		dupB := deepCopyLine(t, base)
		dupB["index"], dupB["total"] = 1, 2
		wantDup := map[string]any{}
		for k, v := range attribution {
			wantDup[k] = v
		}
		wantDup["index"] = 1
		_, err = certifyRecovered(t, marshalJoinLines(t, ev.ID, dupA, dupB), Assertion{Event: ev, Want: wantDup})
		rows = append(rows, domainRow{event: ev.ID, field: "(bounded-many)", dimension: "duplicate_index", applicable: true, wantAccept: false, gotAccept: err == nil})

		// Count mismatch: ONE line self-declaring total=2 (index=1, in
		// range, no duplicate possible with only one line) -- the scope
		// actually holds only 1 line, so count(1) != total(2).
		short := deepCopyLine(t, base)
		short["index"], short["total"] = 1, 2
		wantShort := map[string]any{}
		for k, v := range attribution {
			wantShort[k] = v
		}
		wantShort["index"] = 1
		_, err = certifyRecovered(t, marshalJoinLines(t, ev.ID, short), Assertion{Event: ev, Want: wantShort})
		rows = append(rows, domainRow{event: ev.ID, field: "(bounded-many)", dimension: "count_mismatch", applicable: true, wantAccept: false, gotAccept: err == nil})
	}

	// CHAOS-5517 (hosted battery finding: G20 survived): CertifyAbsent must
	// refuse an attribution map that includes "pass" for a
	// zero_or_one_per_request event (it has no pass concept at all) --
	// generated over every such event.
	zeroOrOneRequestAbsentRows := 0
	for _, ev := range eventspec.All {
		if ev.Multiplicity != eventspec.MultiplicityZeroOrOnePerRequest {
			continue
		}
		zeroOrOneRequestAbsentRows++
		attribution := map[string]any{}
		for _, ak := range ev.Attribution {
			attribution[ak] = "sweep_canonical_string"
		}
		withPass := map[string]any{}
		for k, v := range attribution {
			withPass[k] = v
		}
		withPass["pass"] = 1
		err := CertifyAbsent(&Log{}, ev, withPass)
		rows = append(rows, domainRow{event: ev.ID, field: "(multiplicity)", dimension: "absent_forbids_pass_attribution", applicable: true, wantAccept: false, gotAccept: err == nil})
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
	wantRows := totalFieldCount*len(domainDimensions) + len(eventspec.All) + passMultiplicityRows + constructionRefusalRows + boundedManyExtraRows + zeroOrOneRequestAbsentRows
	if len(rows) != wantRows {
		t.Errorf("census: table has %d rows, want %d (%d fields x %d dimensions + %d duplicate rows + %d pass-multiplicity rows + %d construction-refusal rows + %d bounded-many rows + %d zero-or-one-per-request absent rows)",
			len(rows), wantRows, totalFieldCount, len(domainDimensions), len(eventspec.All), passMultiplicityRows, constructionRefusalRows, boundedManyExtraRows, zeroOrOneRequestAbsentRows)
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
	absentCell("nil_log", false, nil, absentEv, map[string]any{"request_id": "req_1", "pass": 1})

	certifyCell("empty_log", false, emptyLog, ev, fullWant) // zero lines for an exactly_one_per_pass event: refused (not "0 lines" == absent for this multiplicity)
	absentCell("empty_log", true, emptyLog, absentEv, map[string]any{"request_id": "req_1", "pass": 1})

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
