package graphrank

// r2 CLASS pin: an r2 review round found event.Subject.CanonicalID logged
// raw at 11 sites in this file's Trace switch (subject_kind/
// subject_canonical_id pairs) -- CHAOS-5544's first two commits sanitized
// request_id/stage/source_native_grammar in this file but missed this
// sibling field, mechanically fixed by the whole-tree instrument
// (chaos5544_sanitizer_instrument_test.go, internal/contextfabric package).
// This pins the fix through the real production sink at one representative
// site (slice_b_survivor_verdict, which already had its own real-handler
// test for a different reason -- chaos3918_tracer_test.go's
// TestSlogResolutionTracer_SliceBSurvivorVerdictStage).
import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestSlogResolutionTracer_SubjectCanonicalIDSanitizedAcrossTheInputDomain(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, control string }{
		{"lf", "\n"}, {"cr", "\r"}, {"ansi_escape", "\x1b[31m"}, {"nul", "\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			tracer := NewSlogResolutionTracer(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			forged := "pull_request:repo-1:1" + tc.control + "fake_field=1"
			tracer.Trace(ResolutionTraceEvent{
				RequestID: "req-1", Stage: "slice_b_survivor_verdict",
				Subject:         contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: forged},
				SurvivorVerdict: "eliminated",
			})
			var fields map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &fields); err != nil {
				t.Fatalf("emitted line is not valid JSON (more than one line, or malformed): %v; raw=%q", err, buf.String())
			}
			got, _ := fields["subject_canonical_id"].(string)
			if strings.Contains(got, tc.control) {
				t.Fatalf("the raw control byte %q survived into subject_canonical_id: %q", tc.control, got)
			}
			if !strings.HasPrefix(got, "pull_request:repo-1:1") {
				t.Fatalf("subject_canonical_id = %q lost its correlation prefix", got)
			}
		})
	}
}
