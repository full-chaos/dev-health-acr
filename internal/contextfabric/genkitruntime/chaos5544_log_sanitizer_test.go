package genkitruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// lastJSONLine returns the last well-formed JSON object logged to buf,
// regardless of operation -- the generic counterpart to decisionLine (which
// is scoped to synthesize only) for tests that exercise all three
// operations.
func lastJSONLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	fields := map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		candidate := map[string]any{}
		if json.Unmarshal(line, &candidate) == nil {
			fields = candidate
		}
	}
	if len(fields) == 0 {
		t.Fatalf("no decision line was emitted; log = %s", buf.String())
	}
	return fields
}

// forgeShapes is the CHAOS-5544 input-domain axis, GENERATED once and
// reused across every site: clean, CRLF forgery, an ANSI/control-byte
// escape, an over-length id, an empty id, and a unicode id -- team-lead's
// named axis, verbatim.
var forgeShapes = []struct {
	name string
	in   string
}{
	{"clean", "req_clean0000000000000000000000"},
	{"crlf", "req_a\r\n{\"level\":\"INFO\",\"msg\":\"forged\",\"outcome\":\"success\"}"},
	{"ansi", "req_a\x1b[31;1mFORGED\x1b[0mb"},
	{"over_length", "req_" + strings.Repeat("a", 400)},
	{"empty", ""},
	{"unicode", "req_aé中\U0001F600b"},
}

// assertNoLineBreakSurvives is the ONE shared assertion every real-handler
// forge test in this file applies: whatever the input shape, the EMITTED
// request_id field never carries \r or \n (the two characters that split
// one JSON log line into two, or overwrite one in a naive renderer), and
// exactly one decision line exists in the captured log -- never a second,
// forged one.
func assertNoLineBreakSurvives(t *testing.T, buf *bytes.Buffer, got string) {
	t.Helper()
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("request_id = %q still carries a line break -- a caller can forge log lines", got)
	}
	lines := 0
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var candidate map[string]any
		if json.Unmarshal(line, &candidate) == nil {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("emitted %d JSON log lines, want exactly 1 -- more means the request id forged one; log = %s", lines, buf.String())
	}
}

// TestInterpretQuestionSanitizesRequestIDAcrossTheInputDomain is the
// CHAOS-5544 real-handler pin for InterpretQuestion's decision line (the
// site logInterpretDecision's `request_id` field feeds), GENERATED over
// forgeShapes rather than one hand-picked case.
//
// "empty" and "over_length" are REJECTED by InvestigationRequest's OWN v1
// contract bound (a request_id/question length check, unrelated to
// log-injection) before the model is ever called -- a different, older
// guard than this ticket's concern. The C3 defer-hoisting (CHAOS-5380)
// guarantees a decision line is STILL emitted on that early-rejection path
// (operation="" , an empty attempt list), so this test does not require
// success; it asserts log safety on whatever line -- if any -- the call
// actually produced, tolerating (never requiring) that unrelated rejection.
func TestInterpretQuestionSanitizesRequestIDAcrossTheInputDomain(t *testing.T) {
	t.Parallel()
	for _, tc := range forgeShapes {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			runtime := mustRuntime(t, &generatorStub{interpretation: validInterpretationOutput()}, Config{Logger: logger})
			request := validRequest()
			request.RequestID = tc.in
			_, _, _ = runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, request)
			if buf.Len() == 0 {
				t.Fatalf("no decision line was emitted at all (C3 guarantees one even on early rejection)")
			}
			fields := lastJSONLine(t, &buf)
			got, _ := fields["request_id"].(string)
			assertNoLineBreakSurvives(t, &buf, got)
		})
	}
}

// TestSynthesizeAnswerSanitizesRequestIDAcrossTheInputDomain: as above, for
// SynthesizeAnswer / logSynthesizeDecision. TestRequestIDCannotForgeALogLine
// (chaos4522_rejection_reason_test.go) is the original hand-authored CRLF
// case this generated table subsumes; kept standalone since it also pins
// the "keeps its correlation prefix" replace-not-drop property this table
// does not re-assert per shape.
func TestSynthesizeAnswerSanitizesRequestIDAcrossTheInputDomain(t *testing.T) {
	t.Parallel()
	for _, tc := range forgeShapes {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			runtime := mustRuntime(t, &generatorStub{synthesis: validSynthesisOutput()}, Config{Logger: logger})
			input := validSynthesisInput()
			input.Request.RequestID = tc.in
			if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, input); err != nil {
				t.Fatalf("SynthesizeAnswer() error = %v, want success", err)
			}
			fields := lastJSONLine(t, &buf)
			got, _ := fields["request_id"].(string)
			assertNoLineBreakSurvives(t, &buf, got)
		})
	}
}

// TestPhraseStructureOffersSanitizesRequestIDAcrossTheInputDomain: as
// above, for PhraseStructureOffers / logPhraseDecision -- the third and
// last of runtime.go's decision-log sinks, completing the site coverage
// CHAOS-5544 requires (every site, not just the one codex's #76 named).
func TestPhraseStructureOffersSanitizesRequestIDAcrossTheInputDomain(t *testing.T) {
	t.Parallel()
	for _, tc := range forgeShapes {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			gen := &generatorStub{phrasing: phrasingOutput{Phrasings: []phrasingEntryOutput{{OptionID: "opt_pr", Phrasing: "an open pull request"}}}}
			runtime := mustRuntime(t, gen, Config{Logger: logger})
			input := validPhrasingInput()
			input.RequestID = tc.in
			if _, _, err := runtime.PhraseStructureOffers(context.Background(), storage.Principal{OrgID: "org_1"}, input); err != nil {
				t.Fatalf("PhraseStructureOffers() error = %v, want success", err)
			}
			fields := lastJSONLine(t, &buf)
			got, _ := fields["request_id"].(string)
			assertNoLineBreakSurvives(t, &buf, got)
		})
	}
}

// TestSanitizeLogAttrInputDomainTable is the unit-level counterpart to the
// three real-handler tables above: the exact CHAOS-5544 axis
// (clean/CRLF/ANSI/over-length/empty/unicode), asserting SanitizeLogAttr's
// own output shape directly, so a table failure here localizes to the
// shared helper rather than to any one call site.
func TestSanitizeLogAttrInputDomainTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in, want string }{
		{"clean", "req_clean0000000000000000000000", "req_clean0000000000000000000000"},
		{"crlf", "req_a\r\nFORGED", "req_a??FORGED"},
		{"ansi", "req_a\x1b[31mb", "req_a?[31mb"},
		{"over_length", strings.Repeat("a", 300), strings.Repeat("a", 256)},
		{"empty", "", ""},
		{"unicode", "req_aéb", "req_a?b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := contextfabric.SanitizeLogAttr(tc.in); got != tc.want {
				t.Fatalf("SanitizeLogAttr(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
