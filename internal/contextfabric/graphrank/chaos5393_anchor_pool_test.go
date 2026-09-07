package graphrank

// A CONFIRMED MEMBER KIND MUST NOT NARROW THE POOL THAT RESOLVES THE SCOPE
// ANCHOR.
//
// On a children_of_scope frame the caller confirms the kind of the MEMBERS it
// wants ("projects"), while the subject this resolution has to commit is the
// ANCHOR those members hang off ("the CHAOS team"). The design's phase-B
// table states the asymmetry as invariant I11 -- "the RESOLVED anchor's kind
// != MemberKind ... the anchor's kind is unknown until the term resolves" --
// so a pool filtered to the confirmed member kind can only ever return
// nothing, or an anchor that violates I11 by construction.
//
// WHAT THE ROW DID. A caller answered BOTH structure needs truthfully
// (expected_kind = the member kind, subject_anchor = the anchor's receipt)
// and got no_match: the anchor's candidates were dropped by the confirmed-kind
// filter, the pool went empty, and the rescue then re-searched the ANCHOR's
// terms under the MEMBER's kind -- attempted=true, fired=false, result_count=0.
//
// These pins are written so each half fails ALONE, because the anchor kind
// has TWO sources and a fix wired to only one leaves the other shape broken.

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// scopedProjectsFrame is the corpus row's own shape: members are projects,
// the anchor is named by term, and -- per ScopedSetExpression -- the anchor's
// KIND is not on the frame at all.
func scopedProjectsFrame(term string) *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{term},
				MemberKind:  contextfabric.SubjectProject,
			},
		},
	}
}

// anchorRowBackend retrieves the anchor under its OWN kind and nothing under
// the member kind -- the live shape, where "CHAOS" names a team and no
// project carries that name.
func anchorRowBackend(term string) *fakeGraphBackend {
	return &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{term: {anchorTeamNode(term, "CHAOS Team")}},
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			term: {
				contextfabric.SubjectTeam:    {anchorTeamNode(term, "CHAOS Team")},
				contextfabric.SubjectProject: nil,
			},
		},
	}
}

// resolveScoped drives the PRODUCTION entry point with both caller
// confirmations in place, so no test here can pass by building the decision
// it asserts on.
func resolveScoped(t *testing.T, backend *fakeGraphBackend, frame *contextfabric.QuestionFrame,
	confirmedKind *contextfabric.ConfirmedExpectedKind, confirmedAnchor *contextfabric.ConfirmedAnchorSelection,
	receiptAnchorKind contextfabric.SubjectKind) contextfabric.SubjectResolution {
	t.Helper()
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("chaos"),
		backend.deps(), confirmedKind, confirmedAnchor, frame, receiptAnchorKind)
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	return res
}

func confirmedProject() *contextfabric.ConfirmedExpectedKind {
	return &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectProject}
}

// SOURCE 1 -- THE RECEIPT. The classification receipt carried a scope-anchor
// kind, so ScopeAnchorRetrievalKind returns it and the pool must admit it.
func TestAConfirmedMemberKindDoesNotWithholdTheReceiptsScopeAnchor(t *testing.T) {
	t.Parallel()
	res := resolveScoped(t, anchorRowBackend("chaos"), scopedProjectsFrame("chaos"),
		confirmedProject(), nil, contextfabric.SubjectTeam)
	if got := candidateKinds(res)[contextfabric.SubjectTeam]; got == 0 {
		t.Fatalf("team candidates = 0, want >= 1; kinds=%v. The confirmed MEMBER kind (project) withheld the SCOPE ANCHOR (team), which I11 says can never be the member kind -- so this pool could not have resolved the anchor under any outcome.", candidateKinds(res))
	}
}

// SOURCE 2 -- THE CONFIRMED ANCHOR, and it fails ALONE. The receipt carried
// NO scope-anchor kind (the model omitted it), so ScopeAnchorRetrievalKind
// returns "" and the only surviving statement of the anchor's kind is the
// caller's own redeemed anchor receipt. A fix wired solely to the receipt
// leaves exactly this shape at no_match.
func TestTheAnchorKindFallsBackToTheConfirmedAnchorReceipt(t *testing.T) {
	t.Parallel()
	res := resolveScoped(t, anchorRowBackend("chaos"), scopedProjectsFrame("chaos"),
		confirmedProject(),
		&contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:chaos"},
		"")
	if got := candidateKinds(res)[contextfabric.SubjectTeam]; got == 0 {
		t.Fatalf("team candidates = 0, want >= 1; kinds=%v. With no scope-anchor kind on the receipt the caller's CONFIRMED anchor is the only remaining statement of the anchor's kind, and it was ignored.", candidateKinds(res))
	}
}

