package graphrank

// Regression coverage for the scope-anchor-kind retrieval seam.
//
// The defect these pin has TWO independent gates, and the tests are built so
// that each one fails ALONE. That matters because either fix on its own
// leaves the live shape broken, and a single end-to-end test would have gone
// green on a half fix:
//
//   - GATE 1, retrieval: a children_of_scope frame declares only its MEMBER
//     kind, so the anchor's own terms were searched under the member's kind.
//     The kind-coverage floor does search the anchor's kind, but merges
//     repository/project/team into an offer-only pool that
//     resolution.Candidates never sees (the CHAOS-4271 ruling), so a team was
//     retrievable and still never offered.
//   - GATE 2, truncation: even once the anchor kind IS hinted and its
//     candidate reaches the real pool, phase 4's flat top-K drops it when a
//     lexically noisier kind fills the budget first. Measured live: the
//     "dev-health-acr" anchor retrieves 3,201 ci_pipeline_run nodes and ONE
//     repository.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// anchorScopedFrame is the frame "which repositories does the <term> team
// own?" produces: members are repositories, the anchor is named by term and
// its kind is NOT on the frame at all.
func anchorScopedFrame(term string) *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{term},
				MemberKind:  contextfabric.SubjectRepository,
			},
		},
	}
}

// lexicalCrowd is the noisier-kind population that wins the flat ranking
// race: every node shares the term but none matches it exactly.
func lexicalCrowd(term string, n int) []CandidateNode {
	nodes := make([]CandidateNode, 0, n)
	for i := 0; i < n; i++ {
		nodes = append(nodes, candidateNode(
			contractsv1.ContextFabricSubjectCIRun,
			fmt.Sprintf("ci_pipeline_run.v2:github:%s-build-%d", term, i),
			fmt.Sprintf("%s build %d", term, i), 0.9, "*"))
	}
	return nodes
}

func anchorTeamNode(term, label string) CandidateNode {
	return candidateNode(contextfabric.SubjectTeam, "team.v2:github:"+term, label, 0.4, "*")
}

func candidateKinds(res contextfabric.SubjectResolution) map[contextfabric.SubjectKind]int {
	h := make(map[contextfabric.SubjectKind]int)
	for _, c := range res.Candidates {
		h[c.Subject.Kind]++
	}
	return h
}

// resolveWithAnchor drives the PRODUCTION entry point, so a test cannot pass
// by constructing the decision it asserts on.
func resolveWithAnchor(t *testing.T, backend *fakeGraphBackend, req contextfabric.InvestigationRequest, frame *contextfabric.QuestionFrame, anchorKind contextfabric.SubjectKind) contextfabric.SubjectResolution {
	t.Helper()
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("platform"),
		backend.deps(), nil, nil, frame, anchorKind)
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	return res
}

// GATE 1 ALONE. The budget is far larger than the pool, so truncation cannot
// possibly be what removes the team: if it is absent, it is absent because
// retrieval never put it in the real pool.
func TestResolveSubjects_AnchorKindReachesTheRealCandidatePool(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{"platform": lexicalCrowd("platform", 3)},
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"platform": {
				contextfabric.SubjectTeam:       {anchorTeamNode("platform", "Platform Team")},
				contextfabric.SubjectRepository: nil,
			},
		},
	}
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20 // 20 >> 4 candidates: no truncation pressure

	res := resolveWithAnchor(t, backend, req, anchorScopedFrame("platform"), contextfabric.SubjectTeam)
	kinds := candidateKinds(res)
	if kinds[contextfabric.SubjectTeam] == 0 {
		t.Fatalf("team candidates = 0, want >=1; kinds=%v. The anchor kind never reached the REAL pool (gate 1).", kinds)
	}
}

// GATE 2 ALONE. The anchor kind IS supplied, so retrieval is not the
// question; the crowd is larger than the budget, so only a reserved slot can
// keep the team.
func TestResolveSubjects_AnchorKindSurvivesFlatTruncation(t *testing.T) {
	t.Parallel()
	const crowd = 90
	backend := &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{"platform": lexicalCrowd("platform", crowd)},
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"platform": {
				contextfabric.SubjectTeam:       {anchorTeamNode("platform", "Platform Team")},
				contextfabric.SubjectRepository: nil,
			},
		},
	}
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20 // 20 < 90: the crowd fills the budget

	res := resolveWithAnchor(t, backend, req, anchorScopedFrame("platform"), contextfabric.SubjectTeam)
	kinds := candidateKinds(res)
	if kinds[contextfabric.SubjectTeam] == 0 {
		t.Fatalf("team candidates = 0, want >=1; kinds=%v. The anchor kind reached the pool and was then truncated away (gate 2).", kinds)
	}
	if len(res.Candidates) != req.Options.MaxSubjectCandidates {
		t.Errorf("returned %d candidates, want exactly %d -- the reserve must displace, never grow the budget",
			len(res.Candidates), req.Options.MaxSubjectCandidates)
	}
}

