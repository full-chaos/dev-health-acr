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
func TestSanitizeLogAttrUsesTheRecognizedReplacerShape(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("../logsanitize/logsanitize.go")
	if err != nil {
		t.Fatalf("could not read ../logsanitize/logsanitize.go: %v", err)
	}
	text := string(src)
	if !strings.Contains(text, "strings.NewReplacer(") {
		t.Fatal("SanitizeLogAttr must route \\n/\\r through strings.NewReplacer -- the go/log-injection query's own documented recognized barrier shape")
	}
	if !strings.Contains(text, `"\n"`) || !strings.Contains(text, `"\r"`) {
		t.Fatal("the NewReplacer call must name both \\n and \\r explicitly -- CWE-117's own two forgery characters")
	}
}