// POSITIVE CONTROL -- member discovery still honours the confirmed kind.
// Admitting the anchor's kind must admit THAT kind and nothing else: a third
// kind sharing the term stays excluded exactly as before.
func TestTheConfirmedKindStillExcludesEveryOtherKind(t *testing.T) {
	t.Parallel()
	backend := anchorRowBackend("chaos")
	backend.searchResults["chaos"] = append(backend.searchResults["chaos"], lexicalCrowd("chaos", 3)...)
	res := resolveScoped(t, backend, scopedProjectsFrame("chaos"),
		confirmedProject(), nil, contextfabric.SubjectTeam)
	kinds := candidateKinds(res)
	if kinds[contractsv1.ContextFabricSubjectCIRun] != 0 {
		t.Errorf("ci_pipeline_run candidates = %d, want 0; kinds=%v. Admitting the anchor's kind must not reopen the pool to every kind -- the confirmed kind still scopes MEMBER discovery.", kinds[contractsv1.ContextFabricSubjectCIRun], kinds)
	}
	if kinds[contextfabric.SubjectTeam] == 0 {
		t.Errorf("team candidates = 0; the anchor must still be admitted while the third kind is refused -- kinds=%v", kinds)
	}
}

// NEGATIVE CONTROL -- a frame with no scope anchor is untouched. Without this
// arm a fix that simply stopped filtering would satisfy both pins above.
func TestANonScopeAnchoredFrameStillFiltersToTheConfirmedKind(t *testing.T) {
	t.Parallel()
	namedKind := contextfabric.SubjectProject
	named := &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:  contextfabric.SubjectExpressionNamed,
			Named: &contextfabric.NamedSubjectExpression{Terms: []string{"chaos"}, ExpectedKind: &namedKind},
		},
	}
	res := resolveScoped(t, anchorRowBackend("chaos"), named, confirmedProject(), nil, "")
	if got := candidateKinds(res)[contextfabric.SubjectTeam]; got != 0 {
		t.Fatalf("team candidates = %d, want 0 on a frame with NO scope anchor; kinds=%v. The confirmed kind must still narrow the pool everywhere this ticket does not reach.", got, candidateKinds(res))
	}
}

// anchorScopeCapture keeps the folded decision_summary AND the anchor_pool
// summaries it folded, so the reported scope can be checked against its own
// source rather than against a second literal this test also wrote.
type anchorScopeCapture struct {
	summaries  []ResolutionTraceEvent
	anchorPool []ResolutionTraceEvent
}

func (c *anchorScopeCapture) Trace(event ResolutionTraceEvent) {
	switch {
	case event.Stage == "decision_summary":
		c.summaries = append(c.summaries, event)
	case event.Stage == "anchor_pool" && event.AnchorPoolSummary:
		c.anchorPool = append(c.anchorPool, event)
	}
}

func resolveCapturingAnchorScope(t *testing.T, capture *anchorScopeCapture, backend *fakeGraphBackend,
	frame *contextfabric.QuestionFrame, confirmedKind *contextfabric.ConfirmedExpectedKind,
	confirmedAnchor *contextfabric.ConfirmedAnchorSelection, receiptAnchorKind contextfabric.SubjectKind) contextfabric.SubjectResolution {
	t.Helper()
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20
	deps := backend.deps()
	deps.ResolutionTracer = capture
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("chaos"),
		deps, confirmedKind, confirmedAnchor, frame, receiptAnchorKind)
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	return res
}

