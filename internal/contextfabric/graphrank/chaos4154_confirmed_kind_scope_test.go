package graphrank

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// This file is CHAOS-4154's own regression bar: the 9-item list sol-max's
// consult ruling required (ticket comment thread), one test each, in the
// SAME order the ruling states them. See chaos4154_confirmed_kind_scope.go
// for the mechanism these tests pin.

// lastDecisionEvent returns the LAST "decision"-stage event a recordingTracer
// captured -- unlike recordingTracer.decision() (first match), this ticket's
// own mechanism can cause a SECOND decision event to fire for the same
// resolution (the scoped re-decision), exactly like the pre-existing
// evidence-census re-decision already does; the LAST one is always the one
// that decided the returned resolution.
func lastDecisionEvent(events []ResolutionTraceEvent) (ResolutionTraceEvent, bool) {
	var found ResolutionTraceEvent
	ok := false
	for _, event := range events {
		if event.Stage == "decision" {
			found = event
			ok = true
		}
	}
	return found, ok
}

// confirmedKindScopeEvent returns the "confirmed_kind_scope"-stage event a
// recordingTracer captured, if any.
func confirmedKindScopeEvent(events []ResolutionTraceEvent) (ResolutionTraceEvent, bool) {
	for _, event := range events {
		if event.Stage == "confirmed_kind_scope" {
			return event, true
		}
	}
	return ResolutionTraceEvent{}, false
}

// TestResolveSubjects_ConfirmedKindScope_Case57ShapeClearsStaleGlobalTruncation
// is regression item 1: "Case 57 with scoped completeness: stale global
// truncation no longer blocks gate evaluation; unchanged thresholds still
// determine the result."
//
// Mirrors TestResolveSubjects_ConfirmedKindRescueFiresWhenPoolEmptyAfterFiltering's
// own case-57 shape (CHAOS-4132's own diagnosed corpus row: a confirmed
// work_item kind whose only route into the pool is a kind-scoped search),
// with the ONE additional ingredient case 57 itself has and that test does
// not: an EARLIER, UNRELATED unscoped stage (an ordinary Search() call) also
// truncated, tripping the resolution-wide bit BEFORE the rescue ever runs --
// pre-CHAOS-4154, this is exactly "rescue fires, pool complete,
// clarification anyway" (team-lead's own framing). The candidate is a
// LEXICAL (non-exact-label) match, deliberately: an exact-label match would
// already commit via CHAOS-3810's own pre-existing exact_index carve-out in
// the FIRST (unscoped) pass regardless of truncation, which would prove
// nothing about this ticket's own mechanism.
func TestResolveSubjects_ConfirmedKindScope_Case57ShapeClearsStaleGlobalTruncation(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "wi_1", Label: "Widget Rollout Backend Task"}
	node := candidateNode(kind, subject.CanonicalID, subject.Label, 0.9, "*")
	// rival is a SAME-KIND candidate ordinary (unscoped) search finds for
	// this SAME term but the isolated exhaustive SearchKind pass does NOT
	// (codex review finding, Medium/High confidence, confirmed: without a
	// competing unscoped candidate, a broken implementation that simply
	// clears searchTruncated and reuses the existing unscoped pool would
	// pass this test too -- it happens to hold only the SAME one
	// candidate here). With rival present, that broken hypothesis commits
	// the WRONG subject (rival, the only thing in the unscoped pool -- the
	// CHAOS-4132 rescue never fires here since the pool is non-empty after
	// confirmed-kind filtering), while a genuinely isolated snapshot
	// (built ONLY from this call's own exhaustive SearchKind results,
	// which never mention rival) commits the RIGHT one.
	rival := candidateNode(kind, "wi_rival", "Something Else Entirely", 0.85, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{term: {rival}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			term: {kind: {node}},
		},
		// The EARLIER, UNRELATED unscoped stage that trips the
		// resolution-wide bit -- case 57's own shape.
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
		t.Fatalf("resolution.Committed = %#v, want the ISOLATED kind-scoped candidate to commit despite the stale, unrelated global truncation bit -- never the unscoped-pool rival", resolution.Committed)
	}
	event, ok := lastDecisionEvent(tracer.events)
	if !ok || event.CommitGate != "lone_floor" {
		t.Fatalf("decision event = %+v, ok=%v, want an unchanged lone_floor gate to have decided this", event, ok)
	}
}

