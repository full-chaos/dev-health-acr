package graphrank

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// This file pins CHAOS-4155 Phase 1's own regression bar: the shadow
// kind-scoped vector census must be wired at EXACTLY the right spot
// (buildConfirmedKindScopedSnapshot's confirmedKindScopePlanIncomplete
// branch), must be a true no-op absent a deployment opting in, and --
// the load-bearing Phase 1 guarantee -- must NEVER change
// buildConfirmedKindScopedSnapshot's own returned state, regardless of what
// the shadow arm reports. See chaos4155_confirmed_kind_vector_scope.go's
// own doc comment for the full design.

// TestAttemptConfirmedKindVectorCensus_NilHookNotAttempted proves the
// nil-safe convention every other optional ResolveDeps hook in this
// package already follows: a deployment that never wires
// ConfirmedKindVectorCensus gets NotAttempted at zero cost.
func TestAttemptConfirmedKindVectorCensus_NilHookNotAttempted(t *testing.T) {
	t.Parallel()
	got := attemptConfirmedKindVectorCensus(context.Background(), ResolveDeps{}, contextfabric.SubjectWorkItem, []string{"outage"})
	if got.State != ConfirmedKindVectorScopeNotAttempted {
		t.Fatalf("attemptConfirmedKindVectorCensus() = %+v, want State=%q", got, ConfirmedKindVectorScopeNotAttempted)
	}
}

// TestAttemptConfirmedKindVectorCensus_EmptyTermsNeverCallsTheHook proves an
// empty term list short-circuits BEFORE the hook runs -- a backend
// implementation must never see a call it cannot do anything useful with,
// and must never be charged for one.
func TestAttemptConfirmedKindVectorCensus_EmptyTermsNeverCallsTheHook(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		enableConfirmedKindVectorCensus: true,
		confirmedKindVectorCensusResult: ConfirmedKindVectorCensusOutcome{State: ConfirmedKindVectorScopeComplete},
	}
	got := attemptConfirmedKindVectorCensus(context.Background(), backend.deps(), contextfabric.SubjectWorkItem, nil)
	if got.State != ConfirmedKindVectorScopeNotAttempted {
		t.Fatalf("attemptConfirmedKindVectorCensus() = %+v, want State=%q", got, ConfirmedKindVectorScopeNotAttempted)
	}
	if len(backend.confirmedKindVectorCensusCalls) != 0 {
		t.Fatalf("confirmedKindVectorCensusCalls = %#v, want zero calls for an empty term list", backend.confirmedKindVectorCensusCalls)
	}
}

// TestAttemptConfirmedKindVectorCensus_DelegatesKindAndTermsAndOutcome
// proves the pass-through is faithful in both directions: the exact kind
// and term slice reach the hook, and the hook's own outcome is returned
// unmodified.
func TestAttemptConfirmedKindVectorCensus_DelegatesKindAndTermsAndOutcome(t *testing.T) {
	t.Parallel()
	want := ConfirmedKindVectorCensusOutcome{
		State: ConfirmedKindVectorScopeComplete, PopulationCount: 3, EnumeratedCount: 3,
		QueryCount: 2, QueriesScored: 2, ComparisonCount: 6, SnapshotStable: true, DurationMS: 5,
	}
	backend := &fakeGraphBackend{enableConfirmedKindVectorCensus: true, confirmedKindVectorCensusResult: want}
	terms := []string{"widget rollout", "outage"}
	got := attemptConfirmedKindVectorCensus(context.Background(), backend.deps(), contextfabric.SubjectWorkItem, terms)
	// CHAOS-4311: reflect.DeepEqual, not ==, now that Rivals ([]CandidateNode)
	// makes this struct non-comparable.
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attemptConfirmedKindVectorCensus() = %+v, want %+v", got, want)
	}
	if len(backend.confirmedKindVectorCensusCalls) != 1 {
		t.Fatalf("confirmedKindVectorCensusCalls = %#v, want exactly 1 call", backend.confirmedKindVectorCensusCalls)
	}
	call := backend.confirmedKindVectorCensusCalls[0]
	if call.kind != contextfabric.SubjectWorkItem {
		t.Fatalf("call.kind = %q, want %q", call.kind, contextfabric.SubjectWorkItem)
	}
	if len(call.terms) != 2 || call.terms[0] != terms[0] || call.terms[1] != terms[1] {
		t.Fatalf("call.terms = %#v, want %#v", call.terms, terms)
	}
}

