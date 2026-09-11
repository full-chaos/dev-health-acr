package certify

import (
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// certifyRecovered calls Certify but recovers a panic into a normal returned
// error -- so a mutant that reintroduces an out-of-bounds index (e.g.
// disabling the "found 0 lines" guard, which would otherwise index the last
// element of an empty slice) fails every call site below as a clean, named
// test failure instead of crashing the whole package's test binary and
// aborting every test that runs after it. An unrecovered panic is Go's own
// testing semantics, not a harness defect, but it makes a real mutant kill
// unreadable to a battery's floor/RUN-count check -- this converts the kill
// signal back into something mechanical.
func certifyRecovered(t *testing.T, log *Log, a Assertion) (result Result, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("Certify() panicked: %v", r)
		}
	}()
	return Certify(log, a)
}

// certifyAbsentRecovered is certifyRecovered's counterpart for CertifyAbsent
// -- same reason: a mutant that reintroduces a nil-log dereference (or any
// other panic-inducing weakening) must fail as a clean, named test failure,
// never crash the whole package's test binary.
func certifyAbsentRecovered(t *testing.T, log *Log, ev eventspec.Event, attribution map[string]any) (err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("CertifyAbsent() panicked: %v", r)
		}
	}()
	return CertifyAbsent(log, ev, attribution)
}

// wantForRankedCutSummary is one internally-consistent, independently
// constructed fixture value set for eventspec.RankedCutSummary -- used by
// every test below so the controls differ from the passing case by exactly
// the one thing each is proving Certify refuses.
func wantForRankedCutSummary() map[string]any {
	return map[string]any{
		"request_id":            "req_1",
		"pass":                  1,
		"candidate_count":       92,
		"survived_count":        20,
		"max":                   20,
		"anchor_slot_reserved":  "team",
		"anchor_slot_source":    "receipt",
		"anchor_slot_displaced": 1,
		"pool_truncated_n":      72,
	}
}

func validRankedCutSummaryLine() string {
	return `{"time":"2026-09-10T00:00:00Z","level":"INFO","msg":"context fabric resolution trace: ranked cut summary",` +
		`"request_id":"req_1","pass":1,"stage":"ranked_cut","candidate_count":92,"survived_count":20,"survived_ids":["a","b"],"max":20,` +
		`"anchor_slot_reserved":"team","anchor_slot_source":"receipt","anchor_slot_displaced":1,"pool_truncated_n":72,"declared_kind_rescue":[]}`
}

// TestCertifyAcceptsRealProductionLikeOutput is the control-group pass: a
// well-formed line every subsequent red control mutates exactly one thing
// away from.
func TestCertifyAcceptsRealProductionLikeOutput(t *testing.T) {
	log, err := Parse([]byte(validRankedCutSummaryLine()))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if _, err := certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()}); err != nil {
		t.Fatalf("Certify() on a valid line error = %v, want nil", err)
	}
}

// A2 control (a): a recorder/capture struct in place of the real handler.
// A capture struct's own Go value, rendered as text (the shape every
// captureResolutionTracer-style fake would actually produce, and the shape a
// %+v or a hand-typed "field=value" log line takes), is not slog.JSONHandler
// output -- Parse must refuse it, not silently accept a shape the real
// handler never produces.
func TestCertifyRefusesACaptureStructInPlaceOfTheRealHandler(t *testing.T) {
	captureStructRendering := `{RequestID:req_1 Stage:ranked_cut AnchorSlotReserved:team AnchorSlotSource:receipt AnchorSlotDisplaced:1}`
	if _, err := Parse([]byte(captureStructRendering)); err == nil {
		t.Fatal("Parse() accepted a capture-struct rendering as if it were slog.JSONHandler output -- want a refusal")
	}

	// Even a struct that DOES marshal to JSON, but omits the handler's own
	// envelope (time/level/msg), must be refused -- a recorder that captured
	// only the event's own fields is not a substitute for the real handler.
	envelopeFreeJSON := `{"request_id":"req_1","stage":"ranked_cut","anchor_slot_reserved":"team"}`
	_, err := Parse([]byte(envelopeFreeJSON))
	if err == nil {
		t.Fatal("Parse() accepted JSON with no time/level/msg envelope -- want a refusal naming the missing handler envelope")
	}
	if !strings.Contains(err.Error(), "not slog.JSONHandler output") {
		t.Errorf("refusal text = %q, want it to say this is not real handler output", err.Error())
	}
}

