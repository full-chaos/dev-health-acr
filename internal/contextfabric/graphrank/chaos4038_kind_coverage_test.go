package graphrank

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestKindCoverageFloorKinds_ExcludesAliasLookupScopedKinds pins the CHAOS-4038
// scoping rule: repository/project/team (isAliasLookupScopedKind) must never
// appear in kindCoverageFloorKinds -- those three already get a COMPLETE,
// exact-term identity-universe read via AliasLookup, and a supplemental
// generic lexical query would duplicate, not extend, that path.
func TestKindCoverageFloorKinds_ExcludesAliasLookupScopedKinds(t *testing.T) {
	t.Parallel()
	for _, scoped := range []contextfabric.SubjectKind{
		contextfabric.SubjectRepository, contextfabric.SubjectProject, contextfabric.SubjectTeam,
	} {
		if kindCoverageFloorKinds[scoped] {
			t.Errorf("kindCoverageFloorKinds[%q] = true, want false -- alias-lookup-scoped kinds must be excluded", scoped)
		}
	}
}

// TestKindCoverageFloorKinds_MatchesStructureOfferKindsMinusAliasLookupScoped
// proves kindCoverageFloorKinds is EXACTLY structureOfferKinds minus the
// alias-lookup-scoped subset -- never a second, independently drifting list.
func TestKindCoverageFloorKinds_MatchesStructureOfferKindsMinusAliasLookupScoped(t *testing.T) {
	t.Parallel()
	for kind := range structureOfferKinds {
		want := !isAliasLookupScopedKind(kind)
		if got := kindCoverageFloorKinds[kind]; got != want {
			t.Errorf("kindCoverageFloorKinds[%q] = %v, want %v", kind, got, want)
		}
	}
	for kind := range kindCoverageFloorKinds {
		if !structureOfferKinds[kind] {
			t.Errorf("kindCoverageFloorKinds contains %q, which is not even in structureOfferKinds", kind)
		}
	}
}

// TestMissingCoverageKinds_EmptyPoolReturnsEveryFloorKindInOrder proves an
// empty pool reports every kindCoverageFloorKinds member as missing, in the
// fixed kindCoverageOrder -- never Go's randomized map order.
func TestMissingCoverageKinds_EmptyPoolReturnsEveryFloorKindInOrder(t *testing.T) {
	t.Parallel()
	got := missingCoverageKinds(map[string]contextfabric.SubjectCandidate{}, kindCoverageOrder)
	if len(got) != len(kindCoverageFloorKinds) {
		t.Fatalf("missingCoverageKinds(empty) = %#v, want one entry per kindCoverageFloorKinds member", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("missingCoverageKinds(empty) = %#v, want strictly ascending (deterministic) order", got)
		}
	}
}

// TestMissingCoverageKinds_PresentKindDropsOut proves a kind already
// represented in the pool is excluded from the missing set, and a kind
// outside the floorKinds argument entirely (e.g. an alias-lookup-scoped kind
// when the caller passed the BASE kindCoverageOrder) never appears
// regardless of pool contents.
func TestMissingCoverageKinds_PresentKindDropsOut(t *testing.T) {
	t.Parallel()
	pool := map[string]contextfabric.SubjectCandidate{
		"k1": {Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "wi_1"}},
	}
	got := missingCoverageKinds(pool, kindCoverageOrder)
	for _, kind := range got {
		if kind == contextfabric.SubjectWorkItem {
			t.Fatalf("missingCoverageKinds(pool with work_item) = %#v, want work_item excluded", got)
		}
		if kind == contextfabric.SubjectRepository || kind == contextfabric.SubjectProject || kind == contextfabric.SubjectTeam {
			t.Fatalf("missingCoverageKinds() = %#v, want no alias-lookup-scoped kind present when called with the BASE kindCoverageOrder", got)
		}
	}
	if len(got) != len(kindCoverageFloorKinds)-1 {
		t.Fatalf("missingCoverageKinds(pool with work_item) = %#v, want exactly one fewer than the full floor set", got)
	}
}

