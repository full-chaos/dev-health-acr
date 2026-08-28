package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestResolveRepositorySubjectSurvivesSharedPoolTruncation is CHAOS-4417's
// own red-first pin, named verbatim in the ticket and in
// .remember/context-fabric/drafts/repo-subject-diagnosis-2026-08-28.md's
// executed repro: an org whose repository population is a small minority
// of its graph (that repro measured 11 repository nodes vs 37,001
// ci_pipeline_run + thousands of other-kind nodes) ties every lexical
// repository match at a base confidence well under any exact-label
// carve-out, so the shared, cross-kind MaxSubjectCandidates cut can crowd
// repository out of the offer entirely (case 2's own diagnosed shape) --
// turn 1, no receipt has confirmed a kind, so CHAOS-4132/CHAOS-4154's
// confirmed-kind machinery cannot engage.
//
// SHAPE (team-lead R4 ruling): this rescue OFFERS, never commits -- a
// statistical LoneFloor/TopFloor decision cannot soundly survive
// resolution-wide searchTruncated without kind authority this ticket does
// not have pre-confirmation (see chaos4417_low_population_kind_scope.go's
// own top doc comment). So the pin here is: the repository candidate
// reaches candidateOfferMaterial's own offer (CandidateOptions), and
// resolution.Committed stays empty -- commit happens at turn 2 through
// the EXISTING CHAOS-4154 confirmed-kind path once the offer is
// confirmed, not through this mechanism.
//
// DISCRIMINATOR (must fail against CHAOS-4038's PRE-EXISTING coverage
// floor alone, or this test proves nothing about THIS ticket): a single-
// term fixture is not enough -- applyKindCoverageFloor already runs its
// own bounded SearchKind pass for a repository/project/team kind missing
// from pool (chaos4038_kind_coverage.go) and would find a single-term
// candidate identically, whether this ticket exists or not. The
// repository node here is discoverable ONLY on the 4th interpreted term,
// beyond CHAOS-4038's own kindCoverageMaxTermsPerKind=3 bound
// (boundedTerms := terms[:kindCoverageMaxTermsPerKind]) -- the floor
// tries at most 3 terms and never reaches it. Only an EXHAUSTIVE,
// uncapped per-kind pass (buildConfirmedKindScopedSnapshot, this
// ticket's own mechanism) walks every term and finds it.
func TestResolveRepositorySubjectSurvivesSharedPoolTruncation(t *testing.T) {
	t.Parallel()
	terms := []string{"t1", "t2", "t3", "t4"}
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repo_1", Label: "acr-core"}
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.8, "*")
	backend := &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{},
		searchTruncated:  true,
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"t4": {contextfabric.SubjectRepository: {node}},
		},
	}
	resolution, offerMaterial, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(terms...), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want ZERO commits -- this rescue offers, it never commits pre-confirmation (team-lead R4 ruling)", resolution.Committed)
	}
	found := false
	for _, option := range offerMaterial.CandidateOptions {
		if option.Kind == subject.Kind && option.CanonicalID == subject.CanonicalID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("offerMaterial.CandidateOptions = %#v, want the repository candidate offered (found only on term 4, beyond CHAOS-4038's own 3-term bound -- only an exhaustive per-kind pass reaches it)", offerMaterial.CandidateOptions)
	}
}

