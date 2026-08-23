package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-4117 standing telemetry order: raising the calibrated candidate
// limit (10 -> 20) is a decision-basis change, and mid-rollout a caller can
// still request any contract-legal value -- so a resolution's own decision
// trace must say WHICH candidate-limit regime it ran under, not just
// whether it truncated. This pins ResolutionTraceEvent.SearchCandidateLimit
// across all three decision outcomes (committed, ambiguous, no_commit),
// each at a different limit, so the field is proven to carry the REQUEST's
// own value through -- never a hardcoded constant.

func resolveWithTracer(tracer ResolutionTracer, max int, searchTruncated bool, candidates ...contextfabric.SubjectCandidate) contextfabric.SubjectResolution {
	bySubject := make(map[string]contextfabric.SubjectCandidate, len(candidates))
	for _, candidate := range candidates {
		bySubject[SubjectKey(candidate.Subject)] = candidate
	}
	resolution, _, _ := ResolveFromMergedCandidatesWithGateAndBasis(
		bySubject, map[string]string{}, map[string]bool{}, max, true, searchTruncated,
		nil, 0, false, max, 20, true,
		DefaultCommitGatePolicy(), nil, nil, false, tracer, "request_1", "", false)
	return resolution
}

func TestChaos4117_DecisionTraceCarriesSearchCandidateLimit_Committed(t *testing.T) {
	tracer := &captureResolutionTracer{}
	resolution := resolveWithTracer(tracer, 20, false, corroborationCandidate("only", 0.9, contextfabric.MatchLexical))
	if len(resolution.Committed) != 1 {
		t.Fatalf("resolution.Committed = %#v, want exactly one commit for this fixture", resolution.Committed)
	}
	events := tracer.eventsForStage("decision")
	if len(events) != 1 {
		t.Fatalf("decision events = %d, want 1", len(events))
	}
	if events[0].Outcome != "committed" {
		t.Fatalf("Outcome = %q, want committed", events[0].Outcome)
	}
	if events[0].SearchCandidateLimit != 20 {
		t.Fatalf("SearchCandidateLimit = %d, want 20 (the request's own MaxSubjectCandidates)", events[0].SearchCandidateLimit)
	}
}

func TestChaos4117_DecisionTraceCarriesSearchCandidateLimit_Ambiguous(t *testing.T) {
	tracer := &captureResolutionTracer{}
	// Two candidates, low and equal confidence: below LoneFloor, so the
	// default switch case fires (ambiguous=true), never a commit gate.
	resolution := resolveWithTracer(tracer, 7, false,
		corroborationCandidate("a", 0.30, contextfabric.MatchLexical),
		corroborationCandidate("b", 0.30, contextfabric.MatchLexical))
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want none for this fixture", resolution.Committed)
	}
	events := tracer.eventsForStage("decision")
	if len(events) != 1 {
		t.Fatalf("decision events = %d, want 1", len(events))
	}
	if events[0].Outcome != "ambiguous" {
		t.Fatalf("Outcome = %q, want ambiguous", events[0].Outcome)
	}
	if events[0].SearchCandidateLimit != 7 {
		t.Fatalf("SearchCandidateLimit = %d, want 7 (the request's own MaxSubjectCandidates)", events[0].SearchCandidateLimit)
	}
}

func TestChaos4117_DecisionTraceCarriesSearchCandidateLimit_NoCommitEmptyPool(t *testing.T) {
	tracer := &captureResolutionTracer{}
	resolution := resolveWithTracer(tracer, 10, true /* searchTruncated */)
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want none for an empty candidate pool", resolution.Committed)
	}
	events := tracer.eventsForStage("decision")
	if len(events) != 1 {
		t.Fatalf("decision events = %d, want 1", len(events))
	}
	if events[0].Outcome != "no_commit" {
		t.Fatalf("Outcome = %q, want no_commit", events[0].Outcome)
	}
	if events[0].SearchCandidateLimit != 10 {
		t.Fatalf("SearchCandidateLimit = %d, want 10 (the request's own MaxSubjectCandidates)", events[0].SearchCandidateLimit)
	}
}