// A2 control (b): a wrong record -- a twin line carrying the SAME msg AND
// the SAME attribution (request_id) with a DIFFERENT value on the field
// under test. Certify certifies the LAST such line (production's own
// documented multi-pass rule -- see TestCertifyAcceptsAGenuineMultiPassTwin
// below for the positive side of this same behaviour) and must still
// refuse when that last line disagrees with Want -- silently picking a
// value-wrong line and passing is exactly what this control kills.
func TestCertifyRefusesATwinLineWithTheSameMsgAndDifferentValues(t *testing.T) {
	// pass:2 on the twin: this test is about the VALUE check on the last of
	// two genuinely DISTINCT passes, not about duplicate-pass detection
	// (TestCertifyRefusesTwoIdenticalLinesForAnExactlyOnePerPassEvent and
	// TestCertifyRefusesTwoLinesForAZeroOrOnePerPassEvent own that).
	twin := strings.Replace(validRankedCutSummaryLine(), `"anchor_slot_reserved":"team"`, `"anchor_slot_reserved":"repo"`, 1)
	twin = strings.Replace(twin, `"pass":1`, `"pass":2`, 1)
	log, err := Parse([]byte(validRankedCutSummaryLine() + "\n" + twin))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()})
	if err == nil {
		t.Fatal("Certify() accepted a log whose LAST summary line disagrees with Want -- want a refusal naming the value mismatch")
	}
	if !strings.Contains(err.Error(), `"anchor_slot_reserved" = repo, want team`) {
		t.Errorf("refusal text = %q, want it to name the value mismatch on the LAST line", err.Error())
	}
}

// Positive control for the SAME behaviour the twin-line refusal above
// exercises: production genuinely emits more than one RankedCutSummary line
// for one request_id when a resolution re-decides across passes
// (TestRankedCutSummary_LastSummaryDescribesTheKeptPassAcrossReDecisionPasses,
// graphrank's own pre-existing, already-shipped proof) -- Certify must
// accept that shape and certify the LAST (kept) pass, never reject it as an
// ambiguous duplicate by raw count alone (round r1's P2).
func TestCertifyAcceptsAGenuineMultiPassTwin(t *testing.T) {
	// CHAOS-5516: the two passes are now told apart by their own DISTINCT
	// pass numbers (1 and 2), the real production signal -- not by their
	// candidate_count happening to differ, which was the pre-pass-field
	// proxy for "these are really two different passes".
	firstPass := strings.Replace(validRankedCutSummaryLine(), `"candidate_count":92`, `"candidate_count":2`, 1)
	keptPass := strings.Replace(validRankedCutSummaryLine(), `"pass":1`, `"pass":2`, 1)
	log, err := Parse([]byte(firstPass + "\n" + keptPass))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	// r3 fix: Want MUST include "pass" for a pass-keyed event (the caller
	// states which attempt it means to certify, the same requirement
	// Attribution fields already carry) -- pinned to 2, the KEPT pass, so
	// this control still proves Certify selects the LAST line in scope
	// (whose own pass happens to be 2 here) rather than the first.
	want := wantForRankedCutSummary()
	want["pass"] = 2
	if _, err := certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: want}); err != nil {
		t.Fatalf("Certify() refused a genuine multi-pass log (pass 1 candidate_count=2, pass 2/kept candidate_count=92, same request_id) -- want it to certify the LAST line: %v", err)
	}
}

// A2 control (c): a wrong LEVEL -- the exact same line, correctly formed and
// value-correct, but emitted at Debug instead of the declared Info. Presence
// of the right keys with the right values is not enough; production
// visibility is part of the certificate.
func TestCertifyRefusesTheRightLineAtTheWrongLevel(t *testing.T) {
	debugLine := strings.Replace(validRankedCutSummaryLine(), `"level":"INFO"`, `"level":"DEBUG"`, 1)
	log, err := Parse([]byte(debugLine))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()})
	if err == nil {
		t.Fatal("Certify() accepted a value-correct line demoted to Debug -- want a refusal: a Debug demotion that survives the pin is a finding against the pin")
	}
	if !strings.Contains(err.Error(), `at level "DEBUG"`) {
		t.Errorf("refusal text = %q, want it to name the wrong level", err.Error())
	}
}

