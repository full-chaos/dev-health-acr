package graphrank

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestSlogResolutionTracer_EvidenceSourceNativeStages pins the codex xhigh
// review finding (CHAOS-3918, confirmed and fixed): SlogResolutionTracer's
// own Trace switch used to have no case for "evidence_source_native" or
// "evidence_source_native_probe", so both fell to the "unknown stage"
// branch and silently dropped the widening measurement's whole payload in
// production (the SAME defect class "evidence_census_commit" was already
// fixed for -- see that case's own doc comment). This test proves both
// new stages produce a log line naming their own fields, never falling to
// "unknown stage".
func TestSlogResolutionTracer_EvidenceSourceNativeStages(t *testing.T) {
	t.Parallel()

	t.Run("evidence_source_native", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		tracer := NewSlogResolutionTracer(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		tracer.Trace(ResolutionTraceEvent{
			RequestID: "req-1", Stage: "evidence_source_native",
			ShadowSourceNativeMatchCount: 3, ShadowSourceNativeAnyResolved: true,
		})
		out := buf.String()
		if strings.Contains(out, "unknown stage") {
			t.Fatalf("evidence_source_native fell to the unknown-stage branch: %q", out)
		}
		if !strings.Contains(out, "source_native_match_count=3") || !strings.Contains(out, "source_native_any_resolved=true") {
			t.Fatalf("evidence_source_native log line missing expected fields: %q", out)
		}
	})

	t.Run("evidence_source_native_probe", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		tracer := NewSlogResolutionTracer(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		tracer.Trace(ResolutionTraceEvent{
			RequestID: "req-1", Stage: "evidence_source_native_probe",
			ShadowSourceNativeGrammar: "repo_slug", ShadowSourceNativeResolved: true,
			ShadowSourceNativeKind: "repository",
		})
		out := buf.String()
		if strings.Contains(out, "unknown stage") {
			t.Fatalf("evidence_source_native_probe fell to the unknown-stage branch: %q", out)
		}
		if !strings.Contains(out, "source_native_grammar=repo_slug") || !strings.Contains(out, "source_native_resolved=true") ||
			!strings.Contains(out, "source_native_kind=repository") {
			t.Fatalf("evidence_source_native_probe log line missing expected fields: %q", out)
		}
	})
}