// TestApplyLowPopulationKindOffers_IncompleteKindSkippedNotFatal pins the
// offer-only mechanism's own looser-than-commit discipline: unlike a
// commit-shaped rescue, ONE kind's incomplete (truncated) census must NOT
// abort the whole call -- an offer is a suggestion, not a proof, so any
// OTHER kind's own complete census still contributes normally.
func TestApplyLowPopulationKindOffers_IncompleteKindSkippedNotFatal(t *testing.T) {
	t.Parallel()
	const term = "acr"
	repoSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repo_1", Label: "acr-core"}
	repoNode := candidateNode(repoSubject.Kind, repoSubject.CanonicalID, repoSubject.Label, 0.8, "*")
	var searchKindCalls []contextfabric.SubjectKind
	deps := ResolveDeps{
		IsInternal: noInternalSubjects,
		SearchKind: func(ctx context.Context, searchTerm string, kind contextfabric.SubjectKind, limit int) ([]CandidateNode, bool, bool, error) {
			searchKindCalls = append(searchKindCalls, kind)
			switch kind {
			case contextfabric.SubjectRepository:
				return []CandidateNode{repoNode}, false, false, nil
			case contextfabric.SubjectProject:
				return nil, true, false, nil // truncated=true, incomplete
			default:
				return nil, false, false, nil
			}
		},
	}
	request := testRequest()
	offered, err := applyLowPopulationKindOffers(context.Background(), storage.Principal{OrgID: "org_1"}, request, deps, []string{term}, nil, false, request.Options.MaxSubjectCandidates)
	if err != nil {
		t.Fatalf("applyLowPopulationKindOffers() error = %v", err)
	}
	if len(searchKindCalls) != 3 {
		t.Fatalf("searchKindCalls = %#v, want all three kinds attempted -- an incomplete kind must not stop the OTHERS from being tried (unlike the fail-closed discipline a commit-shaped rescue needs)", searchKindCalls)
	}
	if len(offered) != 1 || offered[0].Subject != repoSubject {
		t.Fatalf("offered = %#v, want the repository candidate offered despite project's own census being incomplete", offered)
	}
}

// TestApplyLowPopulationKindOffers_VectorConfiguredSkipsEntirely is codex
// R1's own finding, still valid under the offer-only redesign: on a
// deployment with a live vector mechanism, buildConfirmedKindScopedSnapshot
// returns plan_incomplete for EVERY kind unconditionally
// (chaos4154_confirmed_kind_scope.go), so this pass can never complete a
// census there -- it must detect that up front and skip entirely, zero
// SearchKind calls, rather than paying for three exhaustive passes to
// reach a foregone conclusion.
func TestApplyLowPopulationKindOffers_VectorConfiguredSkipsEntirely(t *testing.T) {
	t.Parallel()
	const term = "acr"
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repo_1", Label: "acr-core"}
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.8, "*")
	var searchKindCalls int
	deps := ResolveDeps{
		VectorMechanismConfigured: true,
		SearchKind: func(ctx context.Context, searchTerm string, kind contextfabric.SubjectKind, limit int) ([]CandidateNode, bool, bool, error) {
			searchKindCalls++
			return []CandidateNode{node}, false, false, nil
		},
	}
	request := testRequest()
	offered, err := applyLowPopulationKindOffers(context.Background(), storage.Principal{OrgID: "org_1"}, request, deps, []string{term}, nil, false, request.Options.MaxSubjectCandidates)
	if err != nil {
		t.Fatalf("applyLowPopulationKindOffers() error = %v", err)
	}
	if len(offered) != 0 {
		t.Fatalf("offered = %#v, want nothing -- a live vector mechanism forecloses this pass's completeness contract entirely", offered)
	}
	if searchKindCalls != 0 {
		t.Fatalf("searchKindCalls = %d, want ZERO -- VectorMechanismConfigured must be checked BEFORE any SearchKind call, not discovered after paying for it", searchKindCalls)
	}
}

// TestApplyLowPopulationKindOffers_NoCandidatesFound pins the "every kind
// completed, nothing to offer" outcome: a nil SearchKind result for every
// kind (all genuinely complete, all empty) must return nil, not an error
// or a spurious offer.
func TestApplyLowPopulationKindOffers_NoCandidatesFound(t *testing.T) {
	t.Parallel()
	const term = "acr"
	deps := ResolveDeps{
		SearchKind: func(ctx context.Context, searchTerm string, kind contextfabric.SubjectKind, limit int) ([]CandidateNode, bool, bool, error) {
			return nil, false, false, nil
		},
	}
	request := testRequest()
	offered, err := applyLowPopulationKindOffers(context.Background(), storage.Principal{OrgID: "org_1"}, request, deps, []string{term}, nil, false, request.Options.MaxSubjectCandidates)
	if err != nil {
		t.Fatalf("applyLowPopulationKindOffers() error = %v", err)
	}
	if len(offered) != 0 {
		t.Fatalf("offered = %#v, want nil/empty -- every kind's census completed with nothing found", offered)
	}
}

