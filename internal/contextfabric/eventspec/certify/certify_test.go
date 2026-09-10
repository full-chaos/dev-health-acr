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

// A2 control (b): a wrong record -- a twin line carrying the SAME msg with
// DIFFERENT values. Certify must refuse on multiplicity, not silently pick
// one of the two and pass.
func TestCertifyRefusesATwinLineWithTheSameMsgAndDifferentValues(t *testing.T) {
	twin := strings.Replace(validRankedCutSummaryLine(), `"anchor_slot_reserved":"team"`, `"anchor_slot_reserved":"repo"`, 1)
	log, err := Parse([]byte(validRankedCutSummaryLine() + "\n" + twin))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	_, err = Certify(log, Assertion{Event: eventspec.RankedCutSummary, Want: wantForRankedCutSummary()})
	if err == nil {
		t.Fatal("Certify() accepted a log with two lines sharing the summary msg but disagreeing values -- want a refusal naming the ambiguity")
	}
	if !strings.Contains(err.Error(), "found 2 lines") {
		t.Errorf("refusal text = %q, want it to name the count", err.Error())
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