// TestResolveFromMergedCandidatesWithGateAndBasis_PopulationBasisInvariant
// is regression item 9: "Every statistical commit with
// resolution_search_truncated=true necessarily reports
// population_basis=confirmed_kind_scoped_complete." Exercised at the gate
// function itself (not the full ResolveSubjects orchestration) so the THREE
// populations sol's closed vocabulary distinguishes can be pinned precisely
// and independently: an ordinary untruncated statistical commit, a
// confirmed-kind-scoped commit, and the pre-existing (unrelated)
// CHAOS-3810 exact-label-survives-truncation carve-out, which must NOT be
// mislabeled as either basis.
func TestResolveFromMergedCandidatesWithGateAndBasis_PopulationBasisInvariant(t *testing.T) {
	t.Parallel()
	t.Run("ordinary untruncated statistical commit", func(t *testing.T) {
		tracer := &recordingTracer{}
		lone := corroborationCandidate("ordinary", 0.80, contextfabric.MatchLexical)
		bySubject := map[string]contextfabric.SubjectCandidate{SubjectKey(lone.Subject): lone}
		ResolveFromMergedCandidatesWithGateAndBasis(bySubject, map[string]string{}, map[string]bool{}, 10, true, false,
			nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, tracer, "req-basis-1", "", false)
		event, ok := lastDecisionEvent(tracer.events)
		if !ok || event.Outcome != "committed" || event.PopulationBasis != "resolution_wide_untruncated" {
			t.Fatalf("event = %+v, ok=%v, want PopulationBasis=resolution_wide_untruncated", event, ok)
		}
	})
	t.Run("confirmed-kind-scoped commit", func(t *testing.T) {
		tracer := &recordingTracer{}
		lone := corroborationCandidate("scoped", 0.80, contextfabric.MatchLexical)
		bySubject := map[string]contextfabric.SubjectCandidate{SubjectKey(lone.Subject): lone}
		// searchTruncated=false: THIS call's own isolated population is the
		// proven-complete census -- confirmedKindScopedBasis=true is what
		// tells the trace that, mirroring resolve.go's own call site.
		ResolveFromMergedCandidatesWithGateAndBasis(bySubject, map[string]string{}, map[string]bool{}, 10, true, false,
			nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, tracer, "req-basis-2", "", true)
		event, ok := lastDecisionEvent(tracer.events)
		if !ok || event.Outcome != "committed" || event.PopulationBasis != "confirmed_kind_scoped_complete" {
			t.Fatalf("event = %+v, ok=%v, want PopulationBasis=confirmed_kind_scoped_complete", event, ok)
		}
	})
	t.Run("exact-label-survives-truncation carve-out reports none", func(t *testing.T) {
		tracer := &recordingTracer{}
		exact := corroborationCandidate("exact", 1, contextfabric.MatchExact)
		bySubject := map[string]contextfabric.SubjectCandidate{SubjectKey(exact.Subject): exact}
		ResolveFromMergedCandidatesWithGateAndBasis(bySubject, map[string]string{}, map[string]bool{}, 10, true, true, /* searchTruncated */
			nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, tracer, "req-basis-3", "", false)
		event, ok := lastDecisionEvent(tracer.events)
		if !ok || event.Outcome != "committed" || event.CommitGate != "exact_index" || event.PopulationBasis != "none" {
			t.Fatalf("event = %+v, ok=%v, want CommitGate=exact_index and PopulationBasis=none -- CHAOS-3810's own carve-out trusts string equality, not population completeness", event, ok)
		}
	})
}

// TestResolveSubjects_ConfirmedKindScope_UnscopedInflatedEntryCannotChangeOutcome
// is regression item 2: "Same-subject unscoped score inflation cannot
// change the scoped outcome." Subject A is found by ORDINARY (unscoped)
// search at a high, non-exact lexical confidence (0.99, well above
// LoneFloor) -- exactly the shape MergeCandidates' own "higher confidence
// wins" rule could otherwise let leak across populations -- while the
// ISOLATED, exhaustive SearchKind pass finds the SAME subject at a much
// lower confidence (0.50, below LoneFloor). If the scoped gate call ever
// consulted the unscoped candidatesBySubject entry for this subject
// (directly, or through a future "helpful" merge), it would commit; the
// isolated snapshot has no candidatesBySubject parameter at all, so it
// cannot.
func TestResolveSubjects_ConfirmedKindScope_UnscopedInflatedEntryCannotChangeOutcome(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	// Ordinary (unscoped) search's own high-confidence, non-exact-label
	// find for subject A -- never an exact match, so it cannot short-circuit
	// via CHAOS-3810's own exact_index carve-out in the first (unscoped)
	// pass; it must fall to the searchTruncated branch, exactly like a real
	// stalled resolution would.
	inflatedA := candidateNode(kind, "wi_a", "Something Entirely Different", 0.99, "*")
	// The isolated, scoped pass's OWN (honest, lower) finding for the SAME
	// subject -- below LoneFloor on its own.
	scopedA := candidateNode(kind, "wi_a", "Something Entirely Different", 0.50, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{term: {inflatedA}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			term: {kind: {scopedA}},
		},
		searchTruncated: true,
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), backend.deps(), confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want ambiguous -- the scoped census's own 0.50 confidence (below LoneFloor) must decide this, never the unscoped leg's 0.99 entry for the SAME subject", resolution.Committed)
	}
}

