package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestSlogEngineTelemetry_RecordAnswerReuseLogsOrgAndOutcome(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buf, nil)))

	telemetry.RecordAnswerReuse(context.Background(), storage.Principal{OrgID: "org_telemetry_1"}, true)

	line := buf.String()
	for _, want := range []string{"org_id=org_telemetry_1", "reused=true"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line = %q, want it to contain %q", line, want)
		}
	}
	// Content-safety: the log line must never carry question text,
	// subject labels, canonical IDs, or result IDs (EngineTelemetry's
	// doc comment) -- there is nothing of that shape passed in, so this
	// is really asserting the implementation never grows a parameter
	// that could leak one in later without a test noticing the shape
	// changed.
	if strings.Contains(line, "question") || strings.Contains(line, "result_id") {
		t.Errorf("log line = %q, unexpectedly contains content-bearing keys", line)
	}
}

func TestSlogEngineTelemetry_RecordAnswerReuseLogsFalseOutcome(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buf, nil)))

	telemetry.RecordAnswerReuse(context.Background(), storage.Principal{OrgID: "org_telemetry_2"}, false)

	if !strings.Contains(buf.String(), "reused=false") {
		t.Errorf("log line = %q, want reused=false", buf.String())
	}
}

func TestSlogEngineTelemetry_RecordPriorSubjectReceiptsSkippedOnlyLogsWhenPositive(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buf, nil)))

	telemetry.RecordPriorSubjectReceiptsSkipped(context.Background(), storage.Principal{OrgID: "org_telemetry_3"}, 0)
	if buf.Len() != 0 {
		t.Fatalf("expected no log line for a zero skip count, got %q", buf.String())
	}

	telemetry.RecordPriorSubjectReceiptsSkipped(context.Background(), storage.Principal{OrgID: "org_telemetry_3"}, 2)
	if !strings.Contains(buf.String(), "skipped_count=2") {
		t.Errorf("log line = %q, want skipped_count=2", buf.String())
	}
}

func TestNewSlogEngineTelemetry_NilLoggerFallsBackToDefault(t *testing.T) {
	t.Parallel()
	// Must not panic.
	telemetry := NewSlogEngineTelemetry(nil)
	telemetry.RecordAnswerReuse(context.Background(), storage.Principal{OrgID: "org_telemetry_4"}, true)
}
