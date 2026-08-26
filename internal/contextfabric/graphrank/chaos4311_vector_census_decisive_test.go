package graphrank

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4311 Phase 3: this file is the ResolveSubjects-level regression bar
// for the flip from shadow to decision-bearing. See
// chaos4155_confirmed_kind_vector_scope.go's own top-of-file doc comment for
// the ratified shape; chaos4154_confirmed_kind_scope_test.go's own
// TestResolveSubjects_ConfirmedKindScope_* tests are this file's direct
// precedent for the fixture shape (fakeGraphBackend, candidateNode,
// testRequest/testInterpreted, recordingTracer).
//
// Two tests here are the team-lead-mandated PRIMARY gating proof for this
// PR (chris "the authorization gap is the most important finding of the
// flip"):
//   - TestResolveSubjects_ConfirmedKindVectorCensus_UnauthorizedRivalNeverReachesTheOfferPool
//   - TestResolveSubjects_ConfirmedKindVectorCensus_RivalsNeverChangeTheCommitDecision

// vectorCensusFixtureBackend builds the SHARED base fixture every test below
// starts from: a confirmed work_item kind, a live vector mechanism
// (vectorMechanismConfigured=true), an exhaustive lexical SearchKind pass
// that finds exactly ONE candidate (subject), and a resolution-wide
// searchTruncated=true from an EARLIER, unrelated ordinary Search() call --
// the exact confirmedKindScopePlanIncomplete-reaching shape
// TestResolveSubjects_ConfirmedKindScope_LiveVectorMechanismBlocksFallbackCompleteness
// already pins. census, when non-nil, becomes this backend's own
// ConfirmedKindVectorCensus outcome.
func vectorCensusFixtureBackend(t *testing.T, kind contextfabric.SubjectKind, term string, subject contextfabric.SubjectRef, census *ConfirmedKindVectorCensusOutcome) *fakeGraphBackend {
	t.Helper()
	node := candidateNode(kind, subject.CanonicalID, subject.Label, 0.9, "*")
	backend := &fakeGraphBackend{
		enableSearchKind:          true,
		searchResults:             map[string][]CandidateNode{term: {}},
		searchKindResults:         map[string]map[contextfabric.SubjectKind][]CandidateNode{term: {kind: {node}}},
		searchTruncated:           true,
		vectorMechanismConfigured: true,
	}
	if census != nil {
		backend.enableConfirmedKindVectorCensus = true
		backend.confirmedKindVectorCensusResult = *census
	}
	return backend
}

// TestResolveSubjects_ConfirmedKindVectorCensus_NoRivalsCommitsThroughTheIsolatedPopulation
// is Phase 3's own core positive case (ticket item 2, first half): a
// Complete outcome with RivalCountAboveTau==0 lets the isolated
// confirmed-kind-scoped population commit exactly like
// confirmedKindScopeComplete already does -- the SAME lexical-only
// candidate that TestResolveSubjects_ConfirmedKindScope_LiveVectorMechanismBlocksFallbackCompleteness
// pins as ambiguous WITHOUT this outcome must now commit WITH it.
func TestResolveSubjects_ConfirmedKindVectorCensus_NoRivalsCommitsThroughTheIsolatedPopulation(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "wi_1", Label: "Widget Rollout Backend Task"}
	backend := vectorCensusFixtureBackend(t, kind, term, subject, &ConfirmedKindVectorCensusOutcome{
		State: ConfirmedKindVectorScopeComplete, PopulationCount: 5, EnumeratedCount: 5, RivalCountAboveTau: 0,
	})
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), deps, confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != subject {
		t.Fatalf("resolution.Committed = %#v, want the isolated candidate to commit -- a Complete vector census with zero rivals must extend the lexical-only completeness proof", resolution.Committed)
	}
	event, ok := lastDecisionEvent(tracer.events)
	if !ok || event.PopulationBasis != "confirmed_kind_scoped_complete" {
		t.Fatalf("decision event = %+v, ok=%v, want population_basis=confirmed_kind_scoped_complete", event, ok)
	}
	if !event.ConfirmedKindVectorCensusDecisive {
		t.Error("decision event ConfirmedKindVectorCensusDecisive = false, want true -- the vector census's own Complete-with-zero-rivals outcome is what let this branch run at all (the lexical pass alone left scopeState=plan_incomplete)")
	}
}

