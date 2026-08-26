package graphrank

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// CHAOS-4311 Phase 3: this file exists because of the exact reachability-gap
// class CHAOS-4155's own history (four separate gaps, cf-rulings.md
// 2026-08-25 22:27) was built from -- a new ResolutionTraceEvent field with
// no corresponding SlogResolutionTracer log key is a field that never
// reaches a production log line at all, regardless of DebugContext level.
// TestSlogResolutionTracer_CoversEveryEmittedStage (chaos3918_tracer_stage_coverage_test.go)
// only proves every STAGE has a case; it says nothing about individual
// FIELDS within a stage, which is exactly the gap these two tests close for
// the two new CHAOS-4311 fields.

// TestSlogResolutionTracer_ConfirmedKindScopeStageLogsRivalsOfferedCount pins
// that ConfirmedKindVectorScopeRivalsOfferedCount actually reaches the
// "confirmed_kind_scope" stage's own log line.
func TestSlogResolutionTracer_ConfirmedKindScopeStageLogsRivalsOfferedCount(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tracer := NewSlogResolutionTracer(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	tracer.Trace(ResolutionTraceEvent{
		RequestID: "req-4311", Stage: "confirmed_kind_scope",
		ConfirmedKindScopeState:                    "plan_incomplete",
		ConfirmedKindVectorScopeState:              "complete",
		ConfirmedKindVectorScopeRivalCountAboveTau: 3,
		ConfirmedKindVectorScopeRivalsOfferedCount: 2,
	})
	out := buf.String()
	if !strings.Contains(out, "vector_census_rivals_offered_count=2") {
		t.Fatalf("log line = %q, want it to contain vector_census_rivals_offered_count=2", out)
	}
}

// TestSlogResolutionTracer_DecisionStageLogsConfirmedKindVectorCensusDecisive
// pins that ConfirmedKindVectorCensusDecisive actually reaches the
// "decision" stage's own log line.
func TestSlogResolutionTracer_DecisionStageLogsConfirmedKindVectorCensusDecisive(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tracer := NewSlogResolutionTracer(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	tracer.Trace(ResolutionTraceEvent{
		RequestID: "req-4311", Stage: "decision", Outcome: "committed",
		ConfirmedKindVectorCensusDecisive: true,
	})
	out := buf.String()
	if !strings.Contains(out, "confirmed_kind_vector_census_decisive=true") {
		t.Fatalf("log line = %q, want it to contain confirmed_kind_vector_census_decisive=true", out)
	}
}
