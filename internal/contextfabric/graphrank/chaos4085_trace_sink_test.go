package graphrank

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-4085 / CHAOS-4089 -- SINK-LEVEL trace tests.
//
// THE LESSON THIS FILE EXISTS TO ENFORCE: a field existing on
// ResolutionTraceEvent, and being populated at every emission site, must
// NEVER again pass for that field reaching an operator.
//
// CommitBasis and TiedStatisticalTop shipped defined, populated, and tested
// -- through an in-memory tracer that read them straight off the struct --
// while SlogResolutionTracer, the sink the hosted runtime actually installs,
// logged neither. The production decision line was byte-for-byte unchanged.
// A retroactive codex pass caught it; no test could have, because every test
// bypassed the sink.
//
// So these assert on the PRODUCTION sink's real output. If you add a field
// to ResolutionTraceEvent, assert it here too.

func captureTraceJSON(t *testing.T, event ResolutionTraceEvent) map[string]any {
	t.Helper()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	NewSlogResolutionTracer(logger).Trace(event)

	line := strings.TrimSpace(buffer.String())
	if line == "" {
		t.Fatal("the production tracer emitted nothing for this event")
	}
	record := map[string]any{}
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("trace line is not valid JSON: %v (%q)", err, line)
	}
	return record
}

// TestChaos4085_ProductionTraceEmitsCommitBasisAndTieFlag is the pin for the
// gap: both fields must appear, by the exact key an operator greps for, in
// the production decision line.
func TestChaos4085_ProductionTraceEmitsCommitBasisAndTieFlag(t *testing.T) {
	record := captureTraceJSON(t, ResolutionTraceEvent{
		RequestID: "request_sink_0001", Stage: "decision",
		Subject:    contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:linear:SINK-1"},
		Outcome:    "committed",
		CommitGate: "lone_floor", WinningMechanism: "lexical",
		SearchTruncated:      true,
		CommitBasis:          string(contextfabric.CommitBasisStatistical),
		TiedStatisticalTop:   true,
		SearchCandidateLimit: 20,
	})

	if got, ok := record["commit_basis"]; !ok || got != string(contextfabric.CommitBasisStatistical) {
		t.Fatalf("commit_basis = %v (present=%v), want %q", got, ok, contextfabric.CommitBasisStatistical)
	}
	if got, ok := record["tied_statistical_top"].(bool); !ok || !got {
		t.Fatalf("tied_statistical_top = %v, want true", record["tied_statistical_top"])
	}
	// The fields this ticket added sit BESIDE the ones that were already
	// there; a regression that dropped the originals while keeping the new
	// ones would be just as bad.
	if got, ok := record["search_truncated"].(bool); !ok || !got {
		t.Fatalf("search_truncated = %v, want true", record["search_truncated"])
	}
	if record["commit_gate"] != "lone_floor" {
		t.Fatalf("commit_gate = %v, want lone_floor", record["commit_gate"])
	}
	// CHAOS-4117: the field this ticket adds -- same file-doc-comment
	// obligation ("If you add a field to ResolutionTraceEvent, assert it
	// here too") the CHAOS-4085 fields above were added under.
	if got, ok := record["search_candidate_limit"].(float64); !ok || got != 20 {
		t.Fatalf("search_candidate_limit = %v, want 20", record["search_candidate_limit"])
	}
}

// TestChaos4085_ProductionTraceEmitsTheTieFlagOnANonCommittedOutcome covers
// the population the refusal is counted from. An ambiguous decision is
// exactly where a reader looks for the tied-rescue refusal's necessary
// condition, so the flag has to survive to the sink on that outcome too --
// not only on a commit, where an empty CommitBasis would have masked a
// sink-side default.
func TestChaos4085_ProductionTraceEmitsTheTieFlagOnANonCommittedOutcome(t *testing.T) {
	record := captureTraceJSON(t, ResolutionTraceEvent{
		RequestID: "request_sink_0002", Stage: "decision",
		Outcome: "ambiguous", SearchTruncated: true, TiedStatisticalTop: true,
		SearchCandidateLimit: 10,
	})

	if got, ok := record["tied_statistical_top"].(bool); !ok || !got {
		t.Fatalf("tied_statistical_top = %v, want true on an ambiguous outcome", record["tied_statistical_top"])
	}
	// Nothing committed, so the basis is empty -- and it must still be
	// PRESENT as a key, or a reader cannot distinguish "no basis" from
	// "this deployment predates the field".
	if _, ok := record["commit_basis"]; !ok {
		t.Fatal("commit_basis must be present even when empty, so its absence means an old deployment rather than an unproven commit")
	}
	if got := record["commit_basis"]; got != "" {
		t.Fatalf("commit_basis = %v, want empty for a non-committed outcome", got)
	}
	// CHAOS-4117: a deliberately DIFFERENT limit than the committed-outcome
	// test above (10, not 20) -- proves this is the request's own value
	// reaching the sink, not a hardcoded constant that happens to match.
	if got, ok := record["search_candidate_limit"].(float64); !ok || got != 10 {
		t.Fatalf("search_candidate_limit = %v, want 10", record["search_candidate_limit"])
	}
}