// Sanity: a wrong VALUE on an otherwise well-formed, correctly-leveled,
// non-twin line is refused too (field deletion / wrong-value is A4's
// production-code mutation battery; this is the runner-level equivalent
// proving the comparison itself is not vacuous).
func TestCertifyRefusesAWrongValue(t *testing.T) {
	wrongValue := strings.Replace(validRankedCutSummaryLine(), `"pool_truncated_n":72`, `"pool_truncated_n":0`, 1)
	log, err := Parse([]byte(wrongValue))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()})
	if err == nil {
		t.Fatal("Certify() accepted pool_truncated_n=0 when the fixture expects 72 -- an expectation that cannot fail pins nothing")
	}
}

// Certify's closed-vocabulary guard: a value outside the event's declared
// closed vocabulary is refused even when the caller's own Want never named
// that value (the guard checks the LINE against the declaration, not just
// against Want) -- kills a mutant that weakens the vocabulary membership
// check.
func TestCertifyRefusesAValueOutsideTheClosedVocabulary(t *testing.T) {
	badVocab := strings.Replace(validRankedCutSummaryLine(), `"anchor_slot_source":"receipt"`, `"anchor_slot_source":"not_a_declared_source"`, 1)
	log, err := Parse([]byte(badVocab))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := wantForRankedCutSummary()
	want["anchor_slot_source"] = "not_a_declared_source"
	_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: want})
	if err == nil {
		t.Fatal("Certify() accepted anchor_slot_source=\"not_a_declared_source\", which is outside the event's declared closed vocabulary {receipt,confirmed_anchor,none} -- want a refusal")
	}
	if !strings.Contains(err.Error(), "closed vocabulary") {
		t.Errorf("refusal text = %q, want it to name the closed-vocabulary violation", err.Error())
	}
}

// Certify's field-presence guard: a Want key the event declares but the
// line omits entirely is refused, never silently skipped -- kills a mutant
// that weakens the presence check (missing must never read as a pass).
func TestCertifyRefusesAWantKeyMissingFromTheLine(t *testing.T) {
	missingKey := strings.Replace(validRankedCutSummaryLine(), `"pool_truncated_n":72,`, ``, 1)
	log, err := Parse([]byte(missingKey))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()})
	if err == nil {
		t.Fatal("Certify() accepted a line with no pool_truncated_n key at all, even though Want asserts it -- want a refusal naming the missing key")
	}
	if !strings.Contains(err.Error(), "has no") {
		t.Errorf("refusal text = %q, want it to name the missing key", err.Error())
	}
}

// Round r1's P1, executed repro reproduced here as a permanent regression
// test: a REQUIRED field the event declares, but that the caller's Want
// never happens to name, must still be checked for PRESENCE -- a field
// silently dropped from production output must never pass just because no
// fixture pinned its exact value. "survived_ids" and "declared_kind_rescue"
// are both declared PresenceRequired on RankedCutSummary and both absent
// from wantForRankedCutSummary()'s own Want map, by construction -- exactly
// the shape the reviewer's repro (deleting anchor_slot_displaced from the
// real tracer emission with every existing test still PASSING) proved was a
// gap.
func TestCertifyRefusesALineMissingARequiredFieldNotNamedInWant(t *testing.T) {
	for _, tc := range []struct{ field, drop string }{
		{"survived_ids", `"survived_ids":["a","b"],`},
		{"declared_kind_rescue", `,"declared_kind_rescue":[]`},
	} {
		t.Run(tc.field, func(t *testing.T) {
			missing := strings.Replace(validRankedCutSummaryLine(), tc.drop, "", 1)
			if missing == validRankedCutSummaryLine() {
				t.Fatalf("fixture bug: needle %q not found in the base line", tc.drop)
			}
			log, err := Parse([]byte(missing))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			want := wantForRankedCutSummary()
			if _, present := want[tc.field]; present {
				t.Fatalf("fixture bug: the dropped field must NOT be named in Want, or this test would pass for the wrong (already-existing) reason")
			}
			_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: want})
			if err == nil {
				t.Fatalf("Certify() accepted a line missing %s, a declared-required field Want never named -- a silently dropped field must never pass unnoticed", tc.field)
			}
		})
	}
}