// TestBuildConfirmedKindScopedSnapshot_VectorCensusNeverChangesReturnedScopeState
// is the LOAD-BEARING Phase 1 guarantee: even when the shadow census
// reports Complete (the outcome a FUTURE Phase 2 change would need to act
// on), buildConfirmedKindScopedSnapshot's own returned state stays
// confirmedKindScopePlanIncomplete -- the commit gate's decision is
// byte-identical to pre-CHAOS-4155 behavior. Planted-defect proof: flipping
// this function's `case deps.VectorMechanismConfigured:` branch to instead
// return the shadow arm's own state (a plausible-looking Phase 2 mistake
// made too early) is exactly what this test exists to catch -- it FAILS
// the instant scopeState stops being hardcoded to plan_incomplete here.
func TestBuildConfirmedKindScopedSnapshot_VectorCensusNeverChangesReturnedScopeState(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	backend := &fakeGraphBackend{
		enableSearchKind:                true,
		searchKindResults:               map[string]map[contextfabric.SubjectKind][]CandidateNode{term: {kind: {}}},
		vectorMechanismConfigured:       true,
		enableConfirmedKindVectorCensus: true,
		// Complete, non-zero counts -- the MOST tempting outcome to
		// mistakenly let flip scopeState, deliberately chosen over a
		// duller fixture.
		confirmedKindVectorCensusResult: ConfirmedKindVectorCensusOutcome{
			State: ConfirmedKindVectorScopeComplete, PopulationCount: 1, EnumeratedCount: 1,
			QueryCount: 1, QueriesScored: 1, SnapshotStable: true,
		},
	}
	_, _, _, _, _, state, _, _, vectorCensus, err := buildConfirmedKindScopedSnapshot(
		context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), backend.deps(),
		[]string{term}, nil, false, kind, 10)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if state != confirmedKindScopePlanIncomplete {
		t.Fatalf("state = %q, want %q -- CHAOS-4155 Phase 1 must never let the shadow vector census flip the returned scopeState", state, confirmedKindScopePlanIncomplete)
	}
	if vectorCensus.State != ConfirmedKindVectorScopeComplete {
		t.Fatalf("vectorCensus.State = %q, want %q -- the shadow outcome must still be CARRIED OUT for telemetry even though it does not change state", vectorCensus.State, ConfirmedKindVectorScopeComplete)
	}
}

// TestBuildConfirmedKindScopedSnapshot_VectorCensusOnlyInvokedWhenLexicalCompleteAndVectorConfigured
// proves the shadow arm fires in EXACTLY one branch of the state machine --
// never when the lexical pass itself truncated/degraded (an incomplete
// lexical read invalidates the whole snapshot regardless of what a vector
// census might add), and never when no vector mechanism exists at all
// (nothing for it to prove).
func TestBuildConfirmedKindScopedSnapshot_VectorCensusOnlyInvokedWhenLexicalCompleteAndVectorConfigured(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"

	newBackend := func(searchTruncated, searchDegraded, vectorConfigured bool) *fakeGraphBackend {
		return &fakeGraphBackend{
			enableSearchKind:                true,
			searchKindResults:               map[string]map[contextfabric.SubjectKind][]CandidateNode{term: {kind: {}}},
			searchKindTruncated:             searchTruncated,
			searchKindDegraded:              searchDegraded,
			vectorMechanismConfigured:       vectorConfigured,
			enableConfirmedKindVectorCensus: true,
			confirmedKindVectorCensusResult: ConfirmedKindVectorCensusOutcome{State: ConfirmedKindVectorScopeComplete},
		}
	}

	cases := []struct {
		name             string
		searchTruncated  bool
		searchDegraded   bool
		vectorConfigured bool
		wantInvoked      bool
		wantState        string
	}{
		{"truncated lexical, vector configured -> not invoked", true, false, true, false, confirmedKindScopeTruncated},
		{"degraded lexical, vector configured -> not invoked", false, true, true, false, confirmedKindScopeFailed},
		{"clean lexical, vector NOT configured -> not invoked", false, false, false, false, confirmedKindScopeComplete},
		{"clean lexical, vector configured -> invoked", false, false, true, true, confirmedKindScopePlanIncomplete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			backend := newBackend(tc.searchTruncated, tc.searchDegraded, tc.vectorConfigured)
			_, _, _, _, _, state, _, _, vectorCensus, err := buildConfirmedKindScopedSnapshot(
				context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), backend.deps(),
				[]string{term}, nil, false, kind, 10)
			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
			if state != tc.wantState {
				t.Fatalf("state = %q, want %q", state, tc.wantState)
			}
			invoked := len(backend.confirmedKindVectorCensusCalls) > 0
			if invoked != tc.wantInvoked {
				t.Fatalf("shadow census invoked = %v, want %v (calls: %#v)", invoked, tc.wantInvoked, backend.confirmedKindVectorCensusCalls)
			}
			if !tc.wantInvoked && vectorCensus.State != "" {
				t.Fatalf("vectorCensus = %+v, want the zero value (State==\"\", read as not-attempted) when the shadow arm was never invoked", vectorCensus)
			}
		})
	}
}
