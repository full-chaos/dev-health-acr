package graphrank

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestKindCoverageFloorKinds_IncludesAliasLookupScopedKinds is the CHAOS-4271
// fix: repository/project/team (isAliasLookupScopedKind) are now part of
// kindCoverageFloorKinds, exactly like the four census kinds -- a completed
// AliasLookup read no longer excludes them from the floor's own kind set;
// whether AliasLookup already found one is decided by pool presence
// (missingCoverageKinds), the same mechanism the census kinds always used.
func TestKindCoverageFloorKinds_IncludesAliasLookupScopedKinds(t *testing.T) {
	t.Parallel()
	for _, scoped := range []contextfabric.SubjectKind{
		contextfabric.SubjectRepository, contextfabric.SubjectProject, contextfabric.SubjectTeam,
	} {
		if !kindCoverageFloorKinds[scoped] {
			t.Errorf("kindCoverageFloorKinds[%q] = false, want true -- alias-lookup-scoped kinds must be included (CHAOS-4271)", scoped)
		}
	}
}

// TestKindCoverageFloorKinds_MatchesStructureOfferKindsExactly proves
// kindCoverageFloorKinds is EXACTLY structureOfferKinds -- never a second,
// independently drifting list.
func TestKindCoverageFloorKinds_MatchesStructureOfferKindsExactly(t *testing.T) {
	t.Parallel()
	for kind := range structureOfferKinds {
		if !kindCoverageFloorKinds[kind] {
			t.Errorf("kindCoverageFloorKinds[%q] = false, want true", kind)
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
// represented in the pool is excluded from the missing set -- the SAME check
// now governs alias-lookup-scoped kinds too (CHAOS-4271): a repository the
// pool already has (from AliasLookup or any other pass) never re-triggers a
// coverage query, exactly like a census kind.
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
	}
	if len(got) != len(kindCoverageFloorKinds)-1 {
		t.Fatalf("missingCoverageKinds(pool with work_item) = %#v, want exactly one fewer than the full floor set", got)
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
// CHAOS-4271: repository/project/team are now part of the floor's own kind
// set too, so this fixture covers all seven -- repository via a genuine
// AliasLookup claimant (unlike the "complete but unmatched" regression
// above), the remaining six (including project/team) via ordinary Search --
// proving the fix adds no extra I/O when a kind is already covered by ANY
// pass, not just AliasLookup.
func TestResolveSubjects_SearchKindSkippedWhenPoolAlreadyCoversAllFloorKinds(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		enableSearchKind:     true,
		enableAliasLookup:    true,
		aliasLookupComplete:  true,
		aliasLookupClaimants: map[string][]CandidateNode{"outage": {candidateNode(contextfabric.SubjectRepository, "repo_1", "acr", 0.9, "*")}},
		searchResults: map[string][]CandidateNode{
			"outage": {
				candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item", 0.9, "*"),
				candidateNode(contextfabric.SubjectPullRequest, "pr_1", "Outage PR", 0.9, "*"),
				candidateNode(contractsv1.ContextFabricSubjectCIRun, "ci_1", "Outage CI run", 0.9, "*"),
				candidateNode(contractsv1.ContextFabricSubjectPullRequestReview, "prr_1", "Outage PR review", 0.9, "*"),
				candidateNode(contextfabric.SubjectProject, "proj_1", "Outage project", 0.9, "*"),
				candidateNode(contextfabric.SubjectTeam, "team_1", "Outage team", 0.9, "*"),
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

// TestResolveSubjects_SearchKindRescuesAliasLookupScopedKindWhenAliasLookupCompleteButUnmatched
// is the CHAOS-4271 regression: AliasLookup ran and reported complete=true
// (the identity-universe SOURCE READ was not budget-truncated) but matched
// NOTHING for this resolution's terms (aliasLookupClaimants is empty) --
// "read complete" is not "target found". Before the fix, complete=true alone
// excluded repository/project/team from the floor's own missing-kind check,
// so a genuinely absent repository had no rescue path at all even though the
// SAME lexical SearchKind mechanism that rescues work_item/pull_request/
// ci_run/pull_request_review in this exact situation could have found it.
func TestResolveSubjects_SearchKindRescuesAliasLookupScopedKindWhenAliasLookupCompleteButUnmatched(t *testing.T) {
	t.Parallel()
	repoCandidate := candidateNode(contextfabric.SubjectRepository, "repo_1", "acr", 0.6, "*")
	backend := &fakeGraphBackend{
		enableSearchKind:     true,
		enableAliasLookup:    true,
		aliasLookupComplete:  true,
		aliasLookupClaimants: map[string][]CandidateNode{},
		searchResults:        map[string][]CandidateNode{"alpha": {}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"alpha": {contextfabric.SubjectRepository: {repoCandidate}},
		},
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("alpha"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	found := false
	for _, call := range backend.searchKindCalls {
		if call.kind == contextfabric.SubjectRepository {
			found = true
		}
	}
	if !found {
		t.Fatalf("searchKindCalls = %#v, want repository queried -- AliasLookup completed but matched nothing, so the floor must still try", backend.searchKindCalls)
	}
	foundRepo := false
	for _, c := range resolution.Candidates {
		if c.Subject.Kind == contextfabric.SubjectRepository && c.Subject.CanonicalID == "repo_1" {
			foundRepo = true
		}
	}
	if !foundRepo {
		t.Fatalf("resolution.Candidates = %#v, want the SearchKind-rescued repository candidate present", resolution.Candidates)
	}
}

// TestResolveSubjects_SearchKindRescuesOnlyTheAliasLookupScopedKindsAliasLookupMissed
// is the CHAOS-4271 codex round-1 follow-up (partial-match case): AliasLookup
// completes and matches ONE of the three alias-lookup-scoped kinds (project)
// but not the other two (repository, team) -- proves the rescue is decided
// per-kind by pool presence, not by one resolution-wide flag. The matched
// kind must NEVER reach SearchKind (would be pure duplication); the two
// unmatched kinds must.
func TestResolveSubjects_SearchKindRescuesOnlyTheAliasLookupScopedKindsAliasLookupMissed(t *testing.T) {
	t.Parallel()
	projectCandidate := candidateNode(contextfabric.SubjectProject, "proj_1", "Widgets", 0.6, "*")
	backend := &fakeGraphBackend{
		enableSearchKind:     true,
		enableAliasLookup:    true,
		aliasLookupComplete:  true,
		aliasLookupClaimants: map[string][]CandidateNode{"alpha": {projectCandidate}},
		searchResults:        map[string][]CandidateNode{"alpha": {}},
	}
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("alpha"), backend.deps(), nil, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	for _, call := range backend.searchKindCalls {
		if call.kind == contextfabric.SubjectProject {
			t.Fatalf("searchKindCalls = %#v, want project never queried -- AliasLookup already matched it", backend.searchKindCalls)
		}
	}
	for _, scoped := range []contextfabric.SubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectTeam} {
		found := false
		for _, call := range backend.searchKindCalls {
			if call.kind == scoped {
				found = true
			}
		}
		if !found {
			t.Fatalf("searchKindCalls = %#v, want %q queried -- AliasLookup did not match it", backend.searchKindCalls, scoped)
		}
	}
}

// TestResolveSubjects_SearchKindRescuedRepositoryNeverAutoCommitsWithoutDecisiveGrounds
// is CHAOS-4271's own commit-safety proof (team-lead condition 2, 2026-08-25):
// widening the floor's kind set to admit repository does not touch how a
// found candidate is GATED -- a coverage-floor find merges through the SAME
// mergeSearchResults/NodeCandidate/commit-gate path every other pass' finds
// already do (chaos4038_kind_coverage.go's own doc comment), so an
// ordinary-confidence, non-exact-match repository rescue is exactly as
// non-decisive as an ordinary-confidence work_item rescue always was --
// CHAOS-4271 changes WHICH candidates the floor can find, never HOW a found
// candidate is judged. This is deliberately NOT "floor finds can never
// commit" (false: TestResolveSubjects_SearchKindCoverageTruncationNeverBlocksAnUnrelatedCommit
// already proves an EXACT-match floor find commits normally, by design,
// same as any other pass) -- it is the narrower, correct claim: no NEW
// commit path opens for repository specifically, and the CHAOS-4040/4085
// gate machinery (untouched by this diff -- zero changes outside
// chaos4038_kind_coverage.go, its test, and one resolve.go call-site
// argument/comment) governs identically regardless of which pass a
// candidate arrived through.
func TestResolveSubjects_SearchKindRescuedRepositoryNeverAutoCommitsWithoutDecisiveGrounds(t *testing.T) {
	t.Parallel()
	// A second, ordinary-pass work_item candidate keeps the pool at 2
	// distinct offerable kinds -- a single-kind pool with no explicit hint
	// is suppressed by kindOfferMaterial's own disambiguation gate
	// (chaos3900_structure_offers.go), which would make this test's offer
	// assertion pass for the wrong reason (suppression, not visibility).
	strongWorkItem := candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item", 0.9, "*")
	weakRepo := candidateNode(contextfabric.SubjectRepository, "repo_1", "acr", 0.5, "*")
	backend := &fakeGraphBackend{
		enableSearchKind:     true,
		enableAliasLookup:    true,
		aliasLookupComplete:  true,
		aliasLookupClaimants: map[string][]CandidateNode{},
		searchResults:        map[string][]CandidateNode{"outage": {strongWorkItem}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"outage": {contextfabric.SubjectRepository: {weakRepo}},
		},
	}
	resolution, offer, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("outage"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	foundOffered := false
	for _, c := range resolution.Candidates {
		if c.Subject.Kind == contextfabric.SubjectRepository && c.Subject.CanonicalID == "repo_1" {
			foundOffered = true
		}
	}
	if !foundOffered {
		t.Fatalf("resolution.Candidates = %#v, want the SearchKind-rescued repository present as an offer candidate", resolution.Candidates)
	}
	// Only the repository's OWN commit-decisiveness is this test's claim --
	// the strong work_item candidate committing on its own separate,
	// pre-existing grounds (a single decisive claimant for its own term) is
	// expected and irrelevant here; asserting a blanket empty Committed
	// would conflate the two.
	for _, committed := range resolution.Committed {
		if committed.Kind == contextfabric.SubjectRepository {
			t.Fatalf("resolution.Committed = %#v, want repository NOT auto-committed -- an ordinary-confidence, non-exact-match coverage-floor find is never decisive on its own, exactly like the pre-CHAOS-4271 census-kind floor finds", resolution.Committed)
		}
	}
	foundOffer := false
	for _, opt := range offer.KindOptions {
		if opt.Kind == contextfabric.SubjectRepository {
			foundOffer = true
		}
	}
	if !foundOffer {
		t.Fatalf("offer.KindOptions = %#v, want repository present as an expected_kind offer option", offer.KindOptions)
	}
}

// TestResolveSubjects_SearchKindCoversAliasLookupScopedKindsWhenAliasLookupNil
// proves a backend with no AliasLookup at all still gets repository/project/
// team covered by the floor (CHAOS-4271: they are unconditionally part of
// kindCoverageFloorKinds now) -- nothing else in the resolution covers them
// in this case.
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
// is the SAME proof as the nil case, for an AliasLookup that ran but could
// not prove completeness (a historical read, an exceeded row budget, a
// source-table existence-check failure) and found no claimants -- the floor
// covers repository/project/team regardless (CHAOS-4271).
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
//
// CHAOS-4132: the ordinary pool here MUST already carry a candidate of the
// confirmed kind. An earlier version of this fixture instead returned
// NOTHING at all for its term -- which, before CHAOS-4132, only happened to
// read as "the floor is skipped", but is exactly the starved-kind shape
// CHAOS-4132's own confirmed-kind rescue exists to fix (see
// TestResolveSubjects_ConfirmedKindRescueFiresWhenPoolEmptyAfterFiltering);
// under the fix, that old fixture would make THIS test fail for the right
// reason, not the wrong one. The negative control this test claims to be
// only holds when the confirmed kind's candidates are already present.
func TestResolveSubjects_SearchKindSkippedWhenConfirmedKindSet(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "wi_1", Label: "Ask Dev"}
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.9, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{"Ask Dev": {node}},
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectWorkItem}
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), backend.deps(), confirmed, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(backend.searchKindCalls) != 0 {
		t.Fatalf("searchKindCalls = %#v, want none once confirmedKind is set and the ordinary pool already covers it", backend.searchKindCalls)
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
