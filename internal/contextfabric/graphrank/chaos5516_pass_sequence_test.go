package graphrank

// CHAOS-5516 r3 -- round r2 finding 4, reproduced and fixed: the new `pass`
// sequence semantics (1-based, monotonic per request_id) were not
// behaviorally pinned anywhere -- the reviewer showed that temporarily
// changing resolve.go's `pass := 1` to `pass := 0` left the entire
// graphrank suite green. This test drives a REAL two-pass resolution (the
// same re-decision fixture
// TestRankedCutSummary_LastSummaryDescribesTheKeptPassAcrossReDecisionPasses
// already established as reliably producing exactly two ranked_cut
// summaries) and asserts the two REAL emitted Pass values are 1 then 2, in
// that order -- so a mutation setting the initial value to 0, or removing
// either `pass++`, fails this test by name.

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestPassIsMonotonicAcrossARealReDecision(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "wi_1", Label: "Widget Rollout Backend Task"}
	node := candidateNode(kind, subject.CanonicalID, subject.Label, 0.9, "*")
	rival1 := candidateNode(kind, "wi_rival1", "Something Else Entirely", 0.85, "*")
	rival2 := candidateNode(kind, "wi_rival2", "Yet Another Rival", 0.75, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{term: {rival1, rival2}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			term: {kind: {node}},
		},
		searchTruncated: true,
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), deps, confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != subject {
		t.Fatalf("resolution.Committed = %#v, want the scoped re-decision's own subject -- this test's pass-sequence assertion is only meaningful if a real re-decision actually fired", resolution.Committed)
	}

	var passes []int
	for _, event := range tracer.events {
		if event.Stage == "ranked_cut" && event.RankedCutSummary {
			passes = append(passes, event.Pass)
		}
	}
	if len(passes) != 2 {
		t.Fatalf("ranked_cut summary events = %d, want exactly 2 (this fixture's own precondition: first pass + one scoped re-decision) -- pass-sequence assertions below would be meaningless otherwise: %v", len(passes), passes)
	}
	if passes[0] != 1 {
		t.Errorf("first pass's own Pass = %d, want 1 -- pass is 1-based (round r2 finding 4: `pass := 0` left the whole suite green before this pin)", passes[0])
	}
	if passes[1] != 2 {
		t.Errorf("second (re-decision) pass's own Pass = %d, want 2 -- monotonic, never repeating the first pass's own number", passes[1])
	}
}