// THE DISABLED STATE. No anchor kind means the pre-ticket behaviour exactly:
// the team is retrievable by the coverage floor and still must not appear,
// because the CHAOS-4271 offer-only ruling is untouched by this change.
func TestResolveSubjects_WithoutAnchorKindTheOfferOnlyRulingStillHolds(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{"platform": lexicalCrowd("platform", 3)},
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"platform": {contextfabric.SubjectTeam: {anchorTeamNode("platform", "Platform Team")}},
		},
	}
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20

	res := resolveWithAnchor(t, backend, req, anchorScopedFrame("platform"), "")
	if kinds := candidateKinds(res); kinds[contextfabric.SubjectTeam] != 0 {
		t.Fatalf("team candidates = %d with NO anchor kind, want 0: the offer-only pool must still withhold alias-scoped floor finds", kinds[contextfabric.SubjectTeam])
	}
}

// A kind the frame never declared must not get a reserved slot. This is the
// control the reserve's safety rests on: reservedKinds comes from the frame
// and receipt, never from a caller's assertion.
func TestReservedPrefix_OnlyReservesTheKindsItWasGiven(t *testing.T) {
	t.Parallel()
	ordered := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_1"}},
		{Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_2"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_1"}},
	}
	tiers := []int{2, 2, 2}

	none := reservedPrefix(ordered, tiers, 2, nil)
	if none[2] {
		t.Error("nil reservedKinds admitted index 2; want the plain prefix")
	}
	other := reservedPrefix(ordered, tiers, 2, []contextfabric.SubjectKind{contextfabric.SubjectProject})
	if other[2] {
		t.Error("reserving PROJECT admitted a TEAM candidate")
	}
	team := reservedPrefix(ordered, tiers, 2, []contextfabric.SubjectKind{contextfabric.SubjectTeam})
	if !team[2] {
		t.Error("reserving TEAM did not admit the team candidate")
	}
	kept := 0
	for _, k := range team {
		if k {
			kept++
		}
	}
	if kept != 2 {
		t.Errorf("kept %d, want exactly max=2 -- the reserve displaces, never grows", kept)
	}
}

// The reserve must never evict a committed subject (phase 4 promises one
// "can never be dropped by" truncation) nor a retained observation's
// canonical parent. Both are asserted, and a tier-2 victim is confirmed to
// exist so the test cannot pass by admitting nothing.
func TestReservedPrefix_NeverDisplacesCommittedOrParentTiers(t *testing.T) {
	t.Parallel()
	ordered := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "committed"}},
		{Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "parent"}},
		{Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ordinary"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_1"}},
	}
	tiers := []int{0, 1, 2, 2} // committed, parent, ordinary, (past the cut)

	kept := reservedPrefix(ordered, tiers, 3, []contextfabric.SubjectKind{contextfabric.SubjectTeam})
	if !kept[0] {
		t.Error("displaced the COMMITTED subject")
	}
	if !kept[1] {
		t.Error("displaced the canonical PARENT")
	}
	if kept[2] {
		t.Error("expected the tier-2 candidate to be the victim")
	}
	if !kept[3] {
		t.Error("reserved team candidate was not admitted")
	}
}

// When every in-budget candidate is protected there is no eligible victim,
// and the reserve must admit NOTHING rather than exceed the caller's budget.
func TestReservedPrefix_AdmitsNothingWhenNoVictimIsEligible(t *testing.T) {
	t.Parallel()
	ordered := []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "committed"}},
		{Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "parent"}},
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_1"}},
	}
	tiers := []int{0, 1, 2}

	kept := reservedPrefix(ordered, tiers, 2, []contextfabric.SubjectKind{contextfabric.SubjectTeam})
	if kept[2] {
		t.Error("admitted a reserved candidate with no eligible victim; the budget must not grow")
	}
	if !kept[0] || !kept[1] {
		t.Error("protected tiers must be retained")
	}
}