// TestEffectiveCoverageFloorKinds_TrustworthyAliasLookupExcludesItsThreeKinds
// pins the aliasLookupTrustworthy=true case: repository/project/team are
// excluded, exactly kindCoverageOrder -- AliasLookup already covers them
// completely for this resolution, so a supplemental generic lexical query
// would duplicate, not extend, that path.
func TestEffectiveCoverageFloorKinds_TrustworthyAliasLookupExcludesItsThreeKinds(t *testing.T) {
	t.Parallel()
	got := effectiveCoverageFloorKinds(true)
	if len(got) != len(kindCoverageOrder) {
		t.Fatalf("effectiveCoverageFloorKinds(true) = %#v, want exactly kindCoverageOrder", got)
	}
	for _, scoped := range []contextfabric.SubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectProject, contextfabric.SubjectTeam} {
		for _, kind := range got {
			if kind == scoped {
				t.Fatalf("effectiveCoverageFloorKinds(true) = %#v, want %q excluded", got, scoped)
			}
		}
	}
}

// TestEffectiveCoverageFloorKinds_UntrustworthyAliasLookupIncludesItsThreeKinds
// is codex CHAOS-4038 review finding 3's own regression: aliasLookupTrustworthy=false
// (nil AliasLookup, or one that ran but could not prove completeness) must
// widen the floor to ALSO cover repository/project/team -- nothing else in
// the resolution covers them in that case.
func TestEffectiveCoverageFloorKinds_UntrustworthyAliasLookupIncludesItsThreeKinds(t *testing.T) {
	t.Parallel()
	got := effectiveCoverageFloorKinds(false)
	if len(got) != len(kindCoverageOrder)+3 {
		t.Fatalf("effectiveCoverageFloorKinds(false) = %#v, want kindCoverageOrder plus exactly 3 alias-lookup-scoped kinds", got)
	}
	for _, scoped := range []contextfabric.SubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectProject, contextfabric.SubjectTeam} {
		found := false
		for _, kind := range got {
			if kind == scoped {
				found = true
			}
		}
		if !found {
			t.Fatalf("effectiveCoverageFloorKinds(false) = %#v, want %q included", got, scoped)
		}
	}
}

// TestPoolHasKind proves the simple membership check applyKindCoverageFloor
// uses to stop early once a kind's floor is satisfied.
func TestPoolHasKind(t *testing.T) {
	t.Parallel()
	pool := map[string]contextfabric.SubjectCandidate{
		"k1": {Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pr_1"}},
	}
	if !poolHasKind(pool, contextfabric.SubjectPullRequest) {
		t.Fatal("poolHasKind(pull_request) = false, want true")
	}
	if poolHasKind(pool, contextfabric.SubjectWorkItem) {
		t.Fatal("poolHasKind(work_item) = true, want false -- pool has no work_item candidate")
	}
}

// TestResolveSubjects_SearchKindNilIsSkippedSilently is the CHAOS-4038
// counterpart of the SearchQuestion nil-is-a-no-op test: a backend that
// never sets SearchKind must behave byte-identically to before this ticket.
func TestResolveSubjects_SearchKindNilIsSkippedSilently(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"outage": {}}}
	request := testRequest()
	resolution, offer, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("outage"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Candidates) != 0 {
		t.Fatalf("resolution.Candidates = %#v, want empty -- nil SearchKind must contribute nothing", resolution.Candidates)
	}
	if len(offer.KindOptions) != 0 {
		t.Fatalf("offer.KindOptions = %#v, want empty", offer.KindOptions)
	}
}

// TestResolveSubjects_SearchKindSkippedWhenPoolAlreadyCoversAllFloorKinds
// proves the floor never spends a call when every kindCoverageFloorKinds
// member is already represented -- purely additive, never redundant I/O.
func TestResolveSubjects_SearchKindSkippedWhenPoolAlreadyCoversAllFloorKinds(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		// AliasLookup complete=true: effectiveCoverageFloorKinds(true) is
		// exactly kindCoverageOrder (the 4 census kinds) -- repository/
		// project/team are AliasLookup's own scope for this test, not the
		// coverage floor's, so the pool below only needs to cover the 4.
		enableAliasLookup:    true,
		aliasLookupComplete:  true,
		aliasLookupClaimants: map[string][]CandidateNode{},
		searchResults: map[string][]CandidateNode{
			"outage": {
				candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item", 0.9, "*"),
				candidateNode(contextfabric.SubjectPullRequest, "pr_1", "Outage PR", 0.9, "*"),
				candidateNode(contractsv1.ContextFabricSubjectCIRun, "ci_1", "Outage CI run", 0.9, "*"),
				candidateNode(contractsv1.ContextFabricSubjectPullRequestReview, "prr_1", "Outage PR review", 0.9, "*"),
			},
		},
	}
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("outage"), backend.deps(), nil, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(backend.searchKindCalls) != 0 {
		t.Fatalf("searchKindCalls = %#v, want none -- every floor kind was already in the pool", backend.searchKindCalls)
	}
}

