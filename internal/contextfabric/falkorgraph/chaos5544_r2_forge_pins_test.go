package falkorgraph

// r2 CLASS pin: an r2 review round found graphRequestIDLogAttrs (and five
// direct request-id/org-id sites this file shares the shape with) logging
// raw, the same pre-fix shape telemetry.go's requestIDLogAttrs had before
// CHAOS-5544's first commit -- mechanically fixed by the whole-tree
// instrument (chaos5544_sanitizer_instrument_test.go, internal/contextfabric
// package).
//
// This wiring pin is intentionally NOT a forgery table: observability.
// WithRequestID silently rejects anything outside req_+32-hex before it
// ever reaches a context value (verified directly in
// internal/observability's own tests, and requestIDKey is unexported --
// no test outside that package can construct a malformed context value),
// so a malformed id can never reach graphRequestIDLogAttrs through the
// public API, exactly the reachability argument telemetry.go's own wiring
// pin already documents. THE INSTRUMENT is the real mutation-proof gate
// for this class of unreachable-forgery site: it is a static, source-level
// check that does not care whether a malformed value is runtime-reachable,
// so reverting graphRequestIDLogAttrs's SanitizeLogAttr call fails
// TestNoUnsanitizedLogAttributeInContextFabric immediately, behavioral
// reachability aside. This test proves only that a VALID id still
// round-trips through the real production call chain unchanged.
import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/observability"
)

func TestRecordCohortExactNameCensusGateRoutesRequestIDThroughSanitizeLogAttr(t *testing.T) {
	t.Parallel()
	const validID = "req_0123456789abcdef0123456789abcdef"
	ctx := observability.WithRequestID(context.Background(), validID)

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	SlogTelemetry{Logger: logger}.RecordCohortExactNameCensusGate(ctx, "org_1", true, CohortExactNameCensusBasisDiscoveredKind)

	var fields map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &fields); err != nil {
		t.Fatalf("emitted line is not valid JSON: %v; raw=%q", err, buf.String())
	}
	got, _ := fields["request_id"].(string)
	if got != validID {
		t.Fatalf("request_id = %q, want %q unchanged (a valid id must round-trip exactly)", got, validID)
	}
}