// THE OBSERVABLE, ON THE FOLDED LINE, BY VALUE.
//
// The three keys must be asserted, not merely present: a build that hardcoded
// `none` would satisfy a structural guard and a leak guard both, and would
// then report every scope-anchored resolution as admitting no anchor kind --
// which is precisely the state this ticket exists to make visible.
//
// The `_source` arms fail INDEPENDENTLY. The two sources go missing for
// unrelated reasons and need unrelated fixes, so a line that named the kind
// without naming where it came from would send an operator to the wrong half.
func TestTheDecisionSummaryNamesTheAnchorScopeAndItsSource(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name              string
		frame             *contextfabric.QuestionFrame
		confirmedKind     *contextfabric.ConfirmedExpectedKind
		confirmedAnchor   *contextfabric.ConfirmedAnchorSelection
		receiptAnchorKind contextfabric.SubjectKind
		wantScope         string
		wantSource        string
		wantMemberKind    string
		wantAnchorInPool  bool
	}{
		{
			name:  "the receipt carried the anchor kind",
			frame: scopedProjectsFrame("chaos"), confirmedKind: confirmedProject(),
			receiptAnchorKind: contextfabric.SubjectTeam,
			wantScope:         "team", wantSource: "receipt", wantMemberKind: "project", wantAnchorInPool: true,
		},
		{
			name:  "the receipt carried none and the confirmed anchor supplied it",
			frame: scopedProjectsFrame("chaos"), confirmedKind: confirmedProject(),
			confirmedAnchor: &contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:chaos"},
			wantScope:       "team", wantSource: "confirmed_anchor", wantMemberKind: "project", wantAnchorInPool: true,
		},
		{
			// EXPLICIT `none`, THREE TIMES. This is the ordinary case, and
			// it is the arm that gives the two above their meaning: if the
			// keys appeared only when a scope existed, their absence on a
			// normal line could not be told from a build that stopped
			// emitting them.
			name:  "no scope anchor at all",
			frame: nil, confirmedKind: confirmedProject(),
			wantScope: "none", wantSource: "none", wantMemberKind: "project",
		},
		{
			// The member kind is its own key and its own failure: a
			// resolution that confirmed no kind never filtered at all, and
			// the scope beside it means nothing without it.
			name:  "a scope anchor with no confirmed member kind",
			frame: scopedProjectsFrame("chaos"), receiptAnchorKind: contextfabric.SubjectTeam,
			wantScope: "team", wantSource: "receipt", wantMemberKind: "none", wantAnchorInPool: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			capture := &anchorScopeCapture{}
			res := resolveCapturingAnchorScope(t, capture, anchorRowBackend("chaos"), testCase.frame,
				testCase.confirmedKind, testCase.confirmedAnchor, testCase.receiptAnchorKind)

			if len(capture.summaries) != 1 {
				t.Fatalf("captured %d decision_summary events, want exactly 1", len(capture.summaries))
			}
			got := capture.summaries[0]
			if got.DecisionAnchorPoolKindScope != testCase.wantScope {
				t.Errorf("anchor_pool_kind_scope on the FOLDED line = %q, want %q", got.DecisionAnchorPoolKindScope, testCase.wantScope)
			}
			if got.DecisionAnchorPoolKindScopeSource != testCase.wantSource {
				t.Errorf("anchor_pool_kind_scope_source on the FOLDED line = %q, want %q -- the two sources fail independently and need different fixes", got.DecisionAnchorPoolKindScopeSource, testCase.wantSource)
			}
			if got.DecisionMemberKindConfirmed != testCase.wantMemberKind {
				t.Errorf("member_kind_confirmed on the FOLDED line = %q, want %q", got.DecisionMemberKindConfirmed, testCase.wantMemberKind)
			}

			// THE IDENTITY: the scope on the line must be the scope the
			// FILTER obeyed. It is read back off the pool -- if the line
			// names a kind, a candidate of that kind survived a filter that
			// would otherwise have dropped it; if the line says `none`, no
			// candidate of a non-member kind survived. Only this goes red
			// when the reported scope drifts from the pool it describes
			// while still agreeing with a literal above.
			if testCase.wantAnchorInPool && candidateKinds(res)[contextfabric.SubjectTeam] == 0 {
				t.Errorf("the line claims anchor scope %q but NO candidate of that kind is in the pool -- the reported scope is not the one the filter obeyed; kinds=%v", got.DecisionAnchorPoolKindScope, candidateKinds(res))
			}
			if !testCase.wantAnchorInPool && testCase.confirmedKind != nil && candidateKinds(res)[contextfabric.SubjectTeam] != 0 {
				t.Errorf("the line claims anchor scope %q but a team candidate survived the confirmed-kind filter anyway; kinds=%v", got.DecisionAnchorPoolKindScope, candidateKinds(res))
			}

			// The folded value must equal what the anchor_pool summary
			// reported, not a value re-derived beside the line.
			if testCase.frame != nil || testCase.receiptAnchorKind != "" {
				if len(capture.anchorPool) == 0 {
					t.Fatal("no anchor_pool summary was emitted, so the folded line reports a scope with no source to agree with")
				}
				last := capture.anchorPool[len(capture.anchorPool)-1]
				if got.DecisionAnchorPoolKindScope != last.DecisionAnchorPoolKindScope ||
					got.DecisionAnchorPoolKindScopeSource != last.DecisionAnchorPoolKindScopeSource ||
					got.DecisionMemberKindConfirmed != last.DecisionMemberKindConfirmed {
					t.Errorf("the folded line (%q/%q/%q) disagrees with the anchor_pool summary it folded (%q/%q/%q)",
						got.DecisionAnchorPoolKindScope, got.DecisionAnchorPoolKindScopeSource, got.DecisionMemberKindConfirmed,
						last.DecisionAnchorPoolKindScope, last.DecisionAnchorPoolKindScopeSource, last.DecisionMemberKindConfirmed)
				}
			}
		})
	}
}

// THE I11 GUARD ON THE FALLBACK. confirmedAnchor.Kind has been through none
// of ScopeAnchorRetrievalKind's checks, so an anchor selection whose kind
// EQUALS the member kind must be refused rather than admitted. Admitting it
// would widen the pool by nothing while claiming a scope, and would let this
// fix report that it enforced I11 on exactly the input that violates it.
func TestAConfirmedAnchorWhoseKindIsTheMemberKindIsRefused(t *testing.T) {
	t.Parallel()
	scope := decideAnchorPoolKindScope(scopedProjectsFrame("chaos"), "",
		&contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:github:chaos"}, confirmedProject())
	if scope.Kind != "" || scope.Source != anchorPoolKindScopeNone {
		t.Fatalf("decideAnchorPoolKindScope admitted %q from source %q for an anchor whose kind EQUALS the member kind -- I11 says the resolved anchor's kind is never the member kind", scope.Kind, scope.Source)
	}
}

// THE PRECEDENCE, pinned by value. The receipt wins when both exist, and
// asserting it needs the two to DISAGREE -- with equal kinds either order
// renders the same line and the ordering would be unpinned.
func TestTheReceiptAnchorKindWinsOverTheConfirmedAnchor(t *testing.T) {
	t.Parallel()
	scope := decideAnchorPoolKindScope(scopedProjectsFrame("chaos"), contextfabric.SubjectTeam,
		&contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectRepository, CanonicalID: "repository.v2:github:chaos"}, confirmedProject())
	if scope.Kind != contextfabric.SubjectTeam || scope.Source != anchorPoolKindScopeReceipt {
		t.Fatalf("scope = %q from %q, want the RECEIPT's team -- a caller can only confirm an option this engine already offered, so the model's reading of the whole question is the wider statement", scope.Kind, scope.Source)
	}
}

