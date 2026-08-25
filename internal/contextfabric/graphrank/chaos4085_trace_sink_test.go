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

// TestChaos4085_ProductionTraceEmitsKindOfferDiagnostics is the sink-level
// pin for CHAOS-4012's kind_offer stage -- same file-doc-comment obligation
// ("If you add a field to ResolutionTraceEvent, assert it here too") as
// every field above. A SEPARATE stage from "decision" (its own event, not
// folded onto CommitGate/SearchTruncated above), so this asserts a
// dedicated captureTraceJSON call rather than adding keys to the two tests
// above.
func TestChaos4085_ProductionTraceEmitsKindOfferDiagnostics(t *testing.T) {
	record := captureTraceJSON(t, ResolutionTraceEvent{
		RequestID: "request_sink_0003", Stage: "kind_offer",
		KindOfferExplicitHintCount:       1,
		KindOfferDistinctKindCount:       1,
		KindOfferSuppressedByCardinality: true,
		KindOfferCandidateOfferCount:     5,
		KindOfferOfferKind:               "candidate",
		// CHAOS-4210: deliberately non-zero and distinct from
		// KindOfferCandidateOfferCount, so the assertion below proves the
		// sink carries THIS field's own number, not a coincidental
		// zero-value or candidate_offer_count match.
		KindOfferCandidateOfferLabelsNormalizedCount: 2,
		KindOfferBoundaryKinds:                       []string{"work_item", "repository"},
		// CHAOS-4183 phase 3: the pre-repair twins, deliberately DIFFERENT
		// from the post-repair values above (one fewer boundary kind, a
		// different distinct count, suppressed flipped) -- proves the sink
		// carries the repair's own before/after delta, not the same value
		// twice under two keys.
		KindOfferBoundaryKindsBeforeRepair:           []string{"repository"},
		KindOfferDistinctKindCountBeforeRepair:       1,
		KindOfferSuppressedByCardinalityBeforeRepair: true,
		// CHAOS-4119: deliberately non-zero/distinct values, so the
		// assertions below prove the sink carries THIS call's own numbers,
		// not a coincidental zero-value match.
		HandleOfferCountBeforeGraphSource:    2,
		HandleOfferGraphDerivedCount:         3,
		HandleOfferGraphDerivedRejectedCount: 4,
	})

	if got, ok := record["explicit_hint_count"].(float64); !ok || got != 1 {
		t.Fatalf("explicit_hint_count = %v, want 1", record["explicit_hint_count"])
	}
	if got, ok := record["distinct_kind_count"].(float64); !ok || got != 1 {
		t.Fatalf("distinct_kind_count = %v, want 1", record["distinct_kind_count"])
	}
	if got, ok := record["suppressed_by_cardinality"].(bool); !ok || !got {
		t.Fatalf("suppressed_by_cardinality = %v, want true", record["suppressed_by_cardinality"])
	}
	// CHAOS-4012 v22: the candidate-list axis's own pair, same sink.
	if got, ok := record["candidate_offer_count"].(float64); !ok || got != 5 {
		t.Fatalf("candidate_offer_count = %v, want 5", record["candidate_offer_count"])
	}
	if got := record["offer_kind"]; got != "candidate" {
		t.Fatalf("offer_kind = %v, want %q", got, "candidate")
	}
	// CHAOS-4210: candidate_offer_labels_normalized_count must reach the
	// PRODUCTION sink too -- same file-doc-comment obligation as every
	// field above.
	if got, ok := record["candidate_offer_labels_normalized_count"].(float64); !ok || got != 2 {
		t.Fatalf("candidate_offer_labels_normalized_count = %v, want 2", record["candidate_offer_labels_normalized_count"])
	}
	// CHAOS-4012 v22 (re-smoke follow-up): boundary_kinds is the
	// call-boundary-scoped pair's own field -- must reach the sink as the
	// actual kind values, not merely a count.
	boundaryKinds, ok := record["boundary_kinds"].([]any)
	if !ok || len(boundaryKinds) != 2 || boundaryKinds[0] != "work_item" || boundaryKinds[1] != "repository" {
		t.Fatalf("boundary_kinds = %v, want [work_item repository]", record["boundary_kinds"])
	}
	// CHAOS-4183 phase 3 (sol design consult, team-lead ratified
	// 2026-08-23): the three pre-repair twins must reach the PRODUCTION
	// sink too -- same file-doc-comment obligation as every field above.
	beforeKinds, ok := record["boundary_kinds_before_repair"].([]any)
	if !ok || len(beforeKinds) != 1 || beforeKinds[0] != "repository" {
		t.Fatalf("boundary_kinds_before_repair = %v, want [repository]", record["boundary_kinds_before_repair"])
	}
	if got, ok := record["distinct_kind_count_before_repair"].(float64); !ok || got != 1 {
		t.Fatalf("distinct_kind_count_before_repair = %v, want 1", record["distinct_kind_count_before_repair"])
	}
	if got, ok := record["suppressed_by_cardinality_before_repair"].(bool); !ok || !got {
		t.Fatalf("suppressed_by_cardinality_before_repair = %v, want true", record["suppressed_by_cardinality_before_repair"])
	}
	// CHAOS-4119: handleOfferMaterial's own graph-derived-source
	// diagnostics must reach the PRODUCTION sink too -- same
	// file-doc-comment obligation as every field above.
	if got, ok := record["handle_offer_count_before_graph_source"].(float64); !ok || got != 2 {
		t.Fatalf("handle_offer_count_before_graph_source = %v, want 2", record["handle_offer_count_before_graph_source"])
	}
	if got, ok := record["handle_offer_graph_derived_count"].(float64); !ok || got != 3 {
		t.Fatalf("handle_offer_graph_derived_count = %v, want 3", record["handle_offer_graph_derived_count"])
	}
	if got, ok := record["handle_offer_graph_derived_rejected_count"].(float64); !ok || got != 4 {
		t.Fatalf("handle_offer_graph_derived_rejected_count = %v, want 4", record["handle_offer_graph_derived_rejected_count"])
	}
}

