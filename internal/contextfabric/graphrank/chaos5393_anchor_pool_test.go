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
		&contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:github:chaos"})
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
		&contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectRepository, CanonicalID: "repository.v2:github:chaos"})
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
	if len(capture.anchorPool) != 0 {
		t.Fatalf("the short-circuit path emitted %d anchor_pool events; this pin exists to cover the case where NONE is emitted, so it now proves nothing", len(capture.anchorPool))
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