// THE PATH THAT NEVER REACHES THE FILTER, and the reason the `none` tokens
// need their own pin rather than riding on the arms above.
//
// A local mutation that made the flush emit the raw stored value instead of
// the `none` token SURVIVED every other pin in this file. That is not a
// hypothetical: the exact-canonical-hint short-circuit is a real production
// path that commits and returns BEFORE the confirmed-kind filter runs, so no
// anchor_pool event is ever emitted and the three fields are still at their
// zero value when the fold flushes. Under that mutation the line carries
// three empty strings, which a JSON sink renders indistinguishably from a
// build that does not emit the keys at all -- the exact "a key that can lie"
// failure the rest of this observable exists to prevent.
//
// Asserting no anchor_pool event was emitted is half the pin: without it a
// future change that started emitting one here would make the `none`
// assertion pass for a completely different reason.
func TestAResolutionThatNeverBuiltAPoolStillCarriesExplicitNoneTokens(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, "*"),
	}}
	req := testRequest()
	req.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "workbench"}}

	capture := &anchorScopeCapture{}
	deps := backend.deps()
	deps.ResolutionTracer = capture
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted(), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(res.Committed) != 1 {
		t.Fatalf("committed %d subjects, want 1 -- this pin is only meaningful on the short-circuit path that actually returns early", len(res.Committed))
	}
	// The scope decision is now made ABOVE this short-circuit, so this path
	// DOES emit exactly one anchor_pool event. Asserted rather than ignored:
	// the fix for the fallback defect moved the decision earlier, and a
	// later change moving it back would silently restore the hole this pin
	// and its sibling were written for.
	if len(capture.anchorPool) != 1 {
		t.Fatalf("the short-circuit path emitted %d anchor_pool events, want exactly 1 -- the scope must be decided above every early return", len(capture.anchorPool))
	}
	if len(capture.summaries) != 1 {
		t.Fatalf("captured %d decision_summary events, want exactly 1", len(capture.summaries))
	}
	got := capture.summaries[0]
	for key, value := range map[string]string{
		"anchor_pool_kind_scope":        got.DecisionAnchorPoolKindScope,
		"anchor_pool_kind_scope_source": got.DecisionAnchorPoolKindScopeSource,
		"member_kind_confirmed":         got.DecisionMemberKindConfirmed,
	} {
		if value != anchorPoolKindScopeNone {
			t.Errorf("%s = %q on a resolution that never built a pool, want the explicit %q -- an empty value here is indistinguishable from a build that stopped emitting the key", key, value, anchorPoolKindScopeNone)
		}
	}
}

// THE THREE DEFECTS AN ADVERSARIAL ROUND FOUND, PINNED BEFORE THEY WERE FIXED.
//
// All three share one root cause: the anchor scope was decided AFTER retrieval
// and handed to the filter alone, while the two consumers that decide what is
// RETRIEVED and what SURVIVES TRUNCATION kept reading the receipt-only value.
// A kind the filter would admit is worthless if nothing ever searched for it.

// anchorOnlyByKindBackend is the fixture the original pins should have used.
// The anchor is reachable ONLY through SearchKind -- the plain search arm
// returns members and a crowd, never the anchor.
//
// This distinction is the whole defect. The first version of these tests let
// the anchor arrive through the ordinary lexical arm, so the filter admitted
// something retrieval had already found and the pins passed while the
// kind-hinted search was never even asked for the anchor's kind.
func anchorOnlyByKindBackend(term string, crowd int) *fakeGraphBackend {
	// The crowd is the CONFIRMED MEMBER KIND, deliberately. A crowd of some
	// third kind is stripped by the confirmed-kind filter before phase-4
	// ever runs, so it never competes for the budget and a truncation pin
	// built on one passes whether or not the anchor holds a reserved slot --
	// which is exactly how the first version of this fixture let a
	// reservation regression survive.
	members := make([]CandidateNode, 0, crowd+1)
	members = append(members, candidateNode(contextfabric.SubjectProject,
		"project.v2:github:"+term+"-one", term+" one", 0.9, "*"))
	for i := 0; i < crowd; i++ {
		members = append(members, candidateNode(contextfabric.SubjectProject,
			fmt.Sprintf("project.v2:github:%s-%d", term, i),
			fmt.Sprintf("%s project %d", term, i), 0.88, "*"))
	}
	return &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{term: members},
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			term: {
				contextfabric.SubjectTeam:    {anchorTeamNode(term, "CHAOS Team")},
				contextfabric.SubjectProject: nil,
			},
		},
	}
}

// P1-1. With NO scope-anchor kind on the receipt, the caller's confirmed
// anchor is the only statement of the anchor's kind -- and it must reach
// KIND-HINTED RETRIEVAL, not merely the filter. Red before the fix: the pool
// comes back holding only projects, because SearchKind was never called for
// `team` and the filter had nothing of that kind to admit.
func TestTheFallbackAnchorKindReachesKindHintedRetrieval(t *testing.T) {
	t.Parallel()
	res := resolveScoped(t, anchorOnlyByKindBackend("chaos", 1), scopedProjectsFrame("chaos"),
		confirmedProject(),
		&contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:chaos"},
		"")
	if got := candidateKinds(res)[contextfabric.SubjectTeam]; got == 0 {
		t.Fatalf("team candidates = 0, want >= 1; kinds=%v. The fallback anchor kind never reached kind-hinted retrieval, so nothing of that kind was ever searched for -- admitting it at the filter cannot rescue a candidate that was never retrieved.", candidateKinds(res))
	}
}