// Round r1's P2 (CertifyAbsent side): an exactly-one-per-pass event is
// required on EVERY pass by its own declaration, so asserting it absent is
// never a legitimate expectation -- CertifyAbsent must refuse rather than
// silently accepting "0 lines found" as if it proved something.
func TestCertifyAbsentRefusesAnExactlyOnePerPassEvent(t *testing.T) {
	log, err := Parse([]byte(`{"time":"2026-09-10T00:00:00Z","level":"INFO","msg":"an unrelated line"}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	err = certifyAbsentRecovered(t, log, eventspec.RankedCutSummary, map[string]any{"request_id": "req_1"})
	if err == nil {
		t.Fatal("CertifyAbsent() accepted asserting absence for RankedCutSummary, an exactly_one_per_pass event -- want a refusal naming the multiplicity")
	}
	if !strings.Contains(err.Error(), "exactly_one_per_pass") {
		t.Errorf("refusal text = %q, want it to name the multiplicity", err.Error())
	}
}

// Certify's attribution-scoping guard: Want omitting a field the event
// declares in Attribution is refused outright, before any line is even
// located -- without this guard, multiplicity would silently scope over
// the WHOLE supplied log (round r1's P2) instead of the one attempt the
// caller actually means to certify.
func TestCertifyRefusesWantMissingAnAttributionField(t *testing.T) {
	log, err := Parse([]byte(validRankedCutSummaryLine()))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := wantForRankedCutSummary()
	delete(want, "request_id")
	_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: want})
	if err == nil {
		t.Fatal("Certify() accepted a Want with no \"request_id\" (RankedCutSummary's own declared Attribution field) -- want a refusal naming it")
	}
	if !strings.Contains(err.Error(), `"request_id"`) {
		t.Errorf("refusal text = %q, want it to name the missing attribution field", err.Error())
	}
}

// TestCertifyRefusesWantMissingPassForAPassBearingEvent is round r2
// finding 5's own new rule, pinned by name: a pass-bearing event's Want
// MUST include "pass", the same requirement an Attribution field already
// carries -- the caller must state WHICH pass it certifies, not rely on
// "the last line" selection rule alone. Every other test in this package
// happens to always include "pass" in its own Want map, so without this
// dedicated pin the guard's own removal survives a local mutation battery
// silently (caught exactly that way before this pin existed).
func TestCertifyRefusesWantMissingPassForAPassBearingEvent(t *testing.T) {
	log, err := Parse([]byte(validRankedCutSummaryLine()))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := wantForRankedCutSummary()
	delete(want, "pass")
	_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: want})
	if err == nil {
		t.Fatal("Certify() accepted a Want with no \"pass\" for a pass-bearing event -- want a refusal naming it")
	}
	if !strings.Contains(err.Error(), `Want must include "pass"`) {
		t.Errorf("refusal text = %q, want it to name the missing pass requirement", err.Error())
	}
}

// Certify's ExactlyOnePerPass empty-scope guard: zero matching lines for
// the attempt Want names is refused, never a panic and never silently
// certifying nothing. RankedCutSummary is exactly_one_per_pass, so an
// attempt that never reached it must be reported as a refusal, not treated
// as an empty pass to index into.
func TestCertifyRefusesZeroLinesForAnExactlyOnePerPassEvent(t *testing.T) {
	log, err := Parse([]byte(`{"time":"2026-09-10T00:00:00Z","level":"INFO","msg":"an unrelated line","request_id":"req_1"}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()})
	if err == nil {
		t.Fatal("Certify() accepted zero RankedCutSummary lines for this attempt -- want a refusal naming the count")
	}
	if !strings.Contains(err.Error(), "found 0 lines") {
		t.Errorf("refusal text = %q, want it to name the zero count", err.Error())
	}
}

// Certify's ZeroOrOnePerPass duplicate guard: two lines in scope for a
// zero_or_one_per_pass event (AnchorSlotDisplaced) is refused, never
// silently certified against one of them.
func TestCertifyRefusesTwoLinesForAZeroOrOnePerPassEvent(t *testing.T) {
	line := `{"time":"2026-09-10T00:00:00Z","level":"INFO","msg":"context fabric resolution trace: anchor slot displaced",` +
		`"request_id":"req_1","pass":1,"stage":"anchor_slot_displaced","subject_kind":"project","subject_canonical_id":"p1",` +
		`"anchor_slot_reserved":"team","anchor_slot_source":"receipt","anchor_slot_displaced":1,"pool_truncated_n":7}`
	log, err := Parse([]byte(line + "\n" + line))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	_, err = certifyRecovered(t, log, Assertion{
		Event: eventspec.AnchorSlotDisplaced,
		Want: map[string]any{
			"request_id":           "req_1",
			"pass":                 1,
			"anchor_slot_reserved": "team",
			"anchor_slot_source":   "receipt",
			"subject_kind":         "project",
		},
	})
	if err == nil {
		t.Fatal("Certify() accepted two AnchorSlotDisplaced lines sharing the same pass for one attempt -- want a refusal naming the duplicate pass")
	}
	// r3 fix: zero_or_one_per_pass is now pass-keyed like exactly_one_per_pass
	// -- both lines here share pass=1, so the refusal names the duplicate
	// pass, not a raw line count (round r2 finding 1: a raw-count refusal
	// would also wrongly reject two DISTINCT passes, which
	// TestCertifyAcceptsDistinctPassesForAZeroOrOnePerPassEvent below proves
	// must be accepted).
	if !strings.Contains(err.Error(), "duplicate pass number") {
		t.Errorf("refusal text = %q, want it to name the duplicate pass", err.Error())
	}
}