// TestResolveSubjects_ConfirmedKindScope_UnscopedOnlyRivalDoesNotSurvive is
// regression item 3: "Unscoped-only same-kind subjects do not survive into
// the authoritative snapshot." Subject A is found ONLY by ordinary
// (unscoped) search -- the isolated, exhaustive SearchKind pass reports
// nothing at all for the confirmed kind. The isolated snapshot must be
// empty, never silently inheriting A from the unscoped pool, so the
// resolution stays exactly as ambiguous as it was before this ticket.
func TestResolveSubjects_ConfirmedKindScope_UnscopedOnlyRivalDoesNotSurvive(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	unscopedOnly := candidateNode(kind, "wi_unscoped_only", "Something Entirely Different", 0.95, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{term: {unscopedOnly}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			term: {kind: {}}, // the isolated, exhaustive pass finds NOTHING
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
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want ambiguous -- an unscoped-only same-kind subject must never survive into the isolated snapshot", resolution.Committed)
	}
	event, ok := confirmedKindScopeEvent(tracer.events)
	if !ok || event.ConfirmedKindScopeState != confirmedKindScopeComplete || event.ConfirmedKindScopeCandidateCount != 0 {
		t.Fatalf("confirmed_kind_scope event = %+v, ok=%v, want state=complete (the pass itself succeeded) with zero candidates (nothing scoped survived)", event, ok)
	}
}

// TestResolveSubjects_ConfirmedKindScope_LiveVectorMechanismBlocksFallbackCompleteness
// is regression item 4: "A rival found only through the question/vector
// channel is included or prevents completeness." The SearchKind FALLBACK
// mechanism has no kind-scoped vector arm (see this file's own doc
// comment), so it can never PROVE a vector-surfaced rival was included --
// its only sound option is the other half of the disjunction: prevent the
// completeness claim outright whenever a live vector mechanism exists.
func TestResolveSubjects_ConfirmedKindScope_LiveVectorMechanismBlocksFallbackCompleteness(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "wi_1", Label: "Widget Rollout Backend Task"}
	node := candidateNode(kind, subject.CanonicalID, subject.Label, 0.9, "*")
	backend := &fakeGraphBackend{
		enableSearchKind:          true,
		searchResults:             map[string][]CandidateNode{term: {}},
		searchKindResults:         map[string]map[contextfabric.SubjectKind][]CandidateNode{term: {kind: {node}}},
		searchTruncated:           true,
		vectorMechanismConfigured: true, // a live vector mechanism exists for this deployment
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), deps, confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want ambiguous -- a live vector mechanism means the fallback's lexical-only exhaustion cannot prove completeness, even though the lexical pass itself found a clean, otherwise-decisive candidate", resolution.Committed)
	}
	event, ok := confirmedKindScopeEvent(tracer.events)
	if !ok || event.ConfirmedKindScopeState != confirmedKindScopePlanIncomplete {
		t.Fatalf("confirmed_kind_scope event = %+v, ok=%v, want state=plan_incomplete", event, ok)
	}
}

// TestBuildConfirmedKindScopedSnapshot_TruncatedOrDegradedBlocksCompleteness
// is regression item 5: "Any scoped truncation or failure blocks the
// exception." Both a truncated and a degraded exhaustive SearchKind call
// must refuse the completeness claim, exactly like a live vector mechanism
// does -- an incomplete read must never masquerade as a proof. Uses THREE
// terms with the disqualifying signal on the FIRST one (codex review
// finding, Medium/High confidence, confirmed: a single-term fixture cannot
// distinguish "walks every term" from "stops after the first result"), and
// asserts every term was still queried -- pinning the do-not-build's own
// "do not retain the early exit on the completeness-producing path".
func TestBuildConfirmedKindScopedSnapshot_TruncatedOrDegradedBlocksCompleteness(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	terms := []string{"alpha", "beta", "gamma"}
	t.Run("truncated", func(t *testing.T) {
		t.Parallel()
		backend := &fakeGraphBackend{enableSearchKind: true, searchKindTruncated: true}
		_, _, _, _, _, state, _, _, _, err := buildConfirmedKindScopedSnapshot(
			context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), backend.deps(),
			terms, nil, false, kind, 10)
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		if state != confirmedKindScopeTruncated {
			t.Fatalf("state = %q, want %q", state, confirmedKindScopeTruncated)
		}
		if len(backend.searchKindCalls) != len(terms) {
			t.Fatalf("searchKindCalls = %d, want %d -- no early exit even once truncation is already known", len(backend.searchKindCalls), len(terms))
		}
	})
	t.Run("degraded", func(t *testing.T) {
		t.Parallel()
		backend := &fakeGraphBackend{enableSearchKind: true, searchKindDegraded: true}
		_, _, _, _, _, state, _, _, _, err := buildConfirmedKindScopedSnapshot(
			context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), backend.deps(),
			terms, nil, false, kind, 10)
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		if state != confirmedKindScopeFailed {
			t.Fatalf("state = %q, want %q", state, confirmedKindScopeFailed)
		}
		if len(backend.searchKindCalls) != len(terms) {
			t.Fatalf("searchKindCalls = %d, want %d -- no early exit even once degradation is already known", len(backend.searchKindCalls), len(terms))
		}
	})
	t.Run("backend error propagates, never silently downgraded", func(t *testing.T) {
		t.Parallel()
		backend := &fakeGraphBackend{enableSearchKind: true, searchKindErr: errors.New("transient backend failure")}
		_, _, _, _, _, _, _, _, _, err := buildConfirmedKindScopedSnapshot(
			context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), backend.deps(),
			terms, nil, false, kind, 10)
		if err == nil {
			t.Fatal("error = nil, want the backend failure propagated")
		}
	})
}