// P1-2, PINNED AT THE LEVEL THIS CHANGE ACTUALLY CONTROLS: the fallback
// anchor kind must be IN the set phase 4 reserves slots for. Before the fix
// the reservation was computed from the receipt-only value, so a fallback
// anchor held no slot at all.
//
// WHAT THIS PIN DELIBERATELY DOES NOT CLAIM. It does not claim the anchor
// always survives truncation, because it does not -- and that limit is older
// and wider than this change. Measured on this tree with a member-kind crowd
// larger than the budget, the anchor is truncated away identically for the
// RECEIPT source, for the FALLBACK source, and with NO confirmed kind at all
// (`kinds=map[project:20]` in all three). The cause is in phase 4's own
// victim rule: a slot can only be taken from a candidate whose kind is NOT
// itself reserved, and on a scope-anchored frame the MEMBER kind is reserved
// too, so a pool saturated with members offers no eligible victim. Writing a
// pin that asserted survival would therefore be asserting a property the
// engine does not have, and fixing it means changing a truncation rule shared
// with every other frame -- reported separately rather than smuggled in here.
func TestTheFallbackAnchorKindIsReservedAgainstTruncation(t *testing.T) {
	t.Parallel()
	frame := scopedProjectsFrame("chaos")
	scope := decideAnchorPoolKindScope(frame, "",
		&contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:chaos"}, confirmedProject())
	reserved := frameReservedKinds(frame, scope.Kind)
	var sawTeam, sawProject bool
	for _, k := range reserved {
		sawTeam = sawTeam || k == contextfabric.SubjectTeam
		sawProject = sawProject || k == contextfabric.SubjectProject
	}
	if !sawTeam {
		t.Errorf("reserved kinds = %v, want the FALLBACK anchor kind (team) among them -- computed from the receipt-only value it is absent, and the anchor holds no slot at all", reserved)
	}
	if !sawProject {
		t.Errorf("reserved kinds = %v, want the member kind (project) still reserved -- this change must not take the members' slot away", reserved)
	}
	// The receipt source must reserve the same kind, so the two sources are
	// not silently different at truncation.
	if got := frameReservedKinds(frame, decideAnchorPoolKindScope(frame, contextfabric.SubjectTeam, nil, confirmedProject()).Kind); len(got) != len(reserved) {
		t.Errorf("receipt source reserved %v but fallback source reserved %v -- the two sources must be indistinguishable downstream", got, reserved)
	}
}

// P1-3. `member_kind_confirmed` is an INPUT to the call, not something the
// call discovers, so it must be on the line even when the resolution returns
// before any pool is built. Red before the fix: the exact-canonical-hint
// short-circuit returns above the anchor_pool event, and the fold -- which
// learned the value only from that event -- printed `none` while a kind was
// confirmed. A key that reports "no kind was confirmed" on a turn that
// confirmed one is worse than an absent key: it is a confident wrong answer.
func TestTheExactHintSummaryStillNamesTheConfirmedMemberKind(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, "*"),
	}}
	req := testRequest()
	req.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "workbench"}}
	capture := &anchorScopeCapture{}
	deps := backend.deps()
	deps.ResolutionTracer = capture
	if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted(), deps,
		confirmedProject(), nil, nil, ""); err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(capture.summaries) != 1 {
		t.Fatalf("captured %d decision_summary events, want exactly 1", len(capture.summaries))
	}
	if got := capture.summaries[0].DecisionMemberKindConfirmed; got != "project" {
		t.Errorf("member_kind_confirmed = %q, want \"project\" -- the kind WAS confirmed on this turn; reporting \"none\" is a confident wrong answer, not a missing one", got)
	}
}

// THE FOLD MUST NOT LEARN THE CONFIRMED MEMBER KIND FROM AN EVENT.
//
// Both the folded line and the anchor_pool line read the SAME call parameter.
// If the fold instead adopted whatever the event carried, it would inherit
// that event's reachability -- the defect that printed `none` on turns which
// had confirmed a kind -- and a divergence between the two would be
// unobservable. Feeding a deliberately WRONG member kind on the event is the
// only way to tell the two designs apart: a fold that reads the parameter
// ignores it, a fold that reads the event adopts it.
func TestTheFoldKeepsTheConfirmedKindItWasBuiltWith(t *testing.T) {
	t.Parallel()
	capture := &decisionSummaryCapture{}
	buffer := &decisionSummaryBuffer{
		real: capture, requestID: "request_member_kind",
		memberKindConfirmed: "project",
	}
	buffer.Trace(ResolutionTraceEvent{
		RequestID: "request_member_kind", Stage: "anchor_pool", AnchorPoolSummary: true,
		DecisionAnchorPoolKindScope: "team", DecisionAnchorPoolKindScopeSource: "receipt",
		DecisionMemberKindConfirmed: "repository",
	})
	buffer.flush()
	if len(capture.summaries) != 1 {
		t.Fatalf("captured %d decision_summary events, want exactly 1", len(capture.summaries))
	}
	got := capture.summaries[0]
	if got.DecisionMemberKindConfirmed != "project" {
		t.Errorf("member_kind_confirmed = %q, want \"project\" -- the fold adopted the EVENT's value instead of the confirmed kind it was constructed with", got.DecisionMemberKindConfirmed)
	}
	// The scope and its source DO come from the event, and must still.
	if got.DecisionAnchorPoolKindScope != "team" || got.DecisionAnchorPoolKindScopeSource != "receipt" {
		t.Errorf("scope/source = %q/%q, want team/receipt -- those are decided inside the call and folded from the event",
			got.DecisionAnchorPoolKindScope, got.DecisionAnchorPoolKindScopeSource)
	}
}

