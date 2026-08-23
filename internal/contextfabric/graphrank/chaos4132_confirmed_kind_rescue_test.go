package graphrank

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestApplyConfirmedKindRescue_NilSearchKindIsNoOp mirrors
// applyKindCoverageFloor's own convention: a backend that does not
// implement kind-scoped search cannot be rescued, and the function returns
// cleanly rather than erroring.
func TestApplyConfirmedKindRescue_NilSearchKindIsNoOp(t *testing.T) {
	t.Parallel()
	deps := ResolveDeps{}
	added, traversalDegraded, authzDropped, truncated, degraded, err := applyConfirmedKindRescue(
		context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), deps,
		[]string{"alpha"}, map[string]contextfabric.SubjectCandidate{}, nil, nil, nil, nil,
		contextfabric.SubjectWorkItem)
	if err != nil {
		t.Fatalf("applyConfirmedKindRescue() error = %v, want nil", err)
	}
	if added != nil || traversalDegraded != 0 || authzDropped != 0 || truncated || degraded {
		t.Fatalf("applyConfirmedKindRescue(nil SearchKind) = (%#v, %d, %d, %v, %v), want all zero values", added, traversalDegraded, authzDropped, truncated, degraded)
	}
}

// TestApplyConfirmedKindRescue_PropagatesBackendError proves a genuine
// SearchKind failure aborts and surfaces, exactly like every other
// retrieval pass in this file -- never silently downgraded to "found
// nothing".
func TestApplyConfirmedKindRescue_PropagatesBackendError(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{enableSearchKind: true, searchKindErr: errors.New("transient backend failure")}
	_, _, _, _, _, err := applyConfirmedKindRescue(
		context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), backend.deps(),
		[]string{"alpha"}, map[string]contextfabric.SubjectCandidate{}, nil, nil, nil, nil,
		contextfabric.SubjectWorkItem)
	if err == nil {
		t.Fatal("applyConfirmedKindRescue() error = nil, want the backend failure propagated")
	}
}

// TestResolveSubjects_ConfirmedKindRescueFiresWhenPoolEmptyAfterFiltering is
// CHAOS-4132's own repro-and-fix pin, direction (a) of team-lead's ask: a
// confirmed kind whose ONLY route into the pool is a kind-scoped search
// (the exact shape CHAOS-4038's coverage floor exists to backfill, and
// which a confirmed-kind call skips by design) must not be a guaranteed
// no_match -- the rescue must find it and let it commit.
//
// Ordinary search finds NOTHING for this term at all (searchResults is
// empty) -- the starved-kind premise CHAOS-4038 itself is built on --
// while SearchKind (queried only by this rescue, since confirmedKind != nil
// means the coverage floor itself never runs) returns the exact-match
// candidate. "Ask Dev" == the node's own label forces a deterministic
// commit regardless of confidence calibration, the same isolation
// TestResolveSubjects_SearchKindCoverageTruncationNeverBlocksAnUnrelatedCommit
// already relies on.
func TestResolveSubjects_ConfirmedKindRescueFiresWhenPoolEmptyAfterFiltering(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "wi_1", Label: "Ask Dev"}
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.9, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{"Ask Dev": {}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"Ask Dev": {contextfabric.SubjectWorkItem: {node}},
		},
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectWorkItem}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), backend.deps(), confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(backend.searchKindCalls) == 0 {
		t.Fatal("searchKindCalls is empty, want the confirmed-kind rescue to have queried SearchKind once ordinary search alone left the confirmed-kind pool empty")
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != subject {
		t.Fatalf("resolution.Committed = %#v, want the rescued exact-match work_item candidate committed -- a receipt-confirmed kind whose only route into the pool was a kind-scoped search must not be a guaranteed no_match", resolution.Committed)
	}
}