// TestResolveSubjects_ConfirmedKindVectorCensus_UnauthorizedRivalNeverReachesTheOfferPool
// is the team-lead-mandated PRIMARY gating test: a rival row from a
// repository the calling principal is NOT authorized for scores above tau
// (RivalCountAboveTau=1, carried on the outcome) but must NEVER reach the
// offer pool. The census's own row fetch (falkorgraph) has no
// authorization predicate at all -- see ConfirmedKindVectorCensusOutcome.Rivals'
// own doc comment -- so this call site's own mergeSearchResults merge
// (which runs NodeCandidate/AuthorizedAttributes) is the ONLY gate standing
// between an unfiltered graph row and a caller-visible offer.
func TestResolveSubjects_ConfirmedKindVectorCensus_UnauthorizedRivalNeverReachesTheOfferPool(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "wi_1", Label: "Widget Rollout Backend Task"}
	unauthorizedRival := candidateNode(kind, "wi_unauthorized", "A Rival In Another Repo", 0, []string{"repo-the-caller-cannot-see"})
	unauthorizedRival.Mechanism = contextfabric.MatchVector
	similarity := 0.91
	unauthorizedRival.VectorSimilarity = &similarity
	backend := vectorCensusFixtureBackend(t, kind, term, subject, &ConfirmedKindVectorCensusOutcome{
		State: ConfirmedKindVectorScopeComplete, PopulationCount: 5, EnumeratedCount: 5,
		RivalCountAboveTau: 1, Rivals: []CandidateNode{unauthorizedRival},
	})
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	// The calling principal is scoped to a DIFFERENT repository than the
	// rival's own authorization_repositories -- the exact
	// TestNodeCandidateFiltersUnauthorizedNodesBeforeCandidates shape.
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	resolution, offerMaterial, err := ResolveSubjects(context.Background(), principal, testRequest(), testInterpreted(term), deps, confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want ambiguous -- a rival exists, so the isolated population must NOT commit", resolution.Committed)
	}
	for _, option := range offerMaterial.CandidateOptions {
		if option.CanonicalID == "wi_unauthorized" {
			t.Fatalf("offerMaterial.CandidateOptions = %+v, want it to OMIT the unauthorized rival wi_unauthorized entirely", offerMaterial.CandidateOptions)
		}
	}
	event, ok := confirmedKindScopeEvent(tracer.events)
	if !ok {
		t.Fatal("no confirmed_kind_scope event captured")
	}
	if event.ConfirmedKindVectorScopeRivalCountAboveTau != 1 {
		t.Fatalf("event.ConfirmedKindVectorScopeRivalCountAboveTau = %d, want 1 (the outcome's own raw count, unaffected by authorization)", event.ConfirmedKindVectorScopeRivalCountAboveTau)
	}
	if event.ConfirmedKindVectorScopeRivalsOfferedCount != 0 {
		t.Fatalf("event.ConfirmedKindVectorScopeRivalsOfferedCount = %d, want 0 -- the ONE rival that scored above tau was unauthorized and must never be counted as offered", event.ConfirmedKindVectorScopeRivalsOfferedCount)
	}
}