// TestApplyLowPopulationKindOffers_NilSearchKindIsNoOp mirrors
// applyKindCoverageFloor/applyConfirmedKindRescue's own convention: a
// backend that does not implement kind-scoped search cannot be offered
// from, and this returns cleanly rather than erroring.
func TestApplyLowPopulationKindOffers_NilSearchKindIsNoOp(t *testing.T) {
	t.Parallel()
	request := testRequest()
	offered, err := applyLowPopulationKindOffers(context.Background(), storage.Principal{OrgID: "org_1"}, request, ResolveDeps{}, []string{"acr"}, nil, false, request.Options.MaxSubjectCandidates)
	if err != nil {
		t.Fatalf("applyLowPopulationKindOffers() error = %v", err)
	}
	if len(offered) != 0 {
		t.Fatalf("offered = %#v, want nil -- nil deps.SearchKind means every kind returns not_attempted, never complete", offered)
	}
}

// TestResolveRepositorySubjectSurvivesSharedPoolTruncation_UnderOffersOnly is
// codex R4's finding 1 (Medium, confirmed): the rescue's call site in
// resolve.go used to be gated `!offersOnly`, but offersOnly mode
// (contextfabric.WithOffersOnlyResolution, chaos4234_offers_only.go) exists
// SPECIFICALLY to compute StructureOfferMaterial while the rest of the
// resolution is discarded -- see that function's own doc comment: "every
// commit MECHANISM below ... is skipped ... Retrieval, ranking ... and every
// OFFER BUILDER run exactly as on a decisive turn." This rescue never
// commits at all (team-lead R4 ruling, this file's own top doc comment) --
// its entire contribution IS offer material -- so gating it behind
// `!offersOnly` silently withheld exactly the artifact that call exists to
// produce, making the whole rescue a no-op whenever the window gate ran its
// own offers-only pass. Same fixture as
// TestResolveRepositorySubjectSurvivesSharedPoolTruncation (repository
// discoverable only on the 4th term, beyond CHAOS-4038's 3-term bound), the
// only difference being the offers-only context.
func TestResolveRepositorySubjectSurvivesSharedPoolTruncation_UnderOffersOnly(t *testing.T) {
	t.Parallel()
	terms := []string{"t1", "t2", "t3", "t4"}
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repo_1", Label: "acr-core"}
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.8, "*")
	backend := &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{},
		searchTruncated:  true,
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"t4": {contextfabric.SubjectRepository: {node}},
		},
	}
	ctx := contextfabric.WithOffersOnlyResolution(context.Background())
	resolution, offerMaterial, err := ResolveSubjects(ctx, storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(terms...), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want ZERO commits under offers-only mode", resolution.Committed)
	}
	found := false
	for _, option := range offerMaterial.CandidateOptions {
		if option.Kind == subject.Kind && option.CanonicalID == subject.CanonicalID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("offerMaterial.CandidateOptions = %#v, want the repository candidate offered even under offers-only mode -- this rescue never commits, so offers-only must not suppress it", offerMaterial.CandidateOptions)
	}
}

