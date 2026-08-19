package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3858: RawSignalObserver is a measurement-only capture port (see
// ResolveDeps.RawSignalObserver's own doc comment). These tests pin its two
// load-bearing properties: it reports the exact raw pre-remap values a
// mechanism computed (not the remapped Confidence), and it is invoked ONLY
// for candidates NodeCandidate has already accepted (never a pre-
// authorization existence oracle).

type observedCandidate struct {
	subjectKey string
	node       CandidateNode
}

type fakeRawSignalObserver struct {
	observed []observedCandidate
}

func (f *fakeRawSignalObserver) ObserveCandidate(ctx context.Context, subjectKey string, node CandidateNode) {
	f.observed = append(f.observed, observedCandidate{subjectKey: subjectKey, node: node})
}

// vectorCandidateNode/lexicalCandidateNode build on this file's own
// candidateNode() helper (candidate_test.go) -- same authorized attribute
// shape every other test in this package uses -- adding only the
// mechanism/raw-signal fields this file's tests need.
func vectorCandidateNode(kind contextfabric.SubjectKind, id, label string, similarity float64) CandidateNode {
	node := candidateNode(kind, id, label, 0.6, nil)
	sim := similarity
	node.Mechanism = contextfabric.MatchVector
	node.VectorSimilarity = &sim
	return node
}

func lexicalCandidateNode(kind contextfabric.SubjectKind, id, label string, matched, termCount int) CandidateNode {
	node := candidateNode(kind, id, label, 0.6, nil)
	m, tc := matched, termCount
	node.Mechanism = contextfabric.MatchLexical
	node.LexicalMatchedTerms = &m
	node.LexicalTermCount = &tc
	return node
}

// TestResolveSubjects_RawSignalObserverReportsRawVectorSimilarity pins that
// the observer sees the RAW similarity (e.g. 0.61), never the remapped
// Confidence the SAME candidate carries in the public result.
func TestResolveSubjects_RawSignalObserverReportsRawVectorSimilarity(t *testing.T) {
	node := vectorCandidateNode("project", "project_x", "Project X", 0.61)
	observer := &fakeRawSignalObserver{}
	backend := &fakeGraphBackend{
		searchResults:     map[string][]CandidateNode{"project x": {node}},
		rawSignalObserver: observer,
	}
	request, interpreted := testRequest(), testInterpreted("project x")

	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, backend.deps()); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}

	if len(observer.observed) != 1 {
		t.Fatalf("observed %d candidates, want exactly 1: %+v", len(observer.observed), observer.observed)
	}
	got := observer.observed[0]
	wantKey := SubjectKey(contextfabric.SubjectRef{Kind: "project", CanonicalID: "project_x"})
	if got.subjectKey != wantKey {
		t.Fatalf("observed subjectKey = %q, want %q", got.subjectKey, wantKey)
	}
	if got.node.VectorSimilarity == nil || *got.node.VectorSimilarity != 0.61 {
		t.Fatalf("observed VectorSimilarity = %v, want 0.61 (the RAW value, not remapped Confidence)", got.node.VectorSimilarity)
	}
}

// TestResolveSubjects_RawSignalObserverReportsRawLexicalCoverage is the
// lexical-mechanism companion: the observer sees the exact (matched,
// termCount) pair a lexical adapter computed, before
// fulltextRelevanceFromMatchedTerms-style remapping collapses it into a
// [0.50,0.75]-band Confidence.
func TestResolveSubjects_RawSignalObserverReportsRawLexicalCoverage(t *testing.T) {
	node := lexicalCandidateNode("project", "project_y", "Project Y", 2, 4)
	observer := &fakeRawSignalObserver{}
	backend := &fakeGraphBackend{
		searchResults:     map[string][]CandidateNode{"project y": {node}},
		rawSignalObserver: observer,
	}
	request, interpreted := testRequest(), testInterpreted("project y")

	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, backend.deps()); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}

	if len(observer.observed) != 1 {
		t.Fatalf("observed %d candidates, want exactly 1: %+v", len(observer.observed), observer.observed)
	}
	got := observer.observed[0].node
	if got.LexicalMatchedTerms == nil || *got.LexicalMatchedTerms != 2 {
		t.Fatalf("observed LexicalMatchedTerms = %v, want 2", got.LexicalMatchedTerms)
	}
	if got.LexicalTermCount == nil || *got.LexicalTermCount != 4 {
		t.Fatalf("observed LexicalTermCount = %v, want 4", got.LexicalTermCount)
	}
}

// TestResolveSubjects_RawSignalObserverNilIsNoop pins that a nil observer
// (every production deployment) changes nothing: same resolution outcome as
// the identical setup with no observer field set at all, and no panic.
func TestResolveSubjects_RawSignalObserverNilIsNoop(t *testing.T) {
	node := vectorCandidateNode("project", "project_z", "Project Z", 0.9)
	backend := &fakeGraphBackend{
		searchResults: map[string][]CandidateNode{"project z": {node}},
		// rawSignalObserver deliberately left nil.
	}
	request, interpreted := testRequest(), testInterpreted("project z")

	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v (nil observer must never cause a failure)", err)
	}
	if len(resolution.Candidates) != 1 {
		t.Fatalf("ResolveSubjects() candidates = %#v, want exactly 1", resolution.Candidates)
	}
}

// TestResolveSubjects_RawSignalObserverNeverCalledForRejectedCandidate is
// the security-load-bearing pin: the observer must never fire for a node
// NodeCandidate itself rejects (here, via isInternal) -- it is not a
// pre-authorization side channel like vectorArmSimilarity, precisely so it
// cannot become the existence-oracle hazard that map's own doc comment
// warns about.
func TestResolveSubjects_RawSignalObserverNeverCalledForRejectedCandidate(t *testing.T) {
	rejected := contextfabric.SubjectRef{Kind: "project", CanonicalID: "project_internal"}
	node := vectorCandidateNode(rejected.Kind, rejected.CanonicalID, "Internal Bookkeeping Node", 0.99)
	observer := &fakeRawSignalObserver{}
	backend := &fakeGraphBackend{
		searchResults:     map[string][]CandidateNode{"internal": {node}},
		rawSignalObserver: observer,
		isInternal: func(subject contextfabric.SubjectRef) bool {
			return SubjectKey(subject) == SubjectKey(rejected)
		},
	}
	request, interpreted := testRequest(), testInterpreted("internal")

	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, backend.deps()); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}

	if len(observer.observed) != 0 {
		t.Fatalf("observer.observed = %+v, want none -- a node NodeCandidate rejected must never reach the observer", observer.observed)
	}
}
