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

// wantForRankedCutSummary is one internally-consistent, independently
// constructed fixture value set for eventspec.RankedCutSummary -- used by
// every test below so the controls differ from the passing case by exactly
// the one thing each is proving Certify refuses.
func wantForRankedCutSummary() map[string]any {
	return map[string]any{
		"request_id":            "req_1",
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
		`"request_id":"req_1","stage":"ranked_cut","candidate_count":92,"survived_count":20,"survived_ids":["a","b"],"max":20,` +
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
	twin := strings.Replace(validRankedCutSummaryLine(), `"anchor_slot_reserved":"team"`, `"anchor_slot_reserved":"repo"`, 1)
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
	firstPass := strings.Replace(validRankedCutSummaryLine(), `"candidate_count":92`, `"candidate_count":2`, 1)
	log, err := Parse([]byte(firstPass + "\n" + validRankedCutSummaryLine()))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if _, err := certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()}); err != nil {
		t.Fatalf("Certify() refused a genuine multi-pass log (first pass candidate_count=2, kept pass candidate_count=92, same request_id) -- want it to certify the LAST line: %v", err)
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
	err = CertifyAbsent(log, eventspec.RankedCutSummary, map[string]any{"request_id": "req_1"})
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
		`"request_id":"req_1","stage":"anchor_slot_displaced","subject_kind":"project","subject_canonical_id":"p1",` +
		`"anchor_slot_reserved":"team","anchor_slot_source":"receipt","anchor_slot_displaced":1,"pool_truncated_n":7}`
	log, err := Parse([]byte(line + "\n" + line))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	_, err = certifyRecovered(t, log, Assertion{
		Event: eventspec.AnchorSlotDisplaced,
		Want: map[string]any{
			"request_id":           "req_1",
			"anchor_slot_reserved": "team",
			"anchor_slot_source":   "receipt",
			"subject_kind":         "project",
		},
	})
	if err == nil {
		t.Fatal("Certify() accepted two AnchorSlotDisplaced lines for one attempt -- want a refusal naming the count")
	}
	if !strings.Contains(err.Error(), "found 2 lines") {
		t.Errorf("refusal text = %q, want it to name the count", err.Error())
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
	line := validRankedCutSummaryLine()
	dup := strings.Replace(line, `"2026-09-10T00:00:00Z"`, `"2026-09-10T00:00:01Z"`, 1)
	log, err := Parse([]byte(line + "\n" + dup))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	_, err = certifyRecovered(t, log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()})
	if err == nil {
		t.Fatal("Certify() accepted two lines identical apart from time -- want a refusal naming the duplicate")
	}
	if !strings.Contains(err.Error(), "IDENTICAL") {
		t.Errorf("refusal text = %q, want it to name the duplicate", err.Error())
	}
}

// Round r2's P2: CertifyAbsent must scope by attribution -- a line for a
// DIFFERENT attempt (request_id) must never block asserting absence for
// the attempt the caller actually means.
func TestCertifyAbsentIsAttributionScoped(t *testing.T) {
	line := `{"time":"2026-09-10T00:00:00Z","level":"INFO","msg":"context fabric resolution trace: anchor slot displaced",` +
		`"request_id":"req_2","stage":"anchor_slot_displaced","subject_kind":"project","subject_canonical_id":"p1",` +
		`"anchor_slot_reserved":"team","anchor_slot_source":"receipt","anchor_slot_displaced":1,"pool_truncated_n":7}`
	log, err := Parse([]byte(line))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if err := CertifyAbsent(log, eventspec.AnchorSlotDisplaced, map[string]any{"request_id": "req_1"}); err != nil {
		t.Errorf("CertifyAbsent() refused req_1 absence solely because req_2 has a line -- want it to certify absence for req_1: %v", err)
	}
	if err := CertifyAbsent(log, eventspec.AnchorSlotDisplaced, map[string]any{"request_id": "req_2"}); err == nil {
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
	if err := CertifyAbsent(log, bogus, map[string]any{}); err == nil {
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
	if err := CertifyAbsent(log, eventspec.AnchorSlotDisplaced, map[string]any{}); err == nil {
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