// TestApplyLowPopulationKindOffers_OrderIsDeterministicByCanonicalID is codex
// R4's finding 2 (Medium, confirmed): buildConfirmedKindScopedSnapshot
// returns its pool as a map[string]contextfabric.SubjectCandidate, and this
// file's own top doc comment establishes the rescue is deliberately
// EXHAUSTIVE, unlike CHAOS-4038's bounded/early-exit coverage floor -- so a
// genuinely low-population kind can still return more than
// candidateOfferTopN (5, chaos3900_structure_offers.go) offer slots.
// Appending straight from a map range gives Go's randomized iteration
// order, so which candidates survive candidateOfferMaterial's top-N
// truncation (and therefore what a receipt-bound offer contains) could vary
// run to run for the identical corpus and graph state. Six distinct
// repository terms/candidates (well over the top-5 cut) pin a fixed,
// CanonicalID-sorted order.
func TestApplyLowPopulationKindOffers_OrderIsDeterministicByCanonicalID(t *testing.T) {
	t.Parallel()
	terms := []string{"t1", "t2", "t3", "t4", "t5", "t6"}
	// Deliberately NOT inserted in CanonicalID order (t1 -> repo_f,
	// t2 -> repo_a, ...): a test that happened to insert in the already-
	// sorted order would not discriminate the fix from the bug.
	canonicalIDs := map[string]string{
		"t1": "repo_f", "t2": "repo_a", "t3": "repo_e",
		"t4": "repo_b", "t5": "repo_d", "t6": "repo_c",
	}
	searchKindResults := make(map[string]map[contextfabric.SubjectKind][]CandidateNode, len(terms))
	for _, term := range terms {
		id := canonicalIDs[term]
		searchKindResults[term] = map[contextfabric.SubjectKind][]CandidateNode{
			contextfabric.SubjectRepository: {candidateNode(contextfabric.SubjectRepository, id, id, 0.8, "*")},
		}
	}
	deps := ResolveDeps{
		IsInternal: noInternalSubjects,
		SearchKind: func(ctx context.Context, term string, kind contextfabric.SubjectKind, limit int) ([]CandidateNode, bool, bool, error) {
			if kind != contextfabric.SubjectRepository {
				return nil, false, false, nil
			}
			return searchKindResults[term][kind], false, false, nil
		},
	}
	request := testRequest()
	offered, err := applyLowPopulationKindOffers(context.Background(), storage.Principal{OrgID: "org_1"}, request, deps, terms, nil, false, request.Options.MaxSubjectCandidates)
	if err != nil {
		t.Fatalf("applyLowPopulationKindOffers() error = %v", err)
	}
	if len(offered) != 6 {
		t.Fatalf("offered = %#v, want all 6 distinct repository candidates found", offered)
	}
	want := []string{"repo_a", "repo_b", "repo_c", "repo_d", "repo_e", "repo_f"}
	for i, candidate := range offered {
		if candidate.Subject.CanonicalID != want[i] {
			t.Fatalf("offered[%d].Subject.CanonicalID = %q, want %q (offered = %#v) -- must be sorted by CanonicalID, not map-iteration order", i, candidate.Subject.CanonicalID, want[i], offered)
		}
	}
}

// TestResolveSubjects_LowPopulationRescueDuplicateWithCoverageFloorTracesOnce
// is codex R4's finding 3 (Low, confirmed): a stale comment at this
// mechanism's union call site in resolve.go claimed CHAOS-4038's own
// kindCoverageFloorKinds is disjoint from chaos4417LowPopulationScopedKinds.
// It is not -- chaos4038_kind_coverage.go's own doc comment says the floor's
// kind set explicitly "includes isAliasLookupScopedKind kinds (repository/
// project/team)", the SAME three kinds this rescue targets. So the SAME
// subject can legitimately appear in coverageCandidates via BOTH
// contributions. unionCandidatesForOffer already dedupes the final offer
// candidate list (so the OFFER itself was never corrupted), but the
// ranked_cut/CoverageBypass=true trace loop ranged over coverageCandidates
// directly and fired once per ENTRY rather than once per distinct subject --
// double-counting one dropped candidate as two in any reader that counts
// these events. This fixture puts the repository candidate within BOTH
// CHAOS-4038's 3-term bound and CHAOS-4417's exhaustive reach (term 1), so
// both contributions find it.
func TestResolveSubjects_LowPopulationRescueDuplicateWithCoverageFloorTracesOnce(t *testing.T) {
	t.Parallel()
	terms := []string{"t1", "t2", "t3", "t4"}
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repo_1", Label: "acr-core"}
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.8, "*")
	backend := &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{},
		searchTruncated:  true,
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			// term 1: within CHAOS-4038's kindCoverageMaxTermsPerKind=3
			// bound AND within CHAOS-4417's exhaustive reach -- both
			// mechanisms independently find the SAME node here.
			"t1": {contextfabric.SubjectRepository: {node}},
		},
	}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(terms...), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want ZERO commits", resolution.Committed)
	}
	bypassCount := 0
	for _, event := range tracer.events {
		if event.Stage == "ranked_cut" && event.CoverageBypass && event.Subject == subject {
			bypassCount++
		}
	}
	if bypassCount != 1 {
		t.Fatalf("ranked_cut/CoverageBypass events for %#v = %d, want exactly 1 -- CHAOS-4038's coverage floor and CHAOS-4417's rescue both legitimately found this subject, but a reader counting dropped candidates must see it once, not once per contributing mechanism", subject, bypassCount)
	}
}
