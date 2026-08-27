package graphrank

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestResolveSubjects_HintedProjectKindReachesRealPool is CHAOS-4348's own
// red-first proof for the kind-hinted supplemental pass: a project the
// ordinary (unscoped) Search call cannot find at all -- simulating the
// live-verified token-crowd-out defect (root cause §1) -- but that
// deps.SearchKind DOES find when kind-scoped, MUST reach the real pool
// (the "corroboration" trace stage, exactly what expected_subject_in_pool
// reads) when the request carries an explicit ExpectedKinds hint for
// project. Before this ticket, a hinted-but-unscoped-search-missed kind had
// NO path into candidatesBySubject at all -- applyKindCoverageFloor only
// ever runs when confirmedKind is nil AND merges repository/project/team
// into a private offerOnlyPool it never returns to the caller as a real
// candidate -- so this test fails on main.
func TestResolveSubjects_HintedProjectKindReachesRealPool(t *testing.T) {
	t.Parallel()
	target := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:linear:chaos-ops", Label: "chaos-ops"}
	targetNode := candidateNode(target.Kind, target.CanonicalID, target.Label, 1.0, "*")
	backend := &fakeGraphBackend{
		// Ordinary Search finds NOTHING for this term -- the crowd-out this
		// ticket's own root-cause account describes. An empty (nil) entry,
		// not merely absent, proves the term really was searched and really
		// came back empty, not skipped.
		searchResults:     map[string][]CandidateNode{"chaos-ops": nil},
		enableSearchKind:  true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{"chaos-ops": {contextfabric.SubjectProject: {targetNode}}},
	}
	request := testRequest()
	request.ExpectedKinds = []contextfabric.SubjectKind{contextfabric.SubjectProject}
	tracer := &captureResolutionTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer

	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("chaos-ops"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}

	found := false
	for _, e := range tracer.eventsForStage("corroboration") {
		if e.Subject == target {
			found = true
		}
	}
	if !found {
		t.Fatalf("corroboration events = %#v, want one naming %#v (the hinted kind must reach the real pool)", tracer.eventsForStage("corroboration"), target)
	}
	if len(backend.searchKindCalls) == 0 {
		t.Fatal("searchKindCalls is empty, want at least one call -- the hinted pass never ran deps.SearchKind")
	}
	inCandidates := false
	for _, c := range resolution.Candidates {
		if c.Subject == target {
			inCandidates = true
		}
	}
	if !inCandidates {
		t.Fatalf("resolution.Candidates = %#v, want %#v present", resolution.Candidates, target)
	}
}