// TestResolveSubjects_ConfirmedKindScope_UntruncatedResolutionUnchanged is
// regression item 6: "The resolution-wide untruncated path remains
// unchanged." When searchTruncated is false throughout, this ticket's
// mechanism must never even attempt to run (its own trigger condition in
// resolve.go requires searchTruncated=true) -- zero extra SearchKind calls,
// and the ordinary gate decides exactly as it did before this ticket.
func TestResolveSubjects_ConfirmedKindScope_UntruncatedResolutionUnchanged(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "wi_1", Label: "Widget Rollout Backend Task"}
	node := candidateNode(kind, subject.CanonicalID, subject.Label, 0.9, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{term: {node}}, // ordinary search already finds it, untruncated
		// searchKindResults intentionally left empty/unset -- if the
		// mechanism ran, it would find nothing and this test would still
		// (accidentally) pass, so the real assertion is searchKindCalls
		// below.
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), backend.deps(), confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != subject {
		t.Fatalf("resolution.Committed = %#v, want the ordinary lone_floor commit, unchanged", resolution.Committed)
	}
	if len(backend.searchKindCalls) != 0 {
		t.Fatalf("searchKindCalls = %#v, want ZERO -- an untruncated resolution must never attempt this ticket's mechanism at all", backend.searchKindCalls)
	}
}

// TestResolveSubjects_ConfirmedKindScope_NilConfirmedKindNeverTriggers is
// regression item 7: "Unconfirmed-hint paired requests remain behaviorally
// identical." confirmedKind==nil structurally excludes every inferred/
// unconfirmed-hint request from this ticket's entire mechanism (CHAOS-4039
// noninterference, by construction) -- even with searchTruncated=true and a
// SearchKind backend available, this ticket's mechanism must never even be
// attempted (no "confirmed_kind_scope" trace event) and the ordinary
// ambiguous outcome must stand. Uses SubjectProject (an
// isAliasLookupScopedKind, effectiveCoverageFloorKinds member) precisely so
// the PRE-EXISTING CHAOS-4038 coverage floor's own, legitimate,
// confirmedKind==nil-gated SearchKind calls are exercised too -- proving
// this ticket's mechanism adds ZERO calls on top of that already-existing
// behavior, not merely that some other assertion happens to hold.
func TestResolveSubjects_ConfirmedKindScope_NilConfirmedKindNeverTriggers(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectProject
	term := "widget rollout"
	node := candidateNode(kind, "project_1", "Something Entirely Different", 0.9, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{term: {node}},
		searchTruncated:  true,
	}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want ambiguous -- an unconfirmed request must never reach this ticket's mechanism", resolution.Committed)
	}
	if _, ok := confirmedKindScopeEvent(tracer.events); ok {
		t.Fatal("a confirmed_kind_scope trace event fired for a confirmedKind==nil resolution -- CHAOS-4039 noninterference requires this mechanism stay structurally unreachable")
	}
}

// TestResolveSubjects_ConfirmedKindScope_IdentityCensusNeverBypassesVectorGate
// pins the fix for a real soundness gap found during review: an earlier
// version of this mechanism treated deps.AliasLookup's identity-universe
// enumeration (isAliasLookupScopedKind: repository/project/team) as an
// INDEPENDENT completeness proof that fired even on a vector-enabled
// deployment, on the theory that a full row enumeration structurally
// subsumes the vector channel. It does not: AliasLookup only matches
// label/alias/provider-alias EQUALITY, never a team's description/
// project_keys, a project's state, or a repository's tags -- all of which
// falkorgraph's own per-kind search-text composition (search_text.go)
// DOES index for ordinary lexical/vector retrieval -- and, independent of
// that gap, exact-equality row enumeration says nothing about EMBEDDING
// similarity at all. So the identity census must remain a confidence-
// quality addition ONLY: even when it supplies a genuine, otherwise-
// commit-eligible candidate, a live vector mechanism must still block the
// scoped snapshot from ever being treated as complete.
func TestResolveSubjects_ConfirmedKindScope_IdentityCensusNeverBypassesVectorGate(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectProject
	term := "ask dev platform"
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "project_1", Label: "Ask Dev Platform Services"}
	// A non-exact alias-style match (label differs from term, moderate
	// relevance): if this committed in the FIRST (unscoped) pass at all, it
	// would have to be through identity_fast_path or exact_index, neither of
	// which this shape qualifies for (Confidence != 1) -- so pass 1 must
	// fall to the searchTruncated branch, exactly like a real stalled
	// resolution.
	claimant := candidateNode(kind, subject.CanonicalID, subject.Label, 0.80, "*")
	backend := &fakeGraphBackend{
		enableAliasLookup:         true,
		aliasLookupComplete:       true,
		aliasLookupClaimants:      map[string][]CandidateNode{term: {claimant}},
		searchResults:             map[string][]CandidateNode{term: {}},
		enableSearchKind:          true, // the SearchKind pass runs too, and finds nothing new
		searchKindResults:         map[string]map[contextfabric.SubjectKind][]CandidateNode{term: {kind: {}}},
		searchTruncated:           true,
		vectorMechanismConfigured: true, // a live vector mechanism exists for this deployment
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), deps, confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want ambiguous -- a live vector mechanism must block completeness even though the identity census supplied a genuine, otherwise-eligible candidate", resolution.Committed)
	}
	event, ok := confirmedKindScopeEvent(tracer.events)
	if !ok || event.ConfirmedKindScopeState != confirmedKindScopePlanIncomplete {
		t.Fatalf("confirmed_kind_scope event = %+v, ok=%v, want state=plan_incomplete", event, ok)
	}
}

