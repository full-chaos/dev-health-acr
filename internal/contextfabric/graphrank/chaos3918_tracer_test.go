package graphrank

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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

// TestSlogResolutionTracer_SliceBSurvivorVerdictStage is CHAOS-4088's own
// sink-level pin (codex xhigh review round 1, non-blocking coverage gap,
// confirmed and closed): every OTHER new-stage addition in this file gets
// a test that actually exercises SlogResolutionTracer's Trace switch, not
// only the in-process ResolutionTraceEvent shape
// (chaos3896_slice_b_presentation_test.go's own tracer tests only ever
// assert against a recording double, never the production sink). This
// proves "slice_b_survivor_verdict" produces its own log line naming
// subject_kind/subject_canonical_id/survivor_verdict, never falling to
// "unknown stage" -- the exact defect class evidence_census_commit and
// evidence_source_native/evidence_source_native_probe were each found
// missing this coverage for.
func TestSlogResolutionTracer_SliceBSurvivorVerdictStage(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tracer := NewSlogResolutionTracer(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	tracer.Trace(ResolutionTraceEvent{
		RequestID: "req-slice-b-1", Stage: "slice_b_survivor_verdict",
		Subject:         contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:1"},
		SurvivorVerdict: "eliminated",
	})
	out := buf.String()
	if strings.Contains(out, "unknown stage") {
		t.Fatalf("slice_b_survivor_verdict fell to the unknown-stage branch: %q", out)
	}
	for _, want := range []string{"request_id=req-slice-b-1", "subject_kind=pull_request", "subject_canonical_id=pull_request:repo-1:1", "survivor_verdict=eliminated"} {
		if !strings.Contains(out, want) {
			t.Fatalf("slice_b_survivor_verdict log line missing %q: %q", want, out)
		}
	}
}

// TestSlogResolutionTracer_EvidenceSourceNativeStages_NoInjectionCharacters
// is the regression guard for the CodeQL go/log-injection finding, exercised
// through the REAL production sink (NewSlogResolutionTracer + a real
// slog.JSONHandler), CHAOS-5544: plants CR, LF, an ANSI escape, and a NUL
// byte -- the forgery-shape axis, not one hand-picked newline case -- in
// RequestID (and, for the probe stage, ShadowSourceNativeGrammar) and
// asserts none of them survive into the DECODED field value. JSONHandler
// (not TextHandler) is deliberate: both handlers already escape a raw
// control byte in their own on-the-wire encoding regardless of any
// sanitizer -- Go's stdlib quoting is belt-and-suspenders on top of this
// ticket's fix, not a substitute test oracle for it (an earlier draft of
// this test used TextHandler and raw-byte matching on the rendered line,
// and every arm SURVIVED: the stdlib's own escaping made sanitized and
// unsanitized output byte-identical, an equivalent-mutant trap the same
// shape chaos5544_log_sanitizer.go's own NewReplacer pin already named).
// json.Unmarshal round-trips a JSON handler's `\n` escape back into a raw
// byte, so checking the DECODED value distinguishes "SanitizeLogAttr
// replaced it with '?' before slog ever saw it" from "slog's own encoder
// escaped it for the wire" -- the former has no raw byte to round-trip
// back; the latter does. Stage itself stays the EXACT case string (Trace's
// switch matches on it verbatim) so the mutation battery's arms actually
// exercise the two sinks' own lines rather than falling through to the
// "unknown stage" default -- a forged Stage would just select a different
// (also-sanitized) branch, not prove anything about the branch under test.
// The local `sanitizeLogString` this used to pin (strings.Map-based,
// CHAOS-3918) is deleted -- CHAOS-5544 found it was a second, drifted
// implementation of the exact same concern contextfabric.SanitizeLogAttr
// already owns everywhere else in this repo; every
// request_id/stage/source_native_grammar site in this file now routes
// through that ONE shared function (its own input-domain table lives in
// internal/contextfabric/chaos5544_sanitize_log_attr_domain_test.go -- same
// function, so not re-duplicated here).
func TestSlogResolutionTracer_EvidenceSourceNativeStages_NoInjectionCharacters(t *testing.T) {
	t.Parallel()
	const forged = "fake_injected_field=1"

	decodeField := func(t *testing.T, buf *bytes.Buffer, key string) string {
		t.Helper()
		var fields map[string]any
		if err := json.Unmarshal(buf.Bytes(), &fields); err != nil {
			t.Fatalf("emitted line is not valid JSON (more than one line, or malformed): %v; raw=%q", err, buf.String())
		}
		got, _ := fields[key].(string)
		return got
	}

	for _, tc := range []struct {
		name    string
		control string
	}{
		{"lf", "\n"},
		{"cr", "\r"},
		{"ansi_escape", "\x1b[31m"},
		{"nul", "\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			poisoned := "req-1" + tc.control + forged

			t.Run("evidence_source_native", func(t *testing.T) {
				t.Parallel()
				var buf bytes.Buffer
				tracer := NewSlogResolutionTracer(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
				tracer.Trace(ResolutionTraceEvent{RequestID: poisoned, Stage: "evidence_source_native"})
				got := decodeField(t, &buf, "request_id")
				if strings.Contains(got, tc.control) {
					t.Fatalf("the raw control byte %q survived into the decoded request_id: %q", tc.control, got)
				}
				if !strings.HasPrefix(got, "req-1") {
					t.Fatalf("request_id = %q lost its correlation prefix", got)
				}
			})

			t.Run("evidence_source_native_probe", func(t *testing.T) {
				t.Parallel()
				var buf bytes.Buffer
				tracer := NewSlogResolutionTracer(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
				tracer.Trace(ResolutionTraceEvent{
					RequestID: poisoned, Stage: "evidence_source_native_probe",
					ShadowSourceNativeGrammar: "repo_slug" + tc.control + forged,
				})
				gotReqID := decodeField(t, &buf, "request_id")
				if strings.Contains(gotReqID, tc.control) {
					t.Fatalf("the raw control byte %q survived into the decoded request_id: %q", tc.control, gotReqID)
				}
				gotGrammar := decodeField(t, &buf, "source_native_grammar")
				if strings.Contains(gotGrammar, tc.control) {
					t.Fatalf("the raw control byte %q survived into the decoded source_native_grammar: %q", tc.control, gotGrammar)
				}
				if !strings.HasPrefix(gotReqID, "req-1") {
					t.Fatalf("request_id = %q lost its correlation prefix", gotReqID)
				}
			})
		})
	}
}
