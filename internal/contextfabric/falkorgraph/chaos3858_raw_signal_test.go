package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3858: end-to-end (through the real Adapter.ResolveSubjects path,
// not a synthetic graphrank-level fixture) proof that
// Config.RawSignalObserver reaches the raw pre-remap lexical signal
// runFulltextQuery computes, for the SAME weak-lone-hit fixture
// TestResolveSubjectsWeakLoneFulltextHitDoesNotAutoCommit already pins the
// remapped Confidence (0.625 = 1-of-4 terms) for -- proving the raw pair
// (1, 4) and the remapped value describe the SAME candidate without
// re-deriving the fixture.

type fakeRawSignalObserver struct {
	observed map[string]graphrank.CandidateNode
}

func (f *fakeRawSignalObserver) ObserveCandidate(ctx context.Context, subjectKey string, node graphrank.CandidateNode) {
	if f.observed == nil {
		f.observed = map[string]graphrank.CandidateNode{}
	}
	f.observed[subjectKey] = node
}

func TestAdapterResolveSubjects_RawSignalObserverReceivesRawLexicalCoverage(t *testing.T) {
	weakHit := fulltextRow("incident", "weak_hit", "Unrelated Status", "Unrelated outage Status", nil)
	fake := fixedRowsFulltextConn([]row{weakHit})
	adapter := newFakeAdapter(t, fake)
	observer := &fakeRawSignalObserver{}
	adapter.config.RawSignalObserver = observer
	request, interpreted := openQuestionRequest("incident outage payment gateway")

	resolution, _, _, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	// Sanity anchor: still the same 1-of-4 remapped confidence the
	// pre-existing test pins, so this test is observing the SAME candidate.
	const want1of4 = 0.50 + 0.25*0.25
	if len(resolution.Candidates) != 1 || resolution.Candidates[0].Confidence != want1of4 {
		t.Fatalf("resolution.Candidates = %#v, want exactly 1 at confidence %v", resolution.Candidates, want1of4)
	}

	if len(observer.observed) != 1 {
		t.Fatalf("observer.observed = %#v, want exactly 1 candidate", observer.observed)
	}
	var got graphrank.CandidateNode
	for _, node := range observer.observed {
		got = node
	}
	if got.LexicalMatchedTerms == nil || *got.LexicalMatchedTerms != 1 {
		t.Fatalf("observed LexicalMatchedTerms = %v, want 1 (matches the pinned 1-of-4 remapped confidence)", got.LexicalMatchedTerms)
	}
	if got.LexicalTermCount == nil || *got.LexicalTermCount != 4 {
		t.Fatalf("observed LexicalTermCount = %v, want 4", got.LexicalTermCount)
	}
}

// TestAdapterResolveSubjects_NilRawSignalObserverIsDefault pins that an
// Adapter built without ever touching RawSignalObserver (every production
// composition path) behaves identically to today -- no panic, same
// resolution outcome.
func TestAdapterResolveSubjects_NilRawSignalObserverIsDefault(t *testing.T) {
	weakHit := fulltextRow("incident", "weak_hit", "Unrelated Status", "Unrelated outage Status", nil)
	fake := fixedRowsFulltextConn([]row{weakHit})
	adapter := newFakeAdapter(t, fake)
	request, interpreted := openQuestionRequest("incident outage payment gateway")

	resolution, _, _, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v (nil observer must never cause a failure)", err)
	}
	if len(resolution.Candidates) != 1 {
		t.Fatalf("resolution.Candidates = %#v, want exactly 1", resolution.Candidates)
	}
}

// TestRunFulltextQuery_NilObserverLeavesRawLexicalFieldsNil is the
// adversarial-review-requested regression pin, at the exact call site the
// finding named (runFulltextQuery, queries.go): with no RawSignalObserver
// configured (a.config.RawSignalObserver == nil, every production
// deployment), LexicalMatchedTerms/LexicalTermCount must stay nil on every
// returned CandidateNode -- not merely unread, but never assigned, so
// `matched`/`termCount` stay pure stack locals and never escape to the heap
// on this hot path. Guards the `if a.config.RawSignalObserver != nil` gate
// runFulltextQuery uses directly, independent of the graphrank/resolution
// layer above it.
func TestRunFulltextQuery_NilObserverLeavesRawLexicalFieldsNil(t *testing.T) {
	fullMatch := fulltextRow("incident", "full_hit", "Incident Outage", "Incident outage payment gateway", nil)
	fake := fixedRowsFulltextConn([]row{fullMatch})
	adapter := newFakeAdapter(t, fake)
	// RawSignalObserver deliberately left nil (the default).

	candidates, truncated, err := adapter.fulltextSearchNodes(context.Background(), "graph-key", "org-1", "incident outage payment gateway", 10, temporalFilter{})
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	if truncated {
		t.Fatalf("fulltextSearchNodes() truncated = true, want false (this test needs the !truncated branch to run)")
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want exactly 1", candidates)
	}
	if candidates[0].LexicalMatchedTerms != nil || candidates[0].LexicalTermCount != nil {
		t.Fatalf("candidates[0] = %#v, want LexicalMatchedTerms/LexicalTermCount both nil with no RawSignalObserver configured", candidates[0])
	}
}

// TestRunFulltextQuery_ConfiguredObserverPopulatesRawLexicalFields is the
// positive companion: WITH a.config.RawSignalObserver set, the same call
// DOES populate the raw pair -- proving the gate suppresses the fields only
// when unconfigured, not unconditionally.
func TestRunFulltextQuery_ConfiguredObserverPopulatesRawLexicalFields(t *testing.T) {
	fullMatch := fulltextRow("incident", "full_hit", "Incident Outage", "Incident outage payment gateway", nil)
	fake := fixedRowsFulltextConn([]row{fullMatch})
	adapter := newFakeAdapter(t, fake)
	adapter.config.RawSignalObserver = &fakeRawSignalObserver{}

	candidates, truncated, err := adapter.fulltextSearchNodes(context.Background(), "graph-key", "org-1", "incident outage payment gateway", 10, temporalFilter{})
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	if truncated {
		t.Fatalf("fulltextSearchNodes() truncated = true, want false")
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want exactly 1", candidates)
	}
	if candidates[0].LexicalMatchedTerms == nil || candidates[0].LexicalTermCount == nil {
		t.Fatalf("candidates[0] = %#v, want LexicalMatchedTerms/LexicalTermCount both set with a RawSignalObserver configured", candidates[0])
	}
}