// TestBuildConfirmedKindScopedSnapshot_IdentityCensusIsConfidenceQualityOnly
// is the unit-level companion: the identity-census contribution must merge
// into the SAME pool the SearchKind pass builds (so a genuinely eligible
// candidate benefits from the identity-trust bump), but the returned STATE
// must be governed entirely by the SearchKind pass's own outcome. Proven
// here by holding the identity census fixed and only toggling
// VectorMechanismConfigured.
func TestBuildConfirmedKindScopedSnapshot_IdentityCensusIsConfidenceQualityOnly(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectProject
	term := "ask dev platform"
	claimant := candidateNode(kind, "project_1", "Ask Dev Platform Services", 0.80, "*")
	newBackend := func(vectorConfigured bool) *fakeGraphBackend {
		return &fakeGraphBackend{
			enableSearchKind:          true,
			searchKindResults:         map[string]map[contextfabric.SubjectKind][]CandidateNode{term: {kind: {}}},
			vectorMechanismConfigured: vectorConfigured,
		}
	}
	aliasClaimantsByTerm := map[string][]CandidateNode{term: {claimant}}

	vectorOff := newBackend(false)
	pool, _, _, _, _, state, _, _, _, err := buildConfirmedKindScopedSnapshot(
		context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), vectorOff.deps(),
		[]string{term}, aliasClaimantsByTerm, true, kind, 10)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if state != confirmedKindScopeComplete {
		t.Fatalf("state = %q, want %q (vector off, SearchKind clean)", state, confirmedKindScopeComplete)
	}
	if len(pool) != 1 {
		t.Fatalf("pool = %#v, want the identity-census candidate present (confidence-quality merge)", pool)
	}

	vectorOn := newBackend(true)
	_, _, _, _, _, state, _, _, _, err = buildConfirmedKindScopedSnapshot(
		context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), vectorOn.deps(),
		[]string{term}, aliasClaimantsByTerm, true, kind, 10)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if state != confirmedKindScopePlanIncomplete {
		t.Fatalf("state = %q, want %q -- the SAME identity-census contribution must never flip completeness when a vector mechanism is live", state, confirmedKindScopePlanIncomplete)
	}
}

// TestBuildConfirmedKindScopedSnapshot_IdentityMapsAreScopedNotShared pins
// the fix for a real isolation gap found during review: an earlier version
// reused the caller's whole-resolution identity/identityTerms maps for the
// scoped population, which let an UNRELATED unscoped claimant veto a
// scoped candidate through identityCollision, and left mutation residue
// for the LATER evidence-census re-decision to consult even when this
// attempt was discarded. The scopedIdentity/scopedIdentityTerms this
// function returns must be built ONLY from what this call itself finds --
// proven here by passing a caller-supplied identity map pre-loaded with a
// collision for the SAME (class, term) pair the scoped candidate matches
// on, and confirming the returned scoped maps do NOT carry that collision
// (a fresh map only this call populated), while the caller's own maps are
// left untouched (same object, same length, before and after).
func TestBuildConfirmedKindScopedSnapshot_IdentityMapsAreScopedNotShared(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	node := candidateNode(kind, "wi_1", "Widget Rollout Backend Task", 0.9, "*")
	backend := &fakeGraphBackend{
		enableSearchKind:  true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{term: {kind: {node}}},
	}
	// A pre-existing, whole-resolution identity map carrying an UNRELATED
	// collision for a completely different subject -- if this function
	// mutated or even READ this map, corrupting it would be observable;
	// here we simply confirm it is untouched (byte-identical length/
	// content) and that the RETURNED scoped maps are independent of it.
	callerIdentity := identityClaimants{
		identityKeyClassLabel: {"unrelated-term": {"unrelated-subject-a": true, "unrelated-subject-b": true}},
	}
	callerIdentityTerms := identityMatchTerms{
		"unrelated-subject-a": {{class: identityKeyClassLabel, term: "unrelated-term"}},
	}
	_, _, _, scopedIdentity, scopedIdentityTerms, state, _, _, _, err := buildConfirmedKindScopedSnapshot(
		context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), backend.deps(),
		[]string{term}, nil, false, kind, 10)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if state != confirmedKindScopeComplete {
		t.Fatalf("state = %q, want %q", state, confirmedKindScopeComplete)
	}
	if len(callerIdentity[identityKeyClassLabel]["unrelated-term"]) != 2 || len(callerIdentityTerms) != 1 {
		t.Fatalf("caller's own identity maps were mutated: identity=%#v terms=%#v", callerIdentity, callerIdentityTerms)
	}
	if _, ok := scopedIdentity[identityKeyClassLabel]["unrelated-term"]; ok {
		t.Fatalf("scopedIdentity = %#v, want it to NOT carry the caller's unrelated collision -- these maps must be built fresh, only from this call's own findings", scopedIdentity)
	}
	if _, ok := scopedIdentityTerms["unrelated-subject-a"]; ok {
		t.Fatalf("scopedIdentityTerms = %#v, want it to NOT carry the caller's unrelated entry", scopedIdentityTerms)
	}
}