// TestResolveSubjects_ConfirmedKindVectorCensus_NonCompleteStateNeverOffersInjectedRivals
// is codex R2's own Low-confidence finding (CHAOS-4311, confirmed): the
// rivals-merge block at the ConfirmedKindVectorCensus call site was guarded
// only by len(scopeVectorCensus.Rivals)>0, not also State==Complete. The
// concrete falkorgraph adapter only ever populates Rivals on Complete (see
// ConfirmedKindVectorCensusOutcome.Rivals' own doc comment), so this was not
// presently exploitable through the real producer -- but the caller's own
// fail-closed property should not depend entirely on that producer
// invariant. This test injects a non-Complete state (Failed) carrying a
// Rivals slice anyway (a shape the real adapter never produces, but ANY
// ResolveDeps.ConfirmedKindVectorCensus implementation could) and asserts
// it is never merged into the offer pool.
func TestResolveSubjects_ConfirmedKindVectorCensus_NonCompleteStateNeverOffersInjectedRivals(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "wi_1", Label: "Widget Rollout Backend Task"}
	injectedRival := candidateNode(kind, "wi_injected_on_failed_state", "Should Never Be Offered", 0, []string{"full-chaos/dev-health-acr"})
	injectedRival.Mechanism = contextfabric.MatchVector
	similarity := 0.91
	injectedRival.VectorSimilarity = &similarity
	backend := vectorCensusFixtureBackend(t, kind, term, subject, &ConfirmedKindVectorCensusOutcome{
		// State: Failed, NOT Complete -- a shape the real adapter never
		// produces (Rivals stays nil on every non-Complete return path), but
		// this caller must not rely solely on that producer invariant.
		State: ConfirmedKindVectorScopeFailed, RivalCountAboveTau: 1, Rivals: []CandidateNode{injectedRival},
	})
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	_, offerMaterial, err := ResolveSubjects(context.Background(), principal, testRequest(), testInterpreted(term), deps, confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	for _, option := range offerMaterial.CandidateOptions {
		if option.CanonicalID == "wi_injected_on_failed_state" {
			t.Fatalf("offerMaterial.CandidateOptions = %+v, want it to OMIT the injected rival -- State=Failed (not Complete) must never merge Rivals into the offer pool, regardless of what the producer populated", offerMaterial.CandidateOptions)
		}
	}
	event, ok := confirmedKindScopeEvent(tracer.events)
	if !ok {
		t.Fatal("no confirmed_kind_scope event captured")
	}
	if event.ConfirmedKindVectorScopeRivalsOfferedCount != 0 {
		t.Fatalf("event.ConfirmedKindVectorScopeRivalsOfferedCount = %d, want 0 -- a non-Complete state's Rivals must never be merged or counted as offered", event.ConfirmedKindVectorScopeRivalsOfferedCount)
	}
}

// TestResolveSubjects_ConfirmedKindVectorCensus_AuthorizedRivalReachesTheOfferPool
// is the positive counterpart to the test above -- proves the mechanism
// actually WORKS, not merely that it safely does nothing: an AUTHORIZED
// rival (same repository as the calling principal) must reach both the
// telemetry count and the caller-visible offer material.
func TestResolveSubjects_ConfirmedKindVectorCensus_AuthorizedRivalReachesTheOfferPool(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "wi_1", Label: "Widget Rollout Backend Task"}
	authorizedRival := candidateNode(kind, "wi_rival_authorized", "A Genuine Rival", 0, []string{"full-chaos/dev-health-acr"})
	authorizedRival.Mechanism = contextfabric.MatchVector
	similarity := 0.91
	authorizedRival.VectorSimilarity = &similarity
	backend := vectorCensusFixtureBackend(t, kind, term, subject, &ConfirmedKindVectorCensusOutcome{
		State: ConfirmedKindVectorScopeComplete, PopulationCount: 5, EnumeratedCount: 5,
		RivalCountAboveTau: 1, Rivals: []CandidateNode{authorizedRival},
	})
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	resolution, offerMaterial, err := ResolveSubjects(context.Background(), principal, testRequest(), testInterpreted(term), deps, confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want ambiguous -- a rival exists, so the isolated population must NOT commit even though the rival IS authorized (offer-only, never auto-commit)", resolution.Committed)
	}
	found := false
	for _, option := range offerMaterial.CandidateOptions {
		if option.CanonicalID == "wi_rival_authorized" {
			found = true
		}
	}
	if !found {
		t.Fatalf("offerMaterial.CandidateOptions = %+v, want it to include the authorized rival wi_rival_authorized", offerMaterial.CandidateOptions)
	}
	event, ok := confirmedKindScopeEvent(tracer.events)
	if !ok || event.ConfirmedKindVectorScopeRivalsOfferedCount != 1 {
		t.Fatalf("confirmed_kind_scope event = %+v, ok=%v, want ConfirmedKindVectorScopeRivalsOfferedCount=1", event, ok)
	}
}