// TestCertifyAcceptsDistinctPassesForAZeroOrOnePerPassEvent is round r2's
// finding 1, reproduced and fixed: TWO AnchorSlotDisplaced lines for the
// SAME request_id, on DISTINCT passes, must be ACCEPTED -- each pass
// legitimately carries its own zero-or-one line; the multiplicity bounds
// each PASS's own count, never the whole request's line count. Before the
// fix, Certify's zero_or_one_per_pass branch refused any second line in
// scope without ever examining pass, wrongly rejecting this.
func TestCertifyAcceptsDistinctPassesForAZeroOrOnePerPassEvent(t *testing.T) {
	pass1 := `{"time":"2026-09-10T00:00:00Z","level":"INFO","msg":"context fabric resolution trace: anchor slot displaced",` +
		`"request_id":"req_1","pass":1,"stage":"anchor_slot_displaced","subject_kind":"project","subject_canonical_id":"p1",` +
		`"anchor_slot_reserved":"team","anchor_slot_source":"receipt","anchor_slot_displaced":1,"pool_truncated_n":7}`
	pass2 := `{"time":"2026-09-10T00:00:01Z","level":"INFO","msg":"context fabric resolution trace: anchor slot displaced",` +
		`"request_id":"req_1","pass":2,"stage":"anchor_slot_displaced","subject_kind":"project","subject_canonical_id":"p2",` +
		`"anchor_slot_reserved":"team","anchor_slot_source":"receipt","anchor_slot_displaced":1,"pool_truncated_n":5}`
	log, err := Parse([]byte(pass1 + "\n" + pass2))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	// The LAST line in scope (pass 2) is the one certified against Want,
	// same "last pass wins" rule every pass-keyed multiplicity now shares.
	if _, err := certifyRecovered(t, log, Assertion{
		Event: eventspec.AnchorSlotDisplaced,
		Want: map[string]any{
			"request_id":           "req_1",
			"pass":                 2,
			"anchor_slot_reserved": "team",
			"anchor_slot_source":   "receipt",
			"subject_kind":         "project",
			"subject_canonical_id": "p2",
		},
	}); err != nil {
		t.Fatalf("Certify() refused two AnchorSlotDisplaced lines on DISTINCT passes for the same request -- want it to accept and certify the last: %v", err)
	}
}

// Round r2's P1: a nested (object_slice) field's own required children were
// never checked at all -- a declared_kind_rescue row missing "kind" must be
// refused, even though no test names "declared_kind_rescue" in Want.
func TestCertifyRefusesANestedObjectMissingARequiredChildField(t *testing.T) {
	// The base fixture's declared_kind_rescue is empty ([]); build a line
	// carrying one row with "kind" missing.
	withRow := strings.Replace(validRankedCutSummaryLine(), `"declared_kind_rescue":[]`,
		`"declared_kind_rescue":[{"state":"ran_matched_survived","terms_queried":1,"matched":1,"survived":1,"reached":1}]`, 1)
	if withRow == validRankedCutSummaryLine() {
		t.Fatal("fixture bug: declared_kind_rescue:[] needle not found")
	}
	log, err := Parse([]byte(withRow))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()})
	if err == nil {
		t.Fatal("Certify() accepted a declared_kind_rescue row missing \"kind\" -- want a refusal naming the nested field")
	}
	if !strings.Contains(err.Error(), `"kind"`) || !strings.Contains(err.Error(), "declared_kind_rescue") {
		t.Errorf("refusal text = %q, want it to name both the missing nested field and its parent array", err.Error())
	}
}

