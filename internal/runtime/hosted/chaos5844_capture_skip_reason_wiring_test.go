package hosted

// CHAOS-5844 hosted-wiring pin: capture_skip_reason must reach the REAL
// deployed sink, not just contextfabric's own package-level pins against
// contextfabric.NewSlogEngineTelemetry called directly. contextFabricEngineTelemetry
// is the one function open.go's real composition path calls for
// EngineDependencies.Telemetry -- a mutation there (e.g. the nil-override
// fallthrough breaking) would leave every contextfabric-package pin green
// while the deployed service silently dropped the key.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestContextFabricEngineTelemetryEmitsCaptureSkipReason(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	telemetry := contextFabricEngineTelemetry(Options{Logger: logger})

	reasons := []contextfabric.CaptureSkipReason{
		contextfabric.CaptureSkipReasonFrameGateRefused,
		contextfabric.CaptureSkipReasonWindowVetoed,
		contextfabric.CaptureSkipReasonWindowConfirmationRequired,
		contextfabric.CaptureSkipReasonStructureVetoed,
		contextfabric.CaptureSkipReasonReuseValidationError,
		contextfabric.CaptureSkipReasonReuseBudgetRefused,
		contextfabric.CaptureSkipReasonReuseServed,
		contextfabric.CaptureSkipReasonInterpretationFailed,
		contextfabric.CaptureSkipReasonInterpretedTimeUnanswerable,
		contextfabric.CaptureSkipReasonContinuationRefused,
		contextfabric.CaptureSkipReasonWindowAxisConflict,
	}
	for _, reason := range reasons {
		telemetry.RecordConfirmedNeedLedger(context.Background(), storage.Principal{OrgID: "org_hosted_wiring"}, contextfabric.ConfirmedNeedLedgerEvent{CaptureSkipReason: reason})
	}

	lines := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n"))
	if len(lines) != len(reasons) {
		t.Fatalf("captured %d lines, want %d (one per reason)", len(lines), len(reasons))
	}
	for i, raw := range lines {
		var rec map[string]any
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatalf("line %d is not JSON: %v -- line: %s", i, err, raw)
		}
		if rec["msg"] != "context fabric confirmed need ledger" {
			t.Fatalf("line %d msg = %v, want the confirmed need ledger line", i, rec["msg"])
		}
		if level, _ := rec["level"].(string); level != "INFO" {
			t.Fatalf("line %d level = %q, want INFO -- the reason must survive the production log level", i, level)
		}
		if got, _ := rec["capture_skip_reason"].(string); got != string(reasons[i]) {
			t.Fatalf("line %d capture_skip_reason = %q, want %q -- through the real deployed composition, not a direct NewSlogEngineTelemetry construction", i, got, reasons[i])
		}
	}
}