// TestResolveFromMergedCandidatesWithGateAndBasis_ConfirmedKindScopedBasisNeverChangesVerdict
// is regression item 8: "Existing exact, identity, collision, trust,
// vector-only, floor, and gap behavior remains unchanged." The new
// confirmedKindScopedBasis parameter must be telemetry-only: for a fixed
// candidate population and every other parameter held constant, toggling
// it must never change which subjects commit or whether the outcome is
// ambiguous -- only the decision event's PopulationBasis field may differ.
func TestResolveFromMergedCandidatesWithGateAndBasis_ConfirmedKindScopedBasisNeverChangesVerdict(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		candidates []contextfabric.SubjectCandidate
	}{
		{"lone floor clears", []contextfabric.SubjectCandidate{corroborationCandidate("lone", 0.80, contextfabric.MatchLexical)}},
		{"below lone floor stays ambiguous", []contextfabric.SubjectCandidate{corroborationCandidate("below", 0.50, contextfabric.MatchLexical)}},
		{"tied top stays ambiguous", tiedTopCandidates()},
		{"exact match commits via exact_index", []contextfabric.SubjectCandidate{corroborationCandidate("exact", 1, contextfabric.MatchExact)}},
		{"vector-only candidate excluded from lone floor", []contextfabric.SubjectCandidate{corroborationCandidate("vec_only", 0.90, contextfabric.MatchVector)}},
		{"top-of-two gap clears", []contextfabric.SubjectCandidate{
			corroborationCandidate("top", 0.95, contextfabric.MatchLexical),
			corroborationCandidate("second", 0.70, contextfabric.MatchLexical),
		}},
		{"top-of-two gap too narrow stays ambiguous", []contextfabric.SubjectCandidate{
			corroborationCandidate("top_narrow", 0.85, contextfabric.MatchLexical),
			corroborationCandidate("second_narrow", 0.80, contextfabric.MatchLexical),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bySubject := make(map[string]contextfabric.SubjectCandidate, len(tc.candidates))
			for _, candidate := range tc.candidates {
				bySubject[SubjectKey(candidate.Subject)] = candidate
			}
			without, _, _ := ResolveFromMergedCandidatesWithGateAndBasis(bySubject, map[string]string{}, map[string]bool{}, 10, true, false,
				nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, nil, "", "", false)
			with, _, _ := ResolveFromMergedCandidatesWithGateAndBasis(bySubject, map[string]string{}, map[string]bool{}, 10, true, false,
				nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, nil, "", "", true)
			if len(without.Committed) != len(with.Committed) {
				t.Fatalf("confirmedKindScopedBasis changed the commit count: without=%v with=%v", without.Committed, with.Committed)
			}
			for i := range without.Committed {
				if without.Committed[i] != with.Committed[i] {
					t.Fatalf("confirmedKindScopedBasis changed WHICH subject committed: without=%v with=%v", without.Committed, with.Committed)
				}
			}
		})
	}

	// identity_fast_path and identityCollision need real identity/
	// identityTerms maps and aliasIdentityComplete=true, so these two run
	// as their own subtests rather than the generic loop above.
	t.Run("identity fast path commits, unique claimant", func(t *testing.T) {
		t.Parallel()
		claimant := aliasIdentityCandidate(contextfabric.SubjectRepository, "r1", "chaos-ops", contextfabric.MatchAlias)
		bySubject := map[string]contextfabric.SubjectCandidate{SubjectKey(claimant.Subject): claimant}
		identity := identityClaimants{}
		identityTerms := identityMatchTerms{}
		recordIdentityClaim(claimant, identity, identityTerms)
		without, _, _ := ResolveFromMergedCandidatesWithGateAndBasis(bySubject, map[string]string{}, map[string]bool{}, 10, true, false,
			nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), identity, identityTerms, true, nil, "", "", false)
		with, _, _ := ResolveFromMergedCandidatesWithGateAndBasis(bySubject, map[string]string{}, map[string]bool{}, 10, true, false,
			nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), identity, identityTerms, true, nil, "", "", true)
		if len(without.Committed) != 1 || len(with.Committed) != 1 || without.Committed[0] != with.Committed[0] {
			t.Fatalf("confirmedKindScopedBasis changed the identity_fast_path verdict: without=%v with=%v", without.Committed, with.Committed)
		}
	})
	t.Run("identityCollision vetoes both ways identically", func(t *testing.T) {
		t.Parallel()
		repo := aliasIdentityCandidate(contextfabric.SubjectRepository, "r1", "chaos-ops", contextfabric.MatchAlias)
		teamCollision := aliasIdentityCandidate(contextfabric.SubjectTeam, "t1", "chaos-ops", contextfabric.MatchAlias)
		bySubject := map[string]contextfabric.SubjectCandidate{
			SubjectKey(repo.Subject):          repo,
			SubjectKey(teamCollision.Subject): teamCollision,
		}
		identity := identityClaimants{}
		identityTerms := identityMatchTerms{}
		recordIdentityClaim(repo, identity, identityTerms)
		recordIdentityClaim(teamCollision, identity, identityTerms)
		without, _, _ := ResolveFromMergedCandidatesWithGateAndBasis(bySubject, map[string]string{}, map[string]bool{}, 10, true, false,
			nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), identity, identityTerms, true, nil, "", "", false)
		with, _, _ := ResolveFromMergedCandidatesWithGateAndBasis(bySubject, map[string]string{}, map[string]bool{}, 10, true, false,
			nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), identity, identityTerms, true, nil, "", "", true)
		if len(without.Committed) != 0 || len(with.Committed) != 0 {
			t.Fatalf("confirmedKindScopedBasis let a known identity collision commit: without=%v with=%v", without.Committed, with.Committed)
		}
	})
}