// TestResolveSubjects_ConfirmedKindVectorCensus_RivalsNeverChangeTheCommitDecision
// is the team-lead-mandated SECOND gating test: with a rival present
// (RivalCountAboveTau>0), planCompleteViaVectorCensus is false BY DESIGN --
// the isolated population must stay exactly as ambiguous as it was before
// CHAOS-4311 existed (the arm never wired at all, Phase 1/2's own byte-
// identical-to-off guarantee). Control = the arm completely OFF
// (enableConfirmedKindVectorCensus unset, exactly TestResolveSubjects_ConfirmedKindScope_LiveVectorMechanismBlocksFallbackCompleteness's
// own shape); treatment = the arm ON with an AUTHORIZED rival present. The
// commit decision (Committed, CommitBasisSet, CommitDecisionDigestSet) must
// be BYTE-IDENTICAL between the two -- proving the offer-only rival merge
// (nil identity, a private pool the gate never reads -- chaos4038_kind_coverage.go's
// own CHAOS-4271 precedent) has genuinely ZERO effect on the decision, end
// to end, not merely by code inspection.
func TestResolveSubjects_ConfirmedKindVectorCensus_RivalsNeverChangeTheCommitDecision(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "wi_1", Label: "Widget Rollout Backend Task"}
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}

	runOnce := func(census *ConfirmedKindVectorCensusOutcome) (contextfabric.SubjectResolution, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet) {
		backend := vectorCensusFixtureBackend(t, kind, term, subject, census)
		deps := backend.deps()
		resolution, _, bases, digests, err := ResolveSubjectsWithCommitBasis(context.Background(), principal, testRequest(), testInterpreted(term), deps, &contextfabric.ConfirmedExpectedKind{Kind: kind}, nil)
		if err != nil {
			t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
		}
		return resolution, bases, digests
	}

	// Control: the arm entirely OFF -- vectorMechanismConfigured is still
	// true (vectorCensusFixtureBackend's own default), but
	// ConfirmedKindVectorCensus is nil, so attemptConfirmedKindVectorCensus
	// returns NotAttempted immediately and scopeState stays
	// confirmedKindScopePlanIncomplete with no re-decision possible --
	// exactly Phase 1/2's own pre-CHAOS-4311 behavior.
	controlResolution, controlBases, controlDigests := runOnce(nil)

	rival := candidateNode(kind, "wi_rival_authorized", "A Genuine Rival", 0, []string{"full-chaos/dev-health-acr"})
	rival.Mechanism = contextfabric.MatchVector
	similarity := 0.91
	rival.VectorSimilarity = &similarity
	treatmentResolution, treatmentBases, treatmentDigests := runOnce(&ConfirmedKindVectorCensusOutcome{
		State: ConfirmedKindVectorScopeComplete, PopulationCount: 5, EnumeratedCount: 5,
		RivalCountAboveTau: 1, Rivals: []CandidateNode{rival},
	})

	if len(controlResolution.Committed) != 0 || len(treatmentResolution.Committed) != 0 {
		t.Fatalf("both legs must stay ambiguous (nothing committed): control=%#v treatment=%#v", controlResolution.Committed, treatmentResolution.Committed)
	}
	if !reflect.DeepEqual(controlResolution.Committed, treatmentResolution.Committed) {
		t.Fatalf("Committed differs with a rival present: control=%#v treatment=%#v -- offer-only must never change WHO commits", controlResolution.Committed, treatmentResolution.Committed)
	}
	if !reflect.DeepEqual(controlBases, treatmentBases) {
		t.Fatalf("CommitBasisSet differs with a rival present: control=%#v treatment=%#v", controlBases, treatmentBases)
	}
	if !reflect.DeepEqual(controlDigests, treatmentDigests) {
		t.Fatalf("CommitDecisionDigestSet differs with a rival present: control=%#v treatment=%#v", controlDigests, treatmentDigests)
	}
}

// TestResolveSubjects_ConfirmedKindVectorCensus_LexicalOnlyCompletionIsNeverTaggedDecisive
// pins ConfirmedKindVectorCensusDecisive's own negative case: when the
// LEXICAL pass alone already proves completeness (no live vector
// mechanism, the pre-existing confirmedKindScopeComplete path), the
// decision event must NOT claim the vector census was decisive -- it was
// never even consulted.
func TestResolveSubjects_ConfirmedKindVectorCensus_LexicalOnlyCompletionIsNeverTaggedDecisive(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "wi_1", Label: "Widget Rollout Backend Task"}
	node := candidateNode(kind, subject.CanonicalID, subject.Label, 0.9, "*")
	backend := &fakeGraphBackend{
		enableSearchKind:  true,
		searchResults:     map[string][]CandidateNode{term: {}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{term: {kind: {node}}},
		searchTruncated:   true,
		// vectorMechanismConfigured left false: no live vector mechanism,
		// so buildConfirmedKindScopedSnapshot's own state machine returns
		// confirmedKindScopeComplete directly -- the census is never
		// reached at all.
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), deps, confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("resolution.Committed = %#v, want 1 (lexical-only completeness, unrelated to this test's own assertion)", resolution.Committed)
	}
	event, ok := lastDecisionEvent(tracer.events)
	if !ok {
		t.Fatal("no decision event captured")
	}
	if event.ConfirmedKindVectorCensusDecisive {
		t.Error("decision event ConfirmedKindVectorCensusDecisive = true, want false -- no live vector mechanism means the census was never reached, so it cannot have been decisive")
	}
}