// frameReservedKinds is deliberately narrower than hintedPoolKinds: a CALLER
// hint must not buy a guaranteed slot.
func TestFrameReservedKinds_ExcludesCallerSuppliedHints(t *testing.T) {
	t.Parallel()
	frame := anchorScopedFrame("platform")
	got := frameReservedKinds(frame, contextfabric.SubjectTeam)

	var sawRepo, sawTeam bool
	for _, k := range got {
		switch k {
		case contextfabric.SubjectRepository:
			sawRepo = true
		case contextfabric.SubjectTeam:
			sawTeam = true
		}
	}
	if !sawRepo {
		t.Errorf("frameReservedKinds=%v, want the frame's member kind (repository)", got)
	}
	if !sawTeam {
		t.Errorf("frameReservedKinds=%v, want the receipt's anchor kind (team)", got)
	}
	if len(frameReservedKinds(nil, "")) != 0 {
		t.Error("nil frame with no anchor kind must reserve nothing")
	}
}

// THE ELIGIBILITY CONTROL team-lead required. The reserve runs in phase 4,
// after phase 3 has already decided over the FULL untruncated set, so it
// cannot change what commits -- for the reserved kind OR for any other. This
// asserts that directly: the identical population resolved with and without a
// reserve must produce an identical Committed set and identical states.
//
// The non-vacuity guard matters as much as the assertion: if the reserve
// never actually admitted anything, "commit decisions unchanged" would be
// trivially true and would prove nothing.
func TestReservedKinds_DoNotChangeCommitDecisions(t *testing.T) {
	t.Parallel()
	build := func() map[string]contextfabric.SubjectCandidate {
		pool := make(map[string]contextfabric.SubjectCandidate)
		for i := 0; i < 6; i++ {
			c := contextfabric.SubjectCandidate{
				Subject:    contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: fmt.Sprintf("ci_%d", i), Label: fmt.Sprintf("ci %d", i)},
				State:      contractsv1.ContextFabricResolutionAmbiguous,
				Confidence: 0.9,
			}
			pool[SubjectKey(c.Subject)] = c
		}
		team := contextfabric.SubjectCandidate{
			Subject:    contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_1", Label: "Platform Team"},
			State:      contractsv1.ContextFabricResolutionAmbiguous,
			Confidence: 0.4,
		}
		pool[SubjectKey(team.Subject)] = team
		return pool
	}

	call := func(reserved []contextfabric.SubjectKind) contextfabric.SubjectResolution {
		res, _, _ := ResolveFromMergedCandidatesWithGateAndBasis(
			build(), map[string]string{}, map[string]bool{}, 3, true, false,
			nil, 0, false, 10, 20, true,
			DefaultCommitGatePolicy(), nil, nil, false, nil, "", "", false, false, reserved)
		return res
	}

	without := call(nil)
	with := call([]contextfabric.SubjectKind{contextfabric.SubjectTeam})

	// NON-VACUITY: the reserve must actually have done something here, or
	// this test proves nothing about the property it claims.
	teamIn := func(res contextfabric.SubjectResolution) bool {
		for _, c := range res.Candidates {
			if c.Subject.Kind == contextfabric.SubjectTeam {
				return true
			}
		}
		return false
	}
	if teamIn(without) {
		t.Fatal("setup is vacuous: the team survived truncation even WITHOUT a reserve")
	}
	if !teamIn(with) {
		t.Fatal("setup is vacuous: the reserve admitted nothing, so 'commit unchanged' proves nothing")
	}

	if len(without.Committed) != len(with.Committed) {
		t.Fatalf("committed count changed: without=%v with=%v", without.Committed, with.Committed)
	}
	for i := range without.Committed {
		if without.Committed[i] != with.Committed[i] {
			t.Errorf("committed[%d] changed: %v -> %v", i, without.Committed[i], with.Committed[i])
		}
	}
	// The clarification prompt is built from the RETAINED (post-truncation)
	// set by design, so it SHOULD change -- naming the reserved candidate is
	// the entire user-visible point of the reserve. Asserted positively
	// rather than pinned to equality, which was the author's first and wrong
	// property here.
	if with.ClarificationPrompt == without.ClarificationPrompt {
		t.Errorf("clarification prompt unchanged (%q); the reserved candidate must be offered to the client", with.ClarificationPrompt)
	}
	if !strings.Contains(with.ClarificationPrompt, "Platform Team") {
		t.Errorf("clarification prompt = %q, want it to name the reserved team candidate", with.ClarificationPrompt)
	}
	if len(without.Candidates) != len(with.Candidates) {
		t.Errorf("candidate COUNT changed: %d -> %d; the reserve must displace, not grow",
			len(without.Candidates), len(with.Candidates))
	}
}