// TestResolveSubjects_ConfirmedKindScope_CandidateCountZeroOnIncompleteState
// pins a codex review finding (LOW severity, HIGH confidence, confirmed):
// ConfirmedKindScopeCandidateCount must be 0 whenever
// ConfirmedKindScopeState != "complete" -- a truncated exhaustive pass can
// still have merged some candidates into its (discarded) pool before the
// truncation signal arrived, and reporting that count would both
// contradict the field's own documented contract and leak a size signal
// from a population the gate never actually sees.
func TestResolveSubjects_ConfirmedKindScope_CandidateCountZeroOnIncompleteState(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	// The exhaustive SearchKind call finds a real candidate for THIS term,
	// then reports truncated=true -- the pool is non-empty even though the
	// state must downgrade to "truncated".
	node := candidateNode(kind, "wi_1", "Widget Rollout Backend Task", 0.9, "*")
	backend := &fakeGraphBackend{
		enableSearchKind:    true,
		searchResults:       map[string][]CandidateNode{term: {}},
		searchKindResults:   map[string]map[contextfabric.SubjectKind][]CandidateNode{term: {kind: {node}}},
		searchKindTruncated: true,
		searchTruncated:     true,
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), deps, confirmed, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	event, ok := confirmedKindScopeEvent(tracer.events)
	if !ok {
		t.Fatal("no confirmed_kind_scope event")
	}
	if event.ConfirmedKindScopeState != confirmedKindScopeTruncated {
		t.Fatalf("ConfirmedKindScopeState = %q, want %q", event.ConfirmedKindScopeState, confirmedKindScopeTruncated)
	}
	if event.ConfirmedKindScopeCandidateCount != 0 {
		t.Fatalf("ConfirmedKindScopeCandidateCount = %d, want 0 -- a truncated (discarded) snapshot must never report its partial candidate count", event.ConfirmedKindScopeCandidateCount)
	}
}

