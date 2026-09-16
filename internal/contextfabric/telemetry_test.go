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

	telemetry.RecordAnswerReuse(context.Background(), storage.Principal{OrgID: "org_telemetry_1"}, AnswerReuseHit, "ownership-routing.v1")

	line := buf.String()
	for _, want := range []string{"org_id=org_telemetry_1", "outcome=hit", "ownership_routing_version=ownership-routing.v1"} {
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

func TestSlogEngineTelemetry_RecordAnswerReuseLogsMissReasons(t *testing.T) {
	t.Parallel()
	for _, outcome := range []AnswerReuseOutcome{AnswerReuseMissNoCandidate, AnswerReuseMissAuthorization, AnswerReuseMissEvidenceContainment, AnswerReuseMissGraphNotProjected} {
		var buf bytes.Buffer
		telemetry := NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&buf, nil)))

		telemetry.RecordAnswerReuse(context.Background(), storage.Principal{OrgID: "org_telemetry_2"}, outcome, "ownership-routing.v1")

		want := "outcome=" + string(outcome)
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log line = %q, want it to contain %q", buf.String(), want)
		}
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
	telemetry.RecordAnswerReuse(context.Background(), storage.Principal{OrgID: "org_telemetry_4"}, AnswerReuseHit, "ownership-routing.v1")
}

// TestRecordReuseOutcomeCarriesTheDeploymentCurrentOwnershipRoutingVersion
// pins recordReuseOutcome's own wiring: EVERY reuse outcome it records --
// hit or miss, whatever the outcome -- carries the same
// e.reuseVersionAuthorities.OwnershipRoutingVersion value the Engine was
// composed with, never a hardcoded or omitted one.
func TestRecordReuseOutcomeCarriesTheDeploymentCurrentOwnershipRoutingVersion(t *testing.T) {
	t.Parallel()
	rec := &recordingTelemetry{}
	e := &Engine{telemetry: rec, reuseVersionAuthorities: ReuseVersionAuthorities{OwnershipRoutingVersion: "ownership-routing.v7"}}
	e.recordReuseOutcome(context.Background(), storage.Principal{OrgID: "org_reuse_version_wiring"}, AnswerReuseMissNoCandidate)
	if len(rec.answerReuseOwnershipRoutingVersions) != 1 || rec.answerReuseOwnershipRoutingVersions[0] != "ownership-routing.v7" {
		t.Fatalf("answerReuseOwnershipRoutingVersions = %v, want exactly [%q]", rec.answerReuseOwnershipRoutingVersions, "ownership-routing.v7")
	}
}