// TestChaos4085_ProductionTraceEmitsKindCoverageFloorDiagnostics is the
// sink-level pin for CHAOS-4038's kind_coverage_floor stage -- same
// file-doc-comment obligation as every field above. This stage had NO
// sink-level test until CHAOS-4183 phase 2 (2026-08-23): the fields existed,
// were populated, and were read by the harness's own in-memory tracer, but
// nothing proved they survived the PRODUCTION SlogResolutionTracer sink --
// exactly the CommitBasis/TiedStatisticalTop gap this file's own doc comment
// (top of file) exists to prevent from recurring.
func TestChaos4085_ProductionTraceEmitsKindCoverageFloorDiagnostics(t *testing.T) {
	record := captureTraceJSON(t, ResolutionTraceEvent{
		RequestID: "request_sink_0004", Stage: "kind_coverage_floor",
		KindCoverageFloorFired:       true,
		KindCoverageMissingKinds:     1,
		KindCoverageFloorTruncated:   true,
		KindCoverageMissingKindsList: []string{"work_item"},
	})

	if got, ok := record["fired"].(bool); !ok || !got {
		t.Fatalf("fired = %v, want true", record["fired"])
	}
	if got, ok := record["missing_kinds"].(float64); !ok || got != 1 {
		t.Fatalf("missing_kinds = %v, want 1", record["missing_kinds"])
	}
	if got, ok := record["truncated"].(bool); !ok || !got {
		t.Fatalf("truncated = %v, want true", record["truncated"])
	}
	// CHAOS-4183 phase 2: missing_kinds_list is the kind-IDENTITY twin of
	// missing_kinds -- must reach the sink as the actual kind values, not
	// merely a count, or a future reader hits the SAME re-smoke ambiguity
	// this field exists to resolve.
	missingKindsList, ok := record["missing_kinds_list"].([]any)
	if !ok || len(missingKindsList) != 1 || missingKindsList[0] != "work_item" {
		t.Fatalf("missing_kinds_list = %v, want [work_item]", record["missing_kinds_list"])
	}
}