// TestResolveSubjects_SearchKindFillsMissingCoverageKind is the direct
// CHAOS-4038 regression test: the ordinary Search pass finds only a
// work_item candidate for "outage" -- a single-kind pool kindOfferMaterial's
// own gate refuses to offer disambiguation over (chaos3900_structure_offers.go,
// "a single-kind pool with NO explicit hint has nothing to disambiguate").
// SearchKind supplies a pull_request candidate the ordinary top-K never
// surfaced; the resulting two-kind pool must both (a) contain the
// pull_request candidate and (b) make kindOfferMaterial actually offer
// expected_kind disambiguation -- proving the fix closes the exact gap
// CHAOS-4038 describes end to end, not merely that a candidate was added.
func TestResolveSubjects_SearchKindFillsMissingCoverageKind(t *testing.T) {
	t.Parallel()
	prCandidate := candidateNode(contextfabric.SubjectPullRequest, "pr_1", "Outage PR", 0.6, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults: map[string][]CandidateNode{
			"outage": {candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item", 0.9, "*")},
		},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"outage": {contextfabric.SubjectPullRequest: {prCandidate}},
		},
	}
	resolution, offer, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("outage"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Candidates) != 2 {
		t.Fatalf("resolution.Candidates = %#v, want exactly 2 (work_item from Search + pull_request from SearchKind)", resolution.Candidates)
	}
	foundPR := false
	for _, c := range resolution.Candidates {
		if c.Subject.Kind == contextfabric.SubjectPullRequest && c.Subject.CanonicalID == "pr_1" {
			foundPR = true
		}
	}
	if !foundPR {
		t.Fatalf("resolution.Candidates = %#v, want the SearchKind-sourced pull_request candidate present", resolution.Candidates)
	}
	if len(offer.KindOptions) != 2 {
		t.Fatalf("offer.KindOptions = %#v, want expected_kind offered across both kinds now that the pool has 2 distinct offerable kinds", offer.KindOptions)
	}
	sawWorkItem, sawPR := false, false
	for _, opt := range offer.KindOptions {
		switch opt.Kind {
		case contextfabric.SubjectWorkItem:
			sawWorkItem = true
		case contextfabric.SubjectPullRequest:
			sawPR = true
		}
	}
	if !sawWorkItem || !sawPR {
		t.Fatalf("offer.KindOptions = %#v, want both work_item and pull_request offered", offer.KindOptions)
	}
}

// TestResolveSubjects_SearchKindStopsAfterFirstHitPerKind proves the floor
// spends AT MOST as many calls as needed: once a term's own SearchKind call
// satisfies a kind's floor, no further term is queried for that SAME kind.
func TestResolveSubjects_SearchKindStopsAfterFirstHitPerKind(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{"alpha": {}, "beta": {}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"alpha": {contextfabric.SubjectPullRequest: {candidateNode(contextfabric.SubjectPullRequest, "pr_1", "Alpha PR", 0.6, "*")}},
		},
	}
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("alpha", "beta"), backend.deps(), nil, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	prCalls := 0
	for _, call := range backend.searchKindCalls {
		if call.kind == contextfabric.SubjectPullRequest {
			prCalls++
			if call.term != "alpha" {
				t.Fatalf("searchKindCalls = %#v, want the pull_request floor satisfied by \"alpha\" alone, never reaching \"beta\"", backend.searchKindCalls)
			}
		}
	}
	if prCalls != 1 {
		t.Fatalf("pull_request SearchKind calls = %d, want exactly 1 -- the floor stops as soon as it is satisfied", prCalls)
	}
}

// TestResolveSubjects_SearchKindNeverExceedsMaxTermsPerKind is codex
// CHAOS-4038 review round 2's own regression (finding 3): a kind with NO
// matching candidate anywhere must still bound how many terms the floor
// spends on it, at kindCoverageMaxTermsPerKind -- never one call per every
// term this resolution's own interpretation produced.
func TestResolveSubjects_SearchKindNeverExceedsMaxTermsPerKind(t *testing.T) {
	t.Parallel()
	terms := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	searchResults := make(map[string][]CandidateNode, len(terms))
	for _, term := range terms {
		searchResults[term] = nil
	}
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    searchResults,
		// searchKindResults left empty: no term ever satisfies pull_request,
		// so every call within the cap actually fires.
	}
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(terms...), backend.deps(), nil, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	prCalls := 0
	for _, call := range backend.searchKindCalls {
		if call.kind == contextfabric.SubjectPullRequest {
			prCalls++
		}
	}
	if prCalls != kindCoverageMaxTermsPerKind {
		t.Fatalf("pull_request SearchKind calls = %d, want exactly kindCoverageMaxTermsPerKind (%d) -- %d terms were available and none ever satisfied the floor", prCalls, kindCoverageMaxTermsPerKind, len(terms))
	}
}