// THE COMMIT GATES NOW SEE A MIXED-KIND POOL, AND THAT IS THE ONE THING THIS
// CHANGE COULD BREAK WITHOUT ANY TEST NOTICING.
//
// Admitting the anchor's kind means the gates contest member candidates and
// an anchor candidate together. This repo has already learned once, the
// expensive way, that removing a candidate removes what the others were
// competing against -- and the converse is just as true: ADDING one can push
// a pool across a floor it should not cross, or rescue a lone candidate that
// should have stayed uncommitted.
//
// The adversarial round noted the union was untested and declined to count it
// as patch-caused, since member candidates already merged this way. That is a
// fair reading, and it is still worth a pin: "it was already like that" is
// precisely the reasoning that let the earlier vector-only defect stand.
func TestAdmittingTheAnchorDoesNotChangeWhatTheGatesDecide(t *testing.T) {
	t.Parallel()
	// Two rival members, neither individually decisive: the pair must stay
	// ambiguous whether or not the anchor joins them.
	twoMembers := func() []CandidateNode {
		return []CandidateNode{
			candidateNode(contextfabric.SubjectProject, "project.v2:github:chaos-alpha", "chaos alpha", 0.55, "*"),
			candidateNode(contextfabric.SubjectProject, "project.v2:github:chaos-beta", "chaos beta", 0.54, "*"),
		}
	}
	withoutAnchor := &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{"chaos": twoMembers()},
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"chaos": {contextfabric.SubjectTeam: nil, contextfabric.SubjectProject: nil},
		},
	}
	withAnchor := &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{"chaos": twoMembers()},
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"chaos": {
				contextfabric.SubjectTeam:    {anchorTeamNode("chaos", "CHAOS Team")},
				contextfabric.SubjectProject: nil,
			},
		},
	}

	bare := resolveScoped(t, withoutAnchor, scopedProjectsFrame("chaos"), confirmedProject(), nil, "")
	mixed := resolveScoped(t, withAnchor, scopedProjectsFrame("chaos"), confirmedProject(), nil, contextfabric.SubjectTeam)

	// The anchor must actually be in the mixed pool, or this proves nothing.
	if candidateKinds(mixed)[contextfabric.SubjectTeam] == 0 {
		t.Fatalf("the anchor never joined the pool, so this comparison is vacuous; kinds=%v", candidateKinds(mixed))
	}
	// THE MEMBERS' OWN CONTEST IS UNCHANGED. Compare the committed MEMBER
	// subjects, not the whole set -- the anchor is legitimately allowed to
	// commit on its own basis, and that is a different question.
	memberCommits := func(res contextfabric.SubjectResolution) []string {
		out := []string{}
		for _, s := range res.Committed {
			if s.Kind == contextfabric.SubjectProject {
				out = append(out, s.CanonicalID)
			}
		}
		return out
	}
	before, after := memberCommits(bare), memberCommits(mixed)
	if len(before) != len(after) {
		t.Errorf("member commits changed when the anchor joined the pool: %v -> %v. Admitting the anchor must not alter what the members' own contest decides.", before, after)
	}
	for i := range before {
		if i < len(after) && before[i] != after[i] {
			t.Errorf("member commit %d changed: %q -> %q", i, before[i], after[i])
		}
	}
}

// THE NO-REGRESSION PIN. This is the adversarial round's own probe, kept as a
// pin rather than a fix: with NO confirmed member kind, this change must do
// NOTHING. The filter is a no-op on such a turn, so there is no stripped
// anchor to rescue, and widening retrieval there would only add a candidate
// that was never removed -- which is exactly what turned a project committing
// on the lone-candidate floor into a top-two ambiguity asking the caller to
// choose between a member and the scope containing it.
//
// The whole gate result is asserted, not just the committed set: the earlier
// version of this comparison checked committed project ids alone and could
// not see a commit become an ambiguity.
func TestWithNoConfirmedKindTheAnchorChangesNothing(t *testing.T) {
	t.Parallel()
	member := func() []CandidateNode {
		return []CandidateNode{candidateNode(contextfabric.SubjectProject,
			"project.v2:github:chaos-borderline", "chaos borderline", 0.8, "*")}
	}
	backend := &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{"chaos": member()},
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"chaos": {
				contextfabric.SubjectProject: nil,
				contextfabric.SubjectTeam:    {anchorTeamNode("chaos", "CHAOS Team")},
			},
		},
	}
	res := resolveScoped(t, backend, scopedProjectsFrame("chaos"), nil,
		&contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:chaos"},
		"")

	var committedMember bool
	for _, subject := range res.Committed {
		committedMember = committedMember || subject.Kind == contextfabric.SubjectProject
	}
	if !committedMember {
		t.Errorf("the member did not commit; committed=%v. With no confirmed kind this path must behave exactly as it did before this change.", res.Committed)
	}
	if res.ClarificationPrompt != "" {
		t.Errorf("clarification prompt = %q, want empty. Asking which of a member and its own scope the caller meant is a question invariant I11 guarantees has no answer.", res.ClarificationPrompt)
	}
	for _, c := range res.Candidates {
		if c.Subject.Kind == contextfabric.SubjectProject && c.State == contextfabric.ResolutionAmbiguous {
			t.Errorf("member candidate state = ambiguous; it had no rival of its own kind and no confirmed kind was in play")
		}
		if c.Subject.Kind == contextfabric.SubjectTeam {
			t.Errorf("a %s candidate reached the pool with NO confirmed kind; kinds=%v. This change must not widen retrieval on a turn where the filter removed nothing.", c.Subject.Kind, candidateKinds(res))
		}
	}
}