// Round r2's P1: Field.Type was declared but never consulted for a field
// not named in Want -- a string where an int is declared must be refused.
func TestCertifyRefusesAWrongTypeNotNamedInWant(t *testing.T) {
	wrongType := strings.Replace(validRankedCutSummaryLine(), `"pool_truncated_n":72`, `"pool_truncated_n":"not-an-int"`, 1)
	log, err := Parse([]byte(wrongType))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := wantForRankedCutSummary()
	delete(want, "pool_truncated_n")
	_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: want})
	if err == nil {
		t.Fatal("Certify() accepted pool_truncated_n as a string (declared int), not named in Want -- want a refusal naming the type mismatch")
	}
	if !strings.Contains(err.Error(), "declared type=int") {
		t.Errorf("refusal text = %q, want it to name the declared type", err.Error())
	}
}

// Round r2's P2: two lines identical apart from "time" for one attempt must
// be refused as an indistinguishable duplicate emission, never silently
// certified against the last of the two. A genuinely different multi-pass
// pair (TestCertifyAcceptsAGenuineMultiPassTwin) must still be accepted.
func TestCertifyRefusesTwoIdenticalLinesForAnExactlyOnePerPassEvent(t *testing.T) {
	// CHAOS-5516: both lines share pass=1 (validRankedCutSummaryLine()'s own
	// value, unchanged) -- the duplicate-PASS guard now catches this, not a
	// byte-identical-except-time comparison.
	line := validRankedCutSummaryLine()
	dup := strings.Replace(line, `"2026-09-10T00:00:00Z"`, `"2026-09-10T00:00:01Z"`, 1)
	log, err := Parse([]byte(line + "\n" + dup))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()})
	if err == nil {
		t.Fatal("Certify() accepted two lines sharing the same pass number -- want a refusal naming the duplicate")
	}
	if !strings.Contains(err.Error(), "duplicate pass number") {
		t.Errorf("refusal text = %q, want it to name the duplicate pass", err.Error())
	}
}

// CHAOS-5516: the duplicate-pass guard fires even when the two lines'
// OTHER fields genuinely differ -- a same-pass duplicate is a defect
// regardless of content, the whole point of keying on pass rather than
// byte-identity.
func TestCertifyRefusesTwoLinesSharingAPassNumberEvenWithDifferentOtherFields(t *testing.T) {
	line := validRankedCutSummaryLine()
	differentContentSamePass := strings.Replace(line, `"candidate_count":92`, `"candidate_count":2`, 1)
	log, err := Parse([]byte(line + "\n" + differentContentSamePass))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()})
	if err == nil {
		t.Fatal("Certify() accepted two lines sharing pass=1 with different candidate_count -- want a refusal naming the duplicate pass, not a value mismatch")
	}
	if !strings.Contains(err.Error(), "duplicate pass number") {
		t.Errorf("refusal text = %q, want it to name the duplicate pass", err.Error())
	}
}

// Round r2's P2: CertifyAbsent must scope by attribution -- a line for a
// DIFFERENT attempt (request_id) must never block asserting absence for
// the attempt the caller actually means.
func TestCertifyAbsentIsAttributionScoped(t *testing.T) {
	line := `{"time":"2026-09-10T00:00:00Z","level":"INFO","msg":"context fabric resolution trace: anchor slot displaced",` +
		`"request_id":"req_2","pass":1,"stage":"anchor_slot_displaced","subject_kind":"project","subject_canonical_id":"p1",` +
		`"anchor_slot_reserved":"team","anchor_slot_source":"receipt","anchor_slot_displaced":1,"pool_truncated_n":7}`
	log, err := Parse([]byte(line))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if err := certifyAbsentRecovered(t, log, eventspec.AnchorSlotDisplaced, map[string]any{"request_id": "req_1"}); err != nil {
		t.Errorf("CertifyAbsent() refused req_1 absence solely because req_2 has a line -- want it to certify absence for req_1: %v", err)
	}
	if err := certifyAbsentRecovered(t, log, eventspec.AnchorSlotDisplaced, map[string]any{"request_id": "req_2"}); err == nil {
		t.Error("CertifyAbsent() accepted asserting absence for req_2, which DOES have a line -- want a refusal")
	}
}