// TestResolveSubjects_SearchKindSkipsAliasLookupScopedKindsWhenAliasLookupComplete
// proves the floor never calls SearchKind for repository/project/team when
// THIS resolution's own AliasLookup ran and reported complete=true -- those
// three already got a complete identity-universe read, so a supplemental
// generic lexical query would be pure duplication.
func TestResolveSubjects_SearchKindSkipsAliasLookupScopedKindsWhenAliasLookupComplete(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		enableSearchKind:     true,
		enableAliasLookup:    true,
		aliasLookupComplete:  true,
		aliasLookupClaimants: map[string][]CandidateNode{},
		searchResults:        map[string][]CandidateNode{"alpha": {}},
	}
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("alpha"), backend.deps(), nil, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	for _, call := range backend.searchKindCalls {
		if call.kind == contextfabric.SubjectRepository || call.kind == contextfabric.SubjectProject || call.kind == contextfabric.SubjectTeam {
			t.Fatalf("searchKindCalls = %#v, want no alias-lookup-scoped kind queried when AliasLookup reported complete=true", backend.searchKindCalls)
		}
	}
}

// TestResolveSubjects_SearchKindCoversAliasLookupScopedKindsWhenAliasLookupNil
// is codex CHAOS-4038 review finding 3's own regression: a backend with no
// AliasLookup at all leaves repository/project/team with NO coverage from
// any other pass, so the floor must widen to query them too.
func TestResolveSubjects_SearchKindCoversAliasLookupScopedKindsWhenAliasLookupNil(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{"alpha": {}},
	}
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("alpha"), backend.deps(), nil, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	for _, scoped := range []contextfabric.SubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectProject, contextfabric.SubjectTeam} {
		found := false
		for _, call := range backend.searchKindCalls {
			if call.kind == scoped {
				found = true
			}
		}
		if !found {
			t.Fatalf("searchKindCalls = %#v, want %q queried -- AliasLookup is nil, so nothing else covers it", backend.searchKindCalls, scoped)
		}
	}
}

// TestResolveSubjects_SearchKindCoversAliasLookupScopedKindsWhenAliasLookupIncomplete
// is the SAME regression as the nil case, for an AliasLookup that ran but
// could not prove completeness (a historical read, an exceeded row budget,
// a source-table existence-check failure) -- complete=false leaves
// repository/project/team just as uncovered as a nil AliasLookup would.
func TestResolveSubjects_SearchKindCoversAliasLookupScopedKindsWhenAliasLookupIncomplete(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		enableSearchKind:     true,
		enableAliasLookup:    true,
		aliasLookupComplete:  false,
		aliasLookupClaimants: map[string][]CandidateNode{},
		searchResults:        map[string][]CandidateNode{"alpha": {}},
	}
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("alpha"), backend.deps(), nil, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	found := false
	for _, call := range backend.searchKindCalls {
		if call.kind == contextfabric.SubjectTeam {
			found = true
		}
	}
	if !found {
		t.Fatalf("searchKindCalls = %#v, want team queried -- AliasLookup ran but reported complete=false", backend.searchKindCalls)
	}
}