// THE TICKET CASE. With a confirmed member kind the filter WOULD strip the
// anchor, and all three consumers must therefore see the anchor kind: it has
// to be retrieved (kind-hinted search), reserved (phase 4) and admitted (the
// filter). This is the turn the ticket is about -- a caller that answered both
// structure-need offers truthfully and got no_match.
func TestWithAConfirmedKindTheAnchorIsRetrievedReservedAndAdmitted(t *testing.T) {
	t.Parallel()
	capture := &anchorScopeCapture{}
	res := resolveCapturingAnchorScope(t, capture, anchorOnlyByKindBackend("chaos", 3),
		scopedProjectsFrame("chaos"), confirmedProject(),
		&contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:chaos"},
		"")
	// RETRIEVED and ADMITTED: the fixture makes the anchor reachable only
	// through kind-scoped search, so its presence proves both.
	if got := candidateKinds(res)[contextfabric.SubjectTeam]; got == 0 {
		t.Fatalf("team candidates = 0, want >= 1; kinds=%v. The anchor was neither retrieved under its own kind nor admitted past the confirmed-kind filter.", candidateKinds(res))
	}
	// RESERVED: asserted through the observable, which reports the set phase
	// 4 was actually handed.
	if len(capture.summaries) != 1 {
		t.Fatalf("captured %d decision_summary events, want exactly 1", len(capture.summaries))
	}
	var reservedTeam bool
	for _, k := range capture.summaries[0].DecisionReservedKinds {
		reservedTeam = reservedTeam || k == "team"
	}
	if !reservedTeam {
		t.Errorf("reserved_kinds = %v, want the anchor kind among them", capture.summaries[0].DecisionReservedKinds)
	}
}

// THE NEGATIVE CONTROL FOR THE FALLBACK FIXTURE.
//
// The FIRST version of the fallback pin went green for a reason that had
// nothing to do with the fix: its backend also returned the anchor from the
// ordinary lexical `searchResults` arm, so the filter admitted a candidate
// retrieval had already found by another route, and the kind-hinted search
// was never asked for the anchor's kind at all. The pin therefore passed
// while the production path it names was dead.
//
// This control makes that failure mode impossible to repeat silently. It
// asserts, on the fixture the fallback pins actually use, that the anchor is
// unreachable WITHOUT kind-scoped search: with SearchKind disabled the pool
// contains no candidate of the anchor's kind at all. If someone later adds
// the anchor to `searchResults`, this goes red and the fallback pins stop
// being able to pass for a fixture reason.
func TestTheFallbackFixtureCannotLeakTheAnchorThroughPlainSearch(t *testing.T) {
	t.Parallel()
	// The fixture's own plain arm must contain nothing of the anchor kind.
	backend := anchorOnlyByKindBackend("chaos", 3)
	for _, node := range backend.searchResults["chaos"] {
		subject, ok := NodeSubject(node)
		if ok && subject.Kind == contextfabric.SubjectTeam {
			t.Fatalf("the fixture's plain searchResults arm contains a %s node (%q) -- the fallback pins could pass without kind-scoped retrieval ever running, which is exactly how this defect hid the first time",
				subject.Kind, subject.CanonicalID)
		}
	}
	// And end to end: with kind-scoped search UNAVAILABLE, the anchor cannot
	// appear by any other route.
	noKindSearch := anchorOnlyByKindBackend("chaos", 3)
	noKindSearch.enableSearchKind = false
	noKindSearch.searchKindResults = nil
	res := resolveScoped(t, noKindSearch, scopedProjectsFrame("chaos"), confirmedProject(),
		&contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:chaos"},
		"")
	if got := candidateKinds(res)[contextfabric.SubjectTeam]; got != 0 {
		t.Fatalf("team candidates = %d with SearchKind disabled, want 0; kinds=%v. The anchor reached the pool by some route other than kind-scoped retrieval, so the fallback pins prove nothing about the path they name.", got, candidateKinds(res))
	}
}

