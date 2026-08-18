package hosted_test

// CHAOS-3858: unit coverage for trialRawSignalCollector/attachRawSignal --
// the trial-harness-side plumbing that correlates graphrank.RawSignalObserver
// captures back onto a case's committed_matches/top_non_committed_match
// provenance entries. See trialRawSignalCollector's own doc comment
// (generative_trial_live_test.go) for the "keep highest observed" and
// per-case reset contract these tests pin.

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func floatPtr(v float64) *float64 { return &v }
func intPtr(v int) *int           { return &v }

func TestTrialRawSignalCollector_keepsHighestObservedVectorSimilarity(t *testing.T) {
	c := &trialRawSignalCollector{}
	c.ObserveCandidate(context.Background(), "project\x00project_x", graphrank.CandidateNode{VectorSimilarity: floatPtr(0.4)})
	c.ObserveCandidate(context.Background(), "project\x00project_x", graphrank.CandidateNode{VectorSimilarity: floatPtr(0.9)})
	c.ObserveCandidate(context.Background(), "project\x00project_x", graphrank.CandidateNode{VectorSimilarity: floatPtr(0.2)})

	snapshot := c.snapshotAndReset()
	got := snapshot["project\x00project_x"]
	if got.VectorSimilarity == nil || *got.VectorSimilarity != 0.9 {
		t.Fatalf("VectorSimilarity = %v, want 0.9 (the highest of the three observed)", got.VectorSimilarity)
	}
}

func TestTrialRawSignalCollector_keepsHighestObservedLexicalRatio(t *testing.T) {
	c := &trialRawSignalCollector{}
	// 1/4 = 0.25
	c.ObserveCandidate(context.Background(), "project\x00project_y", graphrank.CandidateNode{LexicalMatchedTerms: intPtr(1), LexicalTermCount: intPtr(4)})
	// 3/4 = 0.75 -- higher ratio, must win even though matched count 3 > 1 is the only reason, not a tie
	c.ObserveCandidate(context.Background(), "project\x00project_y", graphrank.CandidateNode{LexicalMatchedTerms: intPtr(3), LexicalTermCount: intPtr(4)})
	// 1/2 = 0.5 -- lower ratio than 0.75, must not overwrite
	c.ObserveCandidate(context.Background(), "project\x00project_y", graphrank.CandidateNode{LexicalMatchedTerms: intPtr(1), LexicalTermCount: intPtr(2)})

	snapshot := c.snapshotAndReset()
	got := snapshot["project\x00project_y"]
	if got.LexicalMatchedTerms == nil || *got.LexicalMatchedTerms != 3 || got.LexicalTermCount == nil || *got.LexicalTermCount != 4 {
		t.Fatalf("lexical raw = (%v,%v), want (3,4) (the highest 3/4 ratio observed)", got.LexicalMatchedTerms, got.LexicalTermCount)
	}
}

func TestTrialRawSignalCollector_resetClearsBetweenCases(t *testing.T) {
	c := &trialRawSignalCollector{}
	c.ObserveCandidate(context.Background(), "project\x00project_x", graphrank.CandidateNode{VectorSimilarity: floatPtr(0.9)})
	c.reset()
	if snapshot := c.snapshotAndReset(); len(snapshot) != 0 {
		t.Fatalf("snapshotAndReset() after reset() = %#v, want empty -- case N's raw signal must never leak into case N+1", snapshot)
	}
}

func TestTrialRawSignalCollector_snapshotAndResetClearsState(t *testing.T) {
	c := &trialRawSignalCollector{}
	c.ObserveCandidate(context.Background(), "project\x00project_x", graphrank.CandidateNode{VectorSimilarity: floatPtr(0.9)})
	first := c.snapshotAndReset()
	if len(first) != 1 {
		t.Fatalf("first snapshot = %#v, want exactly 1 entry", first)
	}
	second := c.snapshotAndReset()
	if len(second) != 0 {
		t.Fatalf("second snapshot (no new observations in between) = %#v, want empty", second)
	}
}

func TestAttachRawSignal_nilProvIsNoop(t *testing.T) {
	// Must not panic.
	attachRawSignal(nil, map[string]graphrank.CandidateNode{"x": {}})
}

func TestAttachRawSignal_nilSnapshotIsNoop(t *testing.T) {
	prov := &trialCandidateMatchProvenance{Kind: "project", CanonicalID: "project_x"}
	attachRawSignal(prov, nil)
	if prov.RawVectorSimilarity != nil {
		t.Fatalf("prov = %#v, want unchanged (nil snapshot)", prov)
	}
}

func TestAttachRawSignal_noEntryForSubjectIsNoop(t *testing.T) {
	prov := &trialCandidateMatchProvenance{Kind: "project", CanonicalID: "project_x"}
	snapshot := map[string]graphrank.CandidateNode{
		graphrank.SubjectKey(contractsv1.ContextFabricSubjectRef{Kind: "project", CanonicalID: "project_OTHER"}): {VectorSimilarity: floatPtr(0.9)},
	}
	attachRawSignal(prov, snapshot)
	if prov.RawVectorSimilarity != nil {
		t.Fatalf("prov = %#v, want unchanged (snapshot has no entry for this subject)", prov)
	}
}

func TestAttachRawSignal_populatesMatchingSubjectRawFields(t *testing.T) {
	prov := &trialCandidateMatchProvenance{Kind: "project", CanonicalID: "project_x", Confidence: 0.755}
	key := graphrank.SubjectKey(contractsv1.ContextFabricSubjectRef{Kind: "project", CanonicalID: "project_x"})
	snapshot := map[string]graphrank.CandidateNode{
		key: {VectorSimilarity: floatPtr(0.61), LexicalMatchedTerms: intPtr(2), LexicalTermCount: intPtr(4)},
	}
	attachRawSignal(prov, snapshot)
	if prov.RawVectorSimilarity == nil || *prov.RawVectorSimilarity != 0.61 {
		t.Fatalf("RawVectorSimilarity = %v, want 0.61", prov.RawVectorSimilarity)
	}
	if prov.RawLexicalMatchedTerms == nil || *prov.RawLexicalMatchedTerms != 2 {
		t.Fatalf("RawLexicalMatchedTerms = %v, want 2", prov.RawLexicalMatchedTerms)
	}
	if prov.RawLexicalTermCount == nil || *prov.RawLexicalTermCount != 4 {
		t.Fatalf("RawLexicalTermCount = %v, want 4", prov.RawLexicalTermCount)
	}
	// Confidence (the remapped value) must be untouched by enrichment.
	if prov.Confidence != 0.755 {
		t.Fatalf("Confidence = %v, want unchanged 0.755", prov.Confidence)
	}
}
