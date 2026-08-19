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

// TestSanitizeLogString pins sanitizeLogString's own contract (CodeQL
// go/log-injection, CHAOS-3918, 2026-08-19): strips \n/\r/other ASCII
// control characters (the classic log-forging vector -- an unescaped
// newline can make injected text masquerade as a separate, fabricated log
// line), leaves ordinary printable text -- including \t -- untouched.
func TestSanitizeLogString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "req_0123456789abcdef", "req_0123456789abcdef"},
		{"newline_forged_fake_line", "req-1\nfake_log_line=injected", "req-1fake_log_line=injected"},
		{"carriage_return", "req-1\rinjected", "req-1injected"},
		{"other_control_char", "req-1\x00\x07injected", "req-1injected"},
		{"tab_kept", "req-1\tstill-one-field", "req-1\tstill-one-field"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := sanitizeLogString(tc.in)
			if got != tc.want {
				t.Fatalf("sanitizeLogString(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSlogResolutionTracer_EvidenceSourceNativeStages_NoInjectionCharacters
// is the regression guard for the CodeQL go/log-injection finding: plants
// control characters (a newline, specifically) in EVERY string-typed
// field the two new stages carry (RequestID, Stage, and
// ShadowSourceNativeGrammar) and asserts none of them survive into the
// rendered log line -- proving sanitizeLogString is actually wired into
// both new cases, not just correct in isolation.
func TestSlogResolutionTracer_EvidenceSourceNativeStages_NoInjectionCharacters(t *testing.T) {
	t.Parallel()
	const forged = "fake_injected_field=1"
	poisoned := "req-1\n" + forged

	t.Run("evidence_source_native", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		tracer := NewSlogResolutionTracer(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		tracer.Trace(ResolutionTraceEvent{RequestID: poisoned, Stage: "evidence_source_native\n" + forged})
		out := buf.String()
		if strings.Contains(out, "\n"+forged) {
			t.Fatalf("a raw newline survived into the log line, forging a fake field: %q", out)
		}
	})

	t.Run("evidence_source_native_probe", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		tracer := NewSlogResolutionTracer(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		tracer.Trace(ResolutionTraceEvent{
			RequestID: poisoned, Stage: "evidence_source_native_probe\n" + forged,
			ShadowSourceNativeGrammar: "repo_slug\n" + forged,
		})
		out := buf.String()
		if strings.Contains(out, "\n"+forged) {
			t.Fatalf("a raw newline survived into the log line, forging a fake field: %q", out)
		}
	})
}