// TestResolveSubjects_ConfirmedKindRescueSkippedWhenPoolAlreadySatisfied is
// team-lead's own required direction (b): the negative control pinning
// CHAOS-3900 P1.D's "nothing left to disambiguate" optimization. When the
// confirmed kind's own candidates are ALREADY in the ordinary pool, the
// rescue must be a true no-op -- zero extra SearchKind calls, not merely a
// call that happens not to change the outcome.
//
// This replaces the ORIGINAL (pre-CHAOS-4132) version of this scenario,
// which set searchResults to return NOTHING at all for its term -- under
// this fix that fixture is actually the (a) rescue-fires case above, not a
// no-op one, so the fixture here now puts the confirmed kind's own
// candidate directly in the ordinary pool to test what the doc comment
// actually claims.
func TestResolveSubjects_ConfirmedKindRescueSkippedWhenPoolAlreadySatisfied(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "wi_1", Label: "Ask Dev"}
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.9, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{"Ask Dev": {node}},
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectWorkItem}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), backend.deps(), confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(backend.searchKindCalls) != 0 {
		t.Fatalf("searchKindCalls = %#v, want none -- the confirmed kind's candidates were already in the ordinary pool", backend.searchKindCalls)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != subject {
		t.Fatalf("resolution.Committed = %#v, want the already-present exact-match candidate committed regardless", resolution.Committed)
	}
}

// TestResolveSubjects_ConfirmedKindRescueTracesItsOwnEvent pins the required
// telemetry: the "confirmed_kind_rescue" stage event exists ONLY when the
// rescue was actually attempted, and reports Fired/ResultCount accurately.
func TestResolveSubjects_ConfirmedKindRescueTracesItsOwnEvent(t *testing.T) {
	t.Parallel()
	t.Run("fires and traces a result", func(t *testing.T) {
		subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "wi_1", Label: "Ask Dev"}
		node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.9, "*")
		tracer := &chaos4120Tracer{}
		backend := &fakeGraphBackend{
			enableSearchKind: true,
			searchResults:    map[string][]CandidateNode{"Ask Dev": {}},
			searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
				"Ask Dev": {contextfabric.SubjectWorkItem: {node}},
			},
		}
		deps := backend.deps()
		deps.ResolutionTracer = tracer
		confirmed := &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectWorkItem}
		if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), deps, confirmed, nil); err != nil {
			t.Fatalf("ResolveSubjects() error = %v", err)
		}
		events := tracer.eventsByStage("confirmed_kind_rescue")
		if len(events) != 1 {
			t.Fatalf("got %d confirmed_kind_rescue events, want exactly 1", len(events))
		}
		if !events[0].ConfirmedKindRescueFired {
			t.Error("ConfirmedKindRescueFired = false, want true -- the rescue found a candidate")
		}
		if events[0].ConfirmedKindRescueResultCount != 1 {
			t.Errorf("ConfirmedKindRescueResultCount = %d, want 1", events[0].ConfirmedKindRescueResultCount)
		}
		if events[0].ConfirmedKindRescueTruncated {
			t.Error("ConfirmedKindRescueTruncated = true, want false -- this fixture never reported truncation")
		}
	})
	t.Run("traces its own truncation", func(t *testing.T) {
		subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "wi_1", Label: "Ask Dev"}
		node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.9, "*")
		tracer := &chaos4120Tracer{}
		backend := &fakeGraphBackend{
			enableSearchKind: true,
			searchResults:    map[string][]CandidateNode{"Ask Dev": {}},
			searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
				"Ask Dev": {contextfabric.SubjectWorkItem: {node}},
			},
			searchKindTruncated: true,
		}
		deps := backend.deps()
		deps.ResolutionTracer = tracer
		confirmed := &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectWorkItem}
		if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), deps, confirmed, nil); err != nil {
			t.Fatalf("ResolveSubjects() error = %v", err)
		}
		events := tracer.eventsByStage("confirmed_kind_rescue")
		if len(events) != 1 {
			t.Fatalf("got %d confirmed_kind_rescue events, want exactly 1", len(events))
		}
		if !events[0].ConfirmedKindRescueTruncated {
			t.Error("ConfirmedKindRescueTruncated = false, want true -- the fixture reported truncation")
		}
	})
	t.Run("absent when the rescue was never attempted", func(t *testing.T) {
		subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "wi_1", Label: "Ask Dev"}
		node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.9, "*")
		tracer := &chaos4120Tracer{}
		backend := &fakeGraphBackend{
			enableSearchKind: true,
			searchResults:    map[string][]CandidateNode{"Ask Dev": {node}},
		}
		deps := backend.deps()
		deps.ResolutionTracer = tracer
		confirmed := &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectWorkItem}
		if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), deps, confirmed, nil); err != nil {
			t.Fatalf("ResolveSubjects() error = %v", err)
		}
		if events := tracer.eventsByStage("confirmed_kind_rescue"); len(events) != 0 {
			t.Fatalf("got %d confirmed_kind_rescue events, want 0 -- the rescue was never needed", len(events))
		}
	})
}

