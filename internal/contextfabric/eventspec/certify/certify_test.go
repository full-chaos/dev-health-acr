package certify

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

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
	if _, err := Certify(log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()}); err != nil {
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
	_, err = Certify(log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()})
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
	if _, err := Certify(log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()}); err != nil {
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
	_, err = Certify(log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()})
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
	_, err = Certify(log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()})
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
	_, err = Certify(log, Assertion{Event: eventspec.RankedCutSummary, Want: want})
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
	_, err = Certify(log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()})
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
			_, err = Certify(log, Assertion{Event: eventspec.RankedCutSummary, Want: want})
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
	err = CertifyAbsent(log, eventspec.RankedCutSummary)
	if err == nil {
		t.Fatal("CertifyAbsent() accepted asserting absence for RankedCutSummary, an exactly_one_per_pass event -- want a refusal naming the multiplicity")
	}
	if !strings.Contains(err.Error(), "exactly_one_per_pass") {
		t.Errorf("refusal text = %q, want it to name the multiplicity", err.Error())
	}
}