// THE ERROR PATH THAT FLUSHES WITHOUT AN anchor_pool EVENT.
//
// A hosted battery arm caught this and it is a genuine consequence of the
// fix, not a stale needle: moving the scope decision above retrieval means
// every SUCCESSFUL path now emits an anchor_pool event, so the fold's
// `none`-token fallback stopped being reachable through any of them -- and a
// mutation removing that fallback survived the whole suite.
//
// It is still reachable, on the paths that return an ERROR before the
// decision is made: the fold is installed by the caller and flushed by a
// defer, so it emits its summary even when the resolution failed at the very
// first check. Without the fallback those lines carry empty strings, which a
// JSON sink renders indistinguishably from a build that emits no keys.
//
// This is the third time on this seam that a value has been proven only on
// the paths a fixture happened to reach. The lesson is written here rather
// than in a commit message: a key that is "always set" must be pinned on a
// path that sets it by fallback, not only on paths that set it directly.
func TestAFailedResolutionStillCarriesExplicitNoneTokens(t *testing.T) {
	t.Parallel()
	capture := &anchorScopeCapture{}
	deps := (&fakeGraphBackend{}).deps()
	deps.ResolutionTracer = capture
	// An empty OrgID fails at the first check in resolveSubjects, which is
	// ABOVE the anchor-scope decision -- so no anchor_pool event is emitted
	// and the fold has nothing to learn the tokens from.
	_, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: ""}, testRequest(), testInterpreted("chaos"),
		deps, confirmedProject(), nil, scopedProjectsFrame("chaos"), contextfabric.SubjectTeam)
	if err == nil {
		t.Fatal("expected an error from an empty OrgID; this pin only means anything on a path that fails before the scope is decided")
	}
	if len(capture.anchorPool) != 0 {
		t.Fatalf("the failing path emitted %d anchor_pool events, want 0 -- it must return BEFORE the decision for this pin to cover the fallback", len(capture.anchorPool))
	}
	if len(capture.summaries) != 1 {
		t.Fatalf("captured %d decision_summary events, want exactly 1 -- the fold is deferred, so an error path still flushes", len(capture.summaries))
	}
	got := capture.summaries[0]
	for key, value := range map[string]string{
		"anchor_pool_kind_scope":        got.DecisionAnchorPoolKindScope,
		"anchor_pool_kind_scope_source": got.DecisionAnchorPoolKindScopeSource,
	} {
		if value != anchorPoolKindScopeNone {
			t.Errorf("%s = %q on a resolution that failed before deciding a scope, want the explicit %q", key, value, anchorPoolKindScopeNone)
		}
	}
	// member_kind_confirmed is stamped at construction, so it survives even
	// this path with its real value -- asserted so the two mechanisms stay
	// visibly different.
	if got.DecisionMemberKindConfirmed != "project" {
		t.Errorf("member_kind_confirmed = %q, want \"project\" -- it is stamped from the parameter at construction and must survive a path that never reached the decision", got.DecisionMemberKindConfirmed)
	}
}

// THE WIRING ON THE LINE. `anchor_pool_kind_scope` reading `team` proves the
// scope was DECIDED; it does not prove the three consumers were HANDED it. A
// regression reverting one of them to the receipt-only value leaves the scope
// and source reading correctly while retrieval, the reserve or the filter
// acts on a different set -- which an adversarial round named as invisible at
// Info. These two keys are what makes it visible.
func TestTheDecisionSummaryNamesTheWiringItHandedTheConsumers(t *testing.T) {
	t.Parallel()
	has := func(list []string, want string) bool {
		for _, v := range list {
			if v == want {
				return true
			}
		}
		return false
	}
	capture := &anchorScopeCapture{}
	resolveCapturingAnchorScope(t, capture, anchorOnlyByKindBackend("chaos", 2),
		scopedProjectsFrame("chaos"), confirmedProject(), nil, contextfabric.SubjectTeam)
	if len(capture.summaries) != 1 {
		t.Fatalf("captured %d decision_summary events, want exactly 1", len(capture.summaries))
	}
	got := capture.summaries[0]
	if !has(got.DecisionReservedKinds, "team") {
		t.Errorf("reserved_kinds = %v, want the anchor kind among them -- a reserve computed from the receipt-only value would omit it on a fallback turn", got.DecisionReservedKinds)
	}
	if !has(got.DecisionFilterKinds, "project") || !has(got.DecisionFilterKinds, "team") {
		t.Errorf("filter_kinds = %v, want both the confirmed member kind and the anchor kind -- these are exactly what the confirmed-kind filter admits", got.DecisionFilterKinds)
	}
	// NEVER NULL, and on a turn this change deliberately leaves alone. With
	// no confirmed kind nothing is filtered and nothing is widened, so the
	// filter set is EMPTY -- which must still be an empty list, because
	// "the filter admitted nothing" and "there was no filter" are different
	// statements and a null cannot tell them apart.
	bare := &anchorScopeCapture{}
	resolveCapturingAnchorScope(t, bare, anchorOnlyByKindBackend("chaos", 2),
		scopedProjectsFrame("chaos"), nil, nil, contextfabric.SubjectTeam)
	if bare.summaries[0].DecisionFilterKinds == nil {
		t.Error("filter_kinds is nil with no confirmed kind, want an empty list")
	}
	if len(bare.summaries[0].DecisionFilterKinds) != 0 {
		t.Errorf("filter_kinds = %v with no confirmed kind, want empty -- nothing was filtered on that turn", bare.summaries[0].DecisionFilterKinds)
	}
}