// TestResolveSubjects_SearchKindOfferSurvivesFinalRankedTruncation is codex
// CHAOS-4038 review finding 1's own regression: a coverage-floor find can be
// dropped from resolution.Candidates by ResolveFromMergedCandidatesWithGate's
// final ranked-set truncation (here, MaxSubjectCandidates=1 with a
// higher-confidence work_item already filling that single slot) -- the
// expected_kind OFFER must still see both kinds regardless, or this whole
// pass is silently defeated for exactly the resolutions it exists to help.
func TestResolveSubjects_SearchKindOfferSurvivesFinalRankedTruncation(t *testing.T) {
	t.Parallel()
	strongWorkItem := candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item", 0.95, "*")
	weakPR := candidateNode(contextfabric.SubjectPullRequest, "pr_1", "Outage PR", 0.5, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{"outage": {strongWorkItem}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"outage": {contextfabric.SubjectPullRequest: {weakPR}},
		},
	}
	request := testRequest()
	request.Options.MaxSubjectCandidates = 1
	resolution, offer, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("outage"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Candidates) != 1 {
		t.Fatalf("resolution.Candidates = %#v, want exactly 1 -- MaxSubjectCandidates=1 caps the final ranked set to the stronger work_item", resolution.Candidates)
	}
	if resolution.Candidates[0].Subject.Kind != contextfabric.SubjectWorkItem {
		t.Fatalf("resolution.Candidates[0].Subject.Kind = %q, want work_item (the higher-confidence survivor)", resolution.Candidates[0].Subject.Kind)
	}
	if len(offer.KindOptions) != 2 {
		t.Fatalf("offer.KindOptions = %#v, want expected_kind offered across BOTH kinds even though pull_request did not survive the final truncation", offer.KindOptions)
	}
	sawWorkItem, sawPR := false, false
	for _, opt := range offer.KindOptions {
		switch opt.Kind {
		case contextfabric.SubjectWorkItem:
			sawWorkItem = true
		case contextfabric.SubjectPullRequest:
			sawPR = true
		}
	}
	if !sawWorkItem || !sawPR {
		t.Fatalf("offer.KindOptions = %#v, want both work_item and pull_request offered", offer.KindOptions)
	}
}

// TestResolveSubjects_SearchKindCoverageTruncationNeverBlocksAnUnrelatedCommit
// is codex CHAOS-4038 review round 2's own regression (finding 1): the
// coverage floor's OWN truncated signal (a missing-kind SearchKind call
// found more than kindCoverageQueryLimit rows) must never force an
// otherwise-decisive, UNRELATED candidate to fall back to clarification --
// that would silently contradict this pass's own "a coverage floor, never a
// competing top-K" design intent, turning a previously-clean auto-commit
// into an unnecessary ambiguity every time a rival kind happens to have a
// deep bench. The term/label exact-match path (allowExactMatch=true, term
// == node label) forces a deterministic commit regardless of confidence
// thresholds, isolating this test from the lone-floor calibration.
func TestResolveSubjects_SearchKindCoverageTruncationNeverBlocksAnUnrelatedCommit(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "wi_1", Label: "Ask Dev"}
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.9, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{"Ask Dev": {node}},
		// Every SearchKind call this resolution makes (one per missing
		// kind) reports truncated=true, regardless of content -- exactly
		// codex's repro: a rival kind's coverage query truncates even
		// though the work_item candidate above is already fully decisive.
		searchKindTruncated: true,
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != subject {
		t.Fatalf("resolution.Committed = %#v, want the exact-match work_item candidate auto-committed -- the coverage floor's own truncation must never block an unrelated commit", resolution.Committed)
	}
}

// TestResolveSubjects_SearchKindSkippedWhenConfirmedKindSet proves the floor
// never runs once a caller already confirmed a kind (CHAOS-3900 P1.D) --
// nothing is left to disambiguate on this axis, so spending extra
// kind-scoped calls would be pure waste.
func TestResolveSubjects_SearchKindSkippedWhenConfirmedKindSet(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{"alpha": {}},
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectWorkItem}
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("alpha"), backend.deps(), confirmed, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(backend.searchKindCalls) != 0 {
		t.Fatalf("searchKindCalls = %#v, want none once confirmedKind is set", backend.searchKindCalls)
	}
}

// TestResolveSubjects_SearchKindDegradedFoldsIntoResolution proves the
// floor's own degraded signal folds into resolution.RetrievalDegraded
// exactly like every other retrieval pass's would, even when every ordinary
// pass is clean.
func TestResolveSubjects_SearchKindDegradedFoldsIntoResolution(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		enableSearchKind:   true,
		searchResults:      map[string][]CandidateNode{"alpha": {}},
		searchKindDegraded: true,
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("alpha"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if !resolution.RetrievalDegraded {
		t.Fatal("resolution.RetrievalDegraded = false, want true -- the coverage floor alone reported a missing mechanism")
	}
}

// TestResolveSubjects_SearchKindPropagatesBackendError proves a genuine
// SearchKind failure aborts the whole resolution and surfaces as an error,
// exactly like a Search/SearchQuestion/AliasLookup failure does -- never
// silently downgraded to "found nothing".
func TestResolveSubjects_SearchKindPropagatesBackendError(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{"alpha": {}},
		searchKindErr:    errors.New("transient backend failure"),
	}
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("alpha"), backend.deps(), nil, nil); err == nil {
		t.Fatal("ResolveSubjects() error = nil, want the SearchKind backend failure propagated")
	}
}