// Round r2's P2: CertifyAbsent must refuse for any Multiplicity other than
// zero_or_one_per_pass, including an unrecognised value -- not just the
// one case (exactly_one_per_pass) the earlier fix named explicitly.
func TestCertifyAbsentRefusesAnUnrecognisedMultiplicity(t *testing.T) {
	log, err := Parse([]byte(`{"time":"2026-09-10T00:00:00Z","level":"INFO","msg":"an unrelated line"}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	bogus := eventspec.Event{ID: "test.bogus", Msg: "an unrelated line", Multiplicity: eventspec.Multiplicity("made_up"), Attribution: nil}
	if err := certifyAbsentRecovered(t, log, bogus, map[string]any{}); err == nil {
		t.Error("CertifyAbsent() accepted an unrecognised Multiplicity value -- want a refusal")
	}
}

// Round r2's battery (H5): CertifyAbsent must refuse when the caller's own
// attribution map omits a key the event declares in Attribution -- the same
// contract Certify's Want enforces (TestCertifyRefusesWantMissingAnAttributionField).
// Without this, an incomplete attribution map would scope against zero
// fields and silently certify absence over the WHOLE log, not one attempt.
func TestCertifyAbsentRefusesAnAttributionMapMissingAnAttributionField(t *testing.T) {
	log, err := Parse([]byte(`{"time":"2026-09-10T00:00:00Z","level":"INFO","msg":"context fabric resolution trace: anchor slot displaced","request_id":"req_1"}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if err := certifyAbsentRecovered(t, log, eventspec.AnchorSlotDisplaced, map[string]any{}); err == nil {
		t.Fatal("CertifyAbsent() accepted an attribution map missing \"request_id\" -- want a refusal naming the missing attribution field")
	} else if !strings.Contains(err.Error(), "request_id") {
		t.Errorf("refusal text = %q, want it to name the missing attribution field", err.Error())
	}
}

// Round r2's battery (G6): a closed-vocabulary field NOT named in Want must
// still be checked against its declared vocabulary -- the same "unconditional,
// not just for Want fields" contract TestCertifyRefusesAWrongTypeNotNamedInWant
// pins for Field.Type.
func TestCertifyRefusesABadVocabNotNamedInWant(t *testing.T) {
	badVocab := strings.Replace(validRankedCutSummaryLine(), `"anchor_slot_source":"receipt"`, `"anchor_slot_source":"made_up_source"`, 1)
	if badVocab == validRankedCutSummaryLine() {
		t.Fatal("fixture bug: anchor_slot_source needle not found")
	}
	log, err := Parse([]byte(badVocab))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := wantForRankedCutSummary()
	delete(want, "anchor_slot_source")
	_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: want})
	if err == nil {
		t.Fatal("Certify() accepted anchor_slot_source=\"made_up_source\" (not named in Want) -- want a refusal naming the closed vocabulary")
	}
	if !strings.Contains(err.Error(), "closed vocabulary") {
		t.Errorf("refusal text = %q, want it to name the closed vocabulary", err.Error())
	}
}

// Round r3's P1: JSON has no integer type, so a genuinely fractional value
// decodes to the same float64 Go shape as a legitimate integer -- only
// checking `_, ok := got.(float64)` let a real slog line with
// candidate_count=92.5 certify as declared type=int. Reproduced against a
// real slog.JSONHandler-shaped line (not a hand-typed struct).
func TestCertifyRefusesAFractionalValueForADeclaredIntField(t *testing.T) {
	fractional := strings.Replace(validRankedCutSummaryLine(), `"candidate_count":92`, `"candidate_count":92.5`, 1)
	if fractional == validRankedCutSummaryLine() {
		t.Fatal("fixture bug: candidate_count needle not found")
	}
	log, err := Parse([]byte(fractional))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := wantForRankedCutSummary()
	delete(want, "candidate_count")
	_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: want})
	if err == nil {
		t.Fatal("Certify() accepted candidate_count=92.5 (fractional) for a declared int field -- want a refusal")
	}
	if !strings.Contains(err.Error(), "not a whole number") {
		t.Errorf("refusal text = %q, want it to say the value is not a whole number", err.Error())
	}
}