// TestResolveSubjects_ConfirmedKindRescueExactMatchSurvivesItsOwnTruncation
// pins the ONE case where a truncated rescue still commits: an exact-label
// match. This is NOT because the rescue's own truncation is ignored (codex
// review round 1 folded it into searchTruncated -- see
// ConfirmedKindRescueTruncated's own doc comment) but because exact_index
// commits sit AHEAD of the searchTruncated check in resolution.go's own
// switch, for every caller, unconditionally (CHAOS-3810's own "term
// equality against the subject's own identity data survives ordinary
// search truncation" precedent) -- this rescue does not change that
// ordering, it only supplies the candidate exact_index reads.
func TestResolveSubjects_ConfirmedKindRescueExactMatchSurvivesItsOwnTruncation(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "wi_1", Label: "Ask Dev"}
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.9, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{"Ask Dev": {}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"Ask Dev": {contextfabric.SubjectWorkItem: {node}},
		},
		searchKindTruncated: true,
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectWorkItem}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), backend.deps(), confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != subject {
		t.Fatalf("resolution.Committed = %#v, want the rescued exact-match candidate committed -- exact_index precedes the searchTruncated check regardless of what fed the pool", resolution.Committed)
	}
}

// TestResolveSubjects_ConfirmedKindRescueTruncationBlocksALoneCandidateCommit
// is codex review round 1's own repro-and-fix pin (MEDIUM, confirmed): a
// NON-exact (lexical/vector, term != label) lone candidate that would
// otherwise clear the lone-floor confidence gate must NOT auto-commit when
// the rescue that found it was itself truncated -- a truncated rescue call
// may have cut off a genuine rival of the SAME confirmed kind, and that
// risk is exactly what searchTruncated exists to gate on. Before this fix,
// the rescue's own truncated signal was discarded entirely, which would
// have let this exact scenario read as a falsely-confident commit.
func TestResolveSubjects_ConfirmedKindRescueTruncationBlocksALoneCandidateCommit(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "wi_1", Label: "Some Other Work Item Entirely"}
	// relevance 0.85 clears DefaultCommitGatePolicy's LoneFloor (0.72) --
	// this candidate WOULD auto-commit via lone_floor if untruncated. The
	// search term ("alpha") deliberately does not equal the candidate's own
	// label, so this never reaches the exact-index tier
	// (TestResolveSubjects_ConfirmedKindRescueExactMatchSurvivesItsOwnTruncation
	// covers that separate, unaffected path).
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.85, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{"alpha": {}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"alpha": {contextfabric.SubjectWorkItem: {node}},
		},
		searchKindTruncated: true,
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectWorkItem}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("alpha"), backend.deps(), confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want no auto-commit -- the rescue's own truncation must gate a non-exact lone candidate exactly like ordinary search truncation would", resolution.Committed)
	}
}