// TestResolveSubjects_ExactNameProjectReachesRealPoolAsSingleClaimant is
// CHAOS-4348's red-first proof for the exact-name arm: a project whose
// label equals the term EXACTLY, that ordinary Search cannot find (the same
// crowd-out simulation), with NO kind hint at all, reaches the real pool as
// a single, high-confidence claimant (Confidence 1, MatchExact) purely
// because ResolveDeps.ExactNameCandidates returned it and the term matched
// its label exactly.
func TestResolveSubjects_ExactNameProjectReachesRealPoolAsSingleClaimant(t *testing.T) {
	t.Parallel()
	target := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:gitlab:chaos-ops", Label: "chaos-ops"}
	targetNode := candidateNode(target.Kind, target.CanonicalID, target.Label, 0, "*")
	backend := &fakeGraphBackend{
		searchResults:             map[string][]CandidateNode{"chaos-ops": nil},
		enableExactNameCandidates: true,
		exactNameCandidates:       []CandidateNode{targetNode},
	}
	request := testRequest()
	// Deliberately NO request.ExpectedKinds -- proves the exact-name arm
	// needs no hint at all, unlike the kind-hinted pass above.
	tracer := &captureResolutionTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer

	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("chaos-ops"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if backend.exactNameCandidatesCalls != 1 {
		t.Fatalf("exactNameCandidatesCalls = %d, want exactly 1 (once per resolution, not per term)", backend.exactNameCandidatesCalls)
	}

	var committed bool
	for _, c := range resolution.Committed {
		if c == target {
			committed = true
		}
	}
	if !committed {
		t.Fatalf("resolution.Committed = %#v, want %#v committed (exact match, single claimant, nothing else in the pool)", resolution.Committed, target)
	}
	var winner *contextfabric.SubjectCandidate
	for i := range resolution.Candidates {
		if resolution.Candidates[i].Subject == target {
			winner = &resolution.Candidates[i]
		}
	}
	if winner == nil {
		t.Fatalf("resolution.Candidates = %#v, want %#v present", resolution.Candidates, target)
	}
	if winner.Confidence != 1 || !HasMechanism(winner.MatchMechanisms, contextfabric.MatchExact) {
		t.Fatalf("winning candidate = %#v, want Confidence=1 and MatchExact", winner)
	}
}

// TestResolveSubjects_ExactNameCollisionAcrossKindsNeverAutoCommits proves
// the "single-claimant only" safety property the ruling required: two
// different subjects (a project and a team) whose labels BOTH equal the
// same term exactly must NOT auto-commit either one -- the pre-existing
// identityCollision guard (chaos3884_identity.go), reused unchanged, is
// what enforces this; this test proves the CHAOS-4348 arm actually feeds
// that guard (real identity/identityTerms, never nil, unlike
// applyKindCoverageFloor's deliberate nil for these same three kinds).
func TestResolveSubjects_ExactNameCollisionAcrossKindsNeverAutoCommits(t *testing.T) {
	t.Parallel()
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:linear:ambiguous", Label: "ambiguous"}
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:ambiguous", Label: "ambiguous"}
	backend := &fakeGraphBackend{
		searchResults:             map[string][]CandidateNode{"ambiguous": nil},
		enableExactNameCandidates: true,
		exactNameCandidates: []CandidateNode{
			candidateNode(project.Kind, project.CanonicalID, project.Label, 0, "*"),
			candidateNode(team.Kind, team.CanonicalID, team.Label, 0, "*"),
		},
	}
	request := testRequest()
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("ambiguous"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	for _, c := range resolution.Committed {
		if c == project || c == team {
			t.Fatalf("resolution.Committed = %#v, want NEITHER %#v nor %#v auto-committed (two exact matches for the same term must collide, not commit)", resolution.Committed, project, team)
		}
	}
}

// TestApplyKindHintedPoolSearch_NoHintProducesByteIdenticalPool and
// TestApplyExactNameArm_NoMatchProducesByteIdenticalPool are CHAOS-4348's
// own blast-radius-control proof: a resolution that carries no kind hint
// and no exact-name match must be byte-identical whether the two new hooks
// are wired or entirely absent -- ranking, truncation, and the decision for
// every OTHER kind must never move.
func TestResolveSubjects_UnhintedNonNameQuestionProducesByteIdenticalPool(t *testing.T) {
	t.Parallel()
	workItem := candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Fix the thing", 0.6, "*")
	// A repository/project/team node that exists but matches NOTHING this
	// resolution asks about -- proves the arm is a genuine no-op on a real,
	// non-empty candidate set, not merely on an empty one.
	unrelatedProject := candidateNode(contextfabric.SubjectProject, "project.v2:linear:unrelated", "Unrelated Project", 0, "*")

	runOnce := func(wireExtras bool) contextfabric.SubjectResolution {
		backend := &fakeGraphBackend{
			searchResults: map[string][]CandidateNode{"the thing": {workItem}},
		}
		if wireExtras {
			backend.enableSearchKind = true
			backend.searchKindResults = map[string]map[contextfabric.SubjectKind][]CandidateNode{}
			backend.enableExactNameCandidates = true
			backend.exactNameCandidates = []CandidateNode{unrelatedProject}
		}
		request := testRequest()
		// No ExpectedKinds, no confirmedKind, and testInterpreted's
		// RequestedJudgment/SubjectTerms below carry no
		// repository/project/team keyword -- inferredKindHints must return
		// nothing either.
		resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("the thing"), backend.deps(), nil, nil)
		if err != nil {
			t.Fatalf("ResolveSubjects() error = %v", err)
		}
		return resolution
	}

	without := runOnce(false)
	with := runOnce(true)
	if !reflect.DeepEqual(without, with) {
		t.Fatalf("resolution differs when the CHAOS-4348 hooks are wired but nothing hints/matches:\n  without: %#v\n  with:    %#v", without, with)
	}
}