// Round r3's P2: Certify/CertifyAbsent must refuse a caller-supplied Event
// that is not byte-equal to its canonical eventspec.All declaration -- Go
// exports the Event type and every field, so nothing previously stopped a
// caller from constructing a degenerate copy (Attribution and Fields both
// stripped) that certifies ANY line matching just the msg string, with zero
// validation performed. Reproduced with an empty Want against a
// canonical-shaped Event with both slices nilled out.
func TestCertifyRefusesAnEventNotByteEqualToItsCanonicalDeclaration(t *testing.T) {
	weakened := eventspec.Event{
		ID:           eventspec.RankedCutSummary.ID,
		Msg:          eventspec.RankedCutSummary.Msg,
		Level:        eventspec.RankedCutSummary.Level,
		Multiplicity: eventspec.RankedCutSummary.Multiplicity,
		Attribution:  nil,
		Fields:       nil,
	}
	log, err := Parse([]byte(`{"time":"2026-09-10T00:00:00Z","level":"INFO","msg":"context fabric resolution trace: ranked cut summary","garbage":"anything"}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if _, err := certifyRecovered(t, log, Assertion{Event: weakened, Want: map[string]any{}}); err == nil {
		t.Fatal("Certify() accepted a caller-supplied Event with Attribution+Fields stripped -- want a refusal naming the canonical mismatch")
	} else if !strings.Contains(err.Error(), "canonical") {
		t.Errorf("refusal text = %q, want it to name the canonical-declaration mismatch", err.Error())
	}
}

// Round r3's P2: an Event ID with no entry in eventspec.All at all (not just
// a mismatched one) must be refused by name, not silently certify against
// whatever the caller supplied.
func TestCertifyRefusesAnUnknownEventID(t *testing.T) {
	invented := eventspec.Event{
		ID:           "not.a.real.event",
		Msg:          "this message does not exist in production",
		Level:        eventspec.LevelInfo,
		Multiplicity: eventspec.MultiplicityZeroOrOnePerPass,
	}
	log, err := Parse([]byte(`{"time":"2026-09-10T00:00:00Z","level":"INFO","msg":"this message does not exist in production"}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if _, err := certifyRecovered(t, log, Assertion{Event: invented, Want: map[string]any{}}); err == nil {
		t.Fatal("Certify() accepted an Event ID with no entry in eventspec.All -- want a refusal")
	} else if !strings.Contains(err.Error(), "not a declared event ID") {
		t.Errorf("refusal text = %q, want it to name the unknown event ID", err.Error())
	}
}

// Round r3's P2 (second part): Certify(nil, ...) and certifyAbsentRecovered(t, nil, ...)
// must return an error, never panic -- a nil *Log is a caller mistake this
// package should name, not crash on.
func TestCertifyAndCertifyAbsentRefuseANilLog(t *testing.T) {
	// Deliberately NOT certifyRecovered/certifyAbsentRecovered here: those
	// helpers convert a panic into a returned error, which would make THIS
	// specific pin blind to its own guard being removed -- if the explicit
	// "log == nil" check is mutated away, execution falls through to a real
	// nil-pointer panic, and a recovering helper would silently relabel
	// that panic as "the function returned an error", passing regardless
	// of whether the graceful check ever ran. Each call gets its own local
	// recover that FAILS the test on a panic, so "returned an error" and
	// "panicked" are kept as two different, distinguishable outcomes.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Certify(nil, ...) panicked instead of returning an error: %v", r)
			}
		}()
		if _, err := Certify(nil, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()}); err == nil {
			t.Error("Certify(nil, ...) returned no error -- want a refusal naming the nil log")
		}
	}()
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("CertifyAbsent(nil, ...) panicked instead of returning an error: %v", r)
			}
		}()
		if err := CertifyAbsent(nil, eventspec.AnchorSlotDisplaced, map[string]any{"request_id": "req_1"}); err == nil {
			t.Error("CertifyAbsent(nil, ...) returned no error -- want a refusal naming the nil log")
		}
	}()
}

// TestEveryEventsMultiplicityAgreesWithWhetherItDeclaresAPassField is round
// r2 finding 5's own executable consistency check: DecisionSummary used to
// declare MultiplicityExactlyOnePerPass while having NO pass field at all
// (once per REQUEST, not once per pass) -- a real inconsistency between the
// declared label and the declared shape. Walks eventspec.All generically
// (not hand-picked) so a future event with the same mismatch is caught the
// day it is declared, not the day a review round finds it.
func TestEveryEventsMultiplicityAgreesWithWhetherItDeclaresAPassField(t *testing.T) {
	for _, ev := range eventspec.All {
		requiresPass, ok := multiplicityRequiresPassField(ev.Multiplicity)
		if !ok {
			t.Errorf("%s: declares unrecognised multiplicity %q", ev.ID, ev.Multiplicity)
			continue
		}
		hasPass := eventHasPassField(ev.Fields)
		if requiresPass != hasPass {
			t.Errorf("%s: declares multiplicity=%q (requires pass field=%v) but its own Fields declare pass field=%v -- the declaration is inconsistent",
				ev.ID, ev.Multiplicity, requiresPass, hasPass)
		}
	}
}
