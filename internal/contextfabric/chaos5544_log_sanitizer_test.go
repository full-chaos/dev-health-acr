package contextfabric

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestRequestIDLogAttrsRoutesThroughSanitizeLogAttr is the CHAOS-5544
// real-handler pin for `requestIDLogAttrs` (alerts 61/68's sink,
// telemetry.go): a VALID request id (the only shape observable.WithRequestID
// will ever store -- it silently rejects anything not `req_`+32 lowercase
// hex, so a CRLF/ANSI/over-length/malformed id can never reach this
// function through the public context API at all) round-trips unchanged
// through the sanitizer, proving the wiring without inventing an
// unreachable "attack succeeded" scenario. The malformed-input axis for
// this exact sink is asserted at the unit level instead --
// TestSanitizeLogAttrInputDomainTable (genkitruntime package) pins
// SanitizeLogAttr itself over the full shape axis, and this function has no
// implementation of its own left to diverge from it.
func TestRequestIDLogAttrsRoutesThroughSanitizeLogAttr(t *testing.T) {
	t.Parallel()
	const validID = "req_0123456789abcdef0123456789abcdef"
	ctx := observability.WithRequestID(context.Background(), validID)
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordPriorSubjectReceiptsSkipped(
			ctx, storage.Principal{OrgID: "org_sink_test"}, 3)
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want exactly 1", len(records))
	}
	got, _ := records[0]["request_id"].(string)
	if got != validID {
		t.Fatalf("request_id = %q, want %q unchanged (a valid id must round-trip exactly)", got, validID)
	}
}

// TestSanitizeLogAttrIsTheOnlyImplementationLeft: a grep-shaped guard, not a
// behavioural one -- requestIDLogAttrs must call SanitizeLogAttr and must
// NOT carry a second, locally re-implemented sanitizer that could drift
// from it. Reading the source directly here (rather than trusting a code
// review to keep noticing) is deliberate: CHAOS-5544 exists because a
// SECOND sanitizer (genkitruntime's old safeLogRequestID) already drifted
// out of sync with this one, silently, for weeks.
func TestSanitizeLogAttrIsTheOnlyImplementationLeft(t *testing.T) {
	t.Parallel()
	const validID = "req_0123456789abcdef0123456789abcdef"
	ctx := observability.WithRequestID(context.Background(), validID)
	attrs := requestIDLogAttrs(ctx)
	if len(attrs) != 2 || attrs[0] != "request_id" {
		t.Fatalf("requestIDLogAttrs(%q) = %v, want [\"request_id\", ...]", validID, attrs)
	}
	if got := attrs[1].(string); got != SanitizeLogAttr(validID) {
		t.Fatalf("requestIDLogAttrs value = %q, want SanitizeLogAttr's own output %q -- a second implementation has drifted", got, SanitizeLogAttr(validID))
	}
}

// TestSanitizeLogAttrUsesTheRecognizedReplacerShape is a SOURCE-SHAPE pin,
// not a behavioural one, and deliberately so: CHAOS-5544's whole reason for
// existing is that the OLD sanitizer (a rune-remap loop only) was already
// behaviourally complete -- it neutralized every dangerous byte at
// runtime -- and CodeQL's go/log-injection query still flagged it, because
// a hand-rolled loop is not a shape its dataflow model recognizes as a
// barrier. The rune allowlist in SanitizeLogAttr is, by itself, ALSO
// behaviourally complete (it independently catches \n and \r, since both
// are < 0x20) -- so a mutation that neuters ONLY the strings.NewReplacer
// pass is an EQUIVALENT MUTANT from a pure input/output standpoint: no
// runtime table can distinguish "the recognized shape is present" from
// "it is absent but the allowlist still catches everything," because both
// produce byte-identical output. Recognition is a property of the SOURCE,
// not of any output, so this pin reads the source directly -- the same
// justification the codebase's own AST-shaped pins elsewhere in this repo
// use when a property is structural rather than behavioural.
//
// CHAOS-5558 RELOCATED the actual implementation to internal/logsanitize
// (internal/auth needed the same barrier and cannot import this package --
// a real cycle, see chaos5544_log_sanitizer.go's own doc comment) -- this
// pin follows it there rather than reading this package's now-thin
// re-export, which no longer contains the NewReplacer call at all.
//
// r1 review round (CHAOS-5558) found this pin only ever grepped the WHOLE
// FILE for the substring, never confirmed the replacer is actually
// referenced INSIDE SanitizeLogAttr's own body -- proven by an executed
// mutation: SanitizeLogAttr rewritten to `return s` (a complete no-op),
// with logAttrForgeryReplacer's declaration left dead and unused in the
// same file, still passed this test. Scoped now to the SanitizeLogAttr
// function body specifically (isolated by its own `func ... {` / closing
// `}` at column 0, the gofmt-guaranteed shape of a top-level declaration),
// so a dead reference elsewhere in the file can no longer satisfy it.
func TestSanitizeLogAttrUsesTheRecognizedReplacerShape(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("../logsanitize/logsanitize.go")
	if err != nil {
		t.Fatalf("could not read ../logsanitize/logsanitize.go: %v", err)
	}
	text := string(src)
	const marker = "func SanitizeLogAttr(s string) string {"
	start := strings.Index(text, marker)
	if start == -1 {
		t.Fatal("could not find `func SanitizeLogAttr(s string) string {` in logsanitize.go -- has the signature changed?")
	}
	body := text[start:]
	end := strings.Index(body, "\n}")
	if end == -1 {
		t.Fatal("could not find SanitizeLogAttr's closing brace (a line starting with `}`) in logsanitize.go")
	}
	body = body[:end]
	// The real shape is a package-level `var logAttrForgeryReplacer =
	// strings.NewReplacer(...)`, called by NAME inside the function body
	// (`logAttrForgeryReplacer.Replace(s)`) -- so the body itself never
	// contains the literal text "strings.NewReplacer(". Two independent
	// checks, not one substring test on the whole file: the body must
	// actually CALL the replacer (defeats a `return s` no-op regardless of
	// what else is dead in the file), and the file must declare that exact
	// name via `var ... = strings.NewReplacer(...)` naming both \n and \r
	// (defeats a same-named decoy that calls something else).
	if !strings.Contains(body, "logAttrForgeryReplacer.Replace(") {
		t.Fatal("SanitizeLogAttr's OWN body must call logAttrForgeryReplacer.Replace(...) -- a mutation that stops calling it (even a bare `return s`) must fail this test, not merely leave the declaration dead elsewhere in the file")
	}
	const declMarker = "var logAttrForgeryReplacer = strings.NewReplacer("
	declStart := strings.Index(text, declMarker)
	if declStart == -1 {
		t.Fatal("could not find `var logAttrForgeryReplacer = strings.NewReplacer(...)` in logsanitize.go -- the go/log-injection query's own documented recognized barrier shape")
	}
	declLineEnd := strings.Index(text[declStart:], "\n")
	declLine := text[declStart : declStart+declLineEnd]
	if !strings.Contains(declLine, `"\n"`) || !strings.Contains(declLine, `"\r"`) {
		t.Fatal("the NewReplacer call must name both \\n and \\r explicitly -- CWE-117's own two forgery characters")
	}
}