// TestResolveSubjects_ConfirmedKindVectorCensus_RivalsPreserveSortedOrderInTheOfferPool
// is codex R1's own Medium-confidence, High-confidence finding, pinned as a
// regression test: sortedCensusRivals' own contract (chaos4155_confirmed_kind_vector_census.go)
// sorts scopeVectorCensus.Rivals by CanonicalID, but the offer-only merge
// above stages every rival into offerOnlyPool, a map -- Go map iteration
// order is randomized per process, so a naive `for range offerOnlyPool`
// would silently discard that ordering before candidateOfferMaterial's own
// verbatim-order, top-N truncation (chaos3900_structure_offers.go) ever
// runs. The fix reconstructs the authorized slice by walking
// scopeVectorCensus.Rivals itself (already sorted) and looking each one up
// in offerOnlyPool by key, rather than ranging over the map directly. Four,
// not five: candidateOfferTopN (chaos3900_structure_offers.go) caps
// offerMaterial.CandidateOptions at 5 total, and the isolated lexical
// candidate wi_1 itself occupies one slot.
//
// A single ResolveSubjects call is NOT a reliable red proof on its own: Go's
// map iteration randomizes only the STARTING bucket slot, so a 4-entry,
// single-bucket, no-collision map (exactly this shape) iterates in one of 4
// ROTATIONS of insertion order, not a full random permutation -- a bug
// re-introduced here would still coincidentally reproduce the sorted order
// ~1/4 of the time (confirmed empirically: /tmp/maptest2.go, 20 in-process
// map creations with these exact keys, ~25-30% landed sorted). This test
// therefore calls ResolveSubjects independently vectorCensusOrderCheckRuns
// times -- each call builds its own freshly-seeded offerOnlyPool map -- and
// requires every single run to preserve order, driving a reintroduced bug's
// false-pass probability to (1/4)^20, i.e. effectively zero, while the
// fixed code (which no longer depends on map order at all) passes all runs
// deterministically.
const vectorCensusOrderCheckRuns = 20

func TestResolveSubjects_ConfirmedKindVectorCensus_RivalsPreserveSortedOrderInTheOfferPool(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "wi_1", Label: "Widget Rollout Backend Task"}
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}

	rivalIDs := []string{"wi_rival_alpha", "wi_rival_bravo", "wi_rival_charlie", "wi_rival_delta"}
	rivals := make([]CandidateNode, 0, len(rivalIDs))
	for _, id := range rivalIDs {
		rival := candidateNode(kind, id, "Rival "+id, 0, []string{"full-chaos/dev-health-acr"})
		rival.Mechanism = contextfabric.MatchVector
		similarity := 0.91
		rival.VectorSimilarity = &similarity
		rivals = append(rivals, rival)
	}

	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	for run := 0; run < vectorCensusOrderCheckRuns; run++ {
		backend := vectorCensusFixtureBackend(t, kind, term, subject, &ConfirmedKindVectorCensusOutcome{
			State: ConfirmedKindVectorScopeComplete, PopulationCount: 5, EnumeratedCount: 5,
			RivalCountAboveTau: int64(len(rivals)), Rivals: rivals,
		})
		deps := backend.deps()
		_, offerMaterial, err := ResolveSubjects(context.Background(), principal, testRequest(), testInterpreted(term), deps, confirmed, nil)
		if err != nil {
			t.Fatalf("run %d: ResolveSubjects() error = %v", run, err)
		}

		var gotOrder []string
		for _, option := range offerMaterial.CandidateOptions {
			for _, id := range rivalIDs {
				if option.CanonicalID == id {
					gotOrder = append(gotOrder, option.CanonicalID)
				}
			}
		}
		if !reflect.DeepEqual(gotOrder, rivalIDs) {
			t.Fatalf("run %d: rival order in offerMaterial.CandidateOptions = %v, want %v (scopeVectorCensus.Rivals' own sorted order, preserved through the offer-only merge) -- a reintroduced `for range offerOnlyPool` would scramble this", run, gotOrder, rivalIDs)
		}
	}
}