// TestResolveSubjects_ConfirmedKindScope_SharedIdentityMapCannotVetoTheScopedGateCall
// closes a mutation-survival gap found while landing this ticket: mutating
// resolve.go's own scoped gate call (the ResolveFromMergedCandidatesWithGateAndBasis
// invocation guarded by confirmedKind != nil && searchTruncated -- see that
// call site's own comment) to pass the WHOLE-RESOLUTION identity/
// identityTerms maps instead of buildConfirmedKindScopedSnapshot's fresh
// scopedIdentity/scopedIdentityTerms compiled and every existing test in
// this file (including IdentityMapsAreScopedNotShared, which never wires a
// caller map into the function it tests at all) still passed. That earlier
// test proves buildConfirmedKindScopedSnapshot's OWN return values are
// fresh; it does not prove resolve.go's call site actually uses them for
// the gate decision -- this test proves the latter, end to end through
// ResolveSubjects.
//
// Shape: term "widget-service" alias-matches TWO different-KIND subjects
// -- node (a project, the confirmed kind) and rival (a repository, an
// unrelated kind that merely shares the same alias term). recordIdentityClaim
// keys the collision-detection maps by (class, term) only, never by kind,
// so the unscoped AliasLookup merge (resolve.go's own top-level pass)
// legitimately records a 2-claimant collision on identityClaimants[alias]
// ["widget-service"] for BOTH node and rival -- neither commits in the
// FIRST (unscoped) decision, which is why searchTruncated (an earlier,
// unrelated Search() truncation, case 57's own shape) is also needed to
// reach the CHAOS-4154 branch at all.
//
// buildConfirmedKindScopedSnapshot's own mergeIdentityCensusCandidates
// filters aliasClaimantsByTerm to the confirmed kind BEFORE merging
// (chaos4154_confirmed_kind_scope.go:307, "subject.Kind != kind"), so
// rival -- a repository -- never enters the scoped pool or scopedIdentity
// at all: scopedIdentity's own (alias, "widget-service") claimant set has
// exactly ONE member (node), no collision. If the gate call correctly
// consults scopedIdentity, node commits via lone_floor. If it (bug)
// consults the SHARED identity/identityTerms instead, the cross-kind
// collision node was never involved in producing (rival's) still applies
// to node's own key there and wrongly vetoes the commit.
func TestResolveSubjects_ConfirmedKindScope_SharedIdentityMapCannotVetoTheScopedGateCall(t *testing.T) {
	t.Parallel()
	term := "widget-service"
	kind := contextfabric.SubjectProject
	node := candidateNode(kind, "proj_1", "Widget Service Backend", 0.9, "*")
	node.Attributes["aliases"] = []string{term}
	rival := candidateNode(contextfabric.SubjectRepository, "repo_rival", "Totally Unrelated Repo", 0.85, "*")
	rival.Attributes["aliases"] = []string{term}
	backend := &fakeGraphBackend{
		enableAliasLookup:    true,
		aliasLookupClaimants: map[string][]CandidateNode{term: {node, rival}},
		aliasLookupComplete:  true,
		enableSearchKind:     true,
		searchResults:        map[string][]CandidateNode{term: {}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			term: {kind: {node}},
		},
		// The EARLIER, UNRELATED unscoped Search() truncation -- case 57's
		// own shape, and the precondition (searchTruncated) the CHAOS-4154
		// branch requires to even run.
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
	wantSubject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "proj_1", Label: "Widget Service Backend"}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != wantSubject {
		t.Fatalf("resolution.Committed = %#v, want the confirmed-kind candidate to commit -- a cross-kind claimant's collision on the SHARED identity map must never veto the isolated scoped gate call", resolution.Committed)
	}
	event, ok := lastDecisionEvent(tracer.events)
	if !ok || event.CommitGate != "lone_floor" {
		t.Fatalf("decision event = %+v, ok=%v, want an unchanged lone_floor gate to have decided this", event, ok)
	}
}

// TestResolveSubjects_ConfirmedKindScope_DiscardedScopedDecisionNeverTraces
// closes a second codex R2 finding (Medium, confirmed): a complete-but-empty
// scoped snapshot's own gate call still fires a "decision"-stage
// (Outcome=="no_commit") trace event internally (resolution.go's
// empty-candidates early return), and resolve.go DISCARDS that call's
// resolution whenever it does not commit -- so tracing it unconditionally
// would leave a "decision" event on the wire describing a resolution that
// was never returned, breaking every reader's (production and this
// ticket's own lastDecisionEvent helper's) assumption that the LAST
// decision event describes the RETURNED resolution.
//
// Shape: an unscoped Search() and the exhaustive confirmed-kind SearchKind
// pass both find nothing at all for this term -- the first-pass gate call's
// own empty-candidates early return already fires ONE "decision" event
// (Outcome=="no_commit"), and searchTruncated (case 57's own shape) with
// resolution.Committed==0 reaches the CHAOS-4154 branch, whose OWN scoped
// snapshot is ALSO complete-but-empty (SearchKind found nothing for the
// confirmed kind either) -- exactly the shape that fires a SECOND
// "decision" event pre-fix. Post-fix, discardableDecisionTracer holds that
// second event back (scopedResolution.Committed stays empty, so keep() is
// never called): exactly ONE "decision" event reaches the tracer.
func TestResolveSubjects_ConfirmedKindScope_DiscardedScopedDecisionNeverTraces(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	backend := &fakeGraphBackend{
		enableSearchKind:  true,
		searchResults:     map[string][]CandidateNode{term: {}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{term: {kind: {}}},
		// The EARLIER, UNRELATED unscoped Search() truncation -- case 57's
		// own shape, and the precondition (searchTruncated) the CHAOS-4154
		// branch requires to even run.
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
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want empty -- nothing was ever found for this term", resolution.Committed)
	}
	decisionCount := 0
	for _, event := range tracer.events {
		if event.Stage == "decision" {
			decisionCount++
		}
	}
	if decisionCount != 1 {
		t.Fatalf("decision-stage event count = %d, want exactly 1 -- the discarded scoped re-decision's own \"decision\" event must never reach the tracer, or a reader relying on the LAST decision event to describe the returned resolution is misled", decisionCount)
	}
}
