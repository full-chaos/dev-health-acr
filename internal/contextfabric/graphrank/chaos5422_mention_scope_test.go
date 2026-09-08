package graphrank

// A MENTION IS NOT A SCOPE.
//
// Corpus row neg-mentions-teams-but-not-grouped, measured on the acr main
// yardstick (0ad85ef4, rep1): a question whose ANCHOR is one kind and whose
// MEMBERS are another ended with a member-kind subject committed as the
// answer's subject. The chain was three turns and no single turn looks wrong
// on its own:
//
//	t1  the engine offers the expected_kind axis; the caller redeems the
//	    MEMBER kind, truthfully -- that is the kind it asked about
//	t2  the confirmed member kind narrows the SUBJECT pool to itself, the
//	    anchor the terms actually named is dropped, and the one survivor -- a
//	    weak lexical hit of the member's kind -- is OFFERED with a receipt id
//	t3  the caller answers with that receipt, the arrival lands already
//	    Committed, and pre_committed_exact_hint records caller_canonical_id
//
// So the substituted subject enters through OFFER -> RECEIPT ->
// caller-canonical-id, and it is NOT the CHAOS-5385 shape: the offered
// candidate matched lexically, so the vector-only exclusion had nothing to
// withhold (the measured t2 line reads vector_only_excluded 0,
// vector_only_demoted 0, emptied_by_exclusion false).
//
// The invariant it violates is already written down. Phase-B invariant I11 --
// "the RESOLVED anchor's kind != MemberKind" -- says a children_of_scope
// resolution can never commit a member-kind subject, so every candidate that
// survives a member-kind-only filter on such a frame is an I11 violation by
// construction. I11 is declared (frame_invariants.go) and nothing enforces it.
// These pins enforce it where CHAOS-5385 established the standard has to
// hold: at the OFFER, because an offer is a commit deferred by one turn.
//
// THE SIGNAL IS THE GROUPING/SCOPE AXIS, never the question's text. Every
// arm below reads SubjectExpression.Kind and the declared MemberKind, both
// closed-vocabulary fields the model filled in deliberately and the server
// validated -- there is no substring match over prose anywhere in this seam.

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// mentionScopeFrame is the row's own shape: the members are TEAMS, the anchor
// is named by term, and -- per ScopedSetExpression -- the anchor's own kind is
// not on the frame at all. That absence is I11's whole point and it is why a
// member-kind filter on this frame can only ever return the wrong subject.
func mentionScopeFrame(term string) *contextfabric.QuestionFrame {
	return &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionChildrenOfScope,
			Scoped: &contextfabric.ScopedSetExpression{
				AnchorTerms: []string{term},
				MemberKind:  contextfabric.SubjectTeam,
			},
		},
	}
}

func confirmedTeam() *contextfabric.ConfirmedExpectedKind {
	return &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectTeam}
}

// mentionScopeBackend is the live retrieval shape: the term names a
// REPOSITORY, and a team of the org shares enough of the term to be reached
// lexically. Retrieval finds both; the confirmed member kind drops the
// repository; the team is what is left to offer.
func mentionScopeBackend(term string) *fakeGraphBackend {
	repo := candidateNode(contextfabric.SubjectRepository,
		"repository.v2:github:"+term, term, 0.95, "*")
	team := candidateNode(contextfabric.SubjectTeam,
		"team.v2:github:"+term+"-owners", term+" owners", 0.4, "*")
	return &fakeGraphBackend{
		searchResults:    map[string][]CandidateNode{term: {repo, team}},
		enableSearchKind: true,
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			term: {
				contextfabric.SubjectTeam:       {team},
				contextfabric.SubjectRepository: {repo},
			},
		},
	}
}

func resolveMentionScope(t *testing.T, backend *fakeGraphBackend, frame *contextfabric.QuestionFrame,
	confirmedKind *contextfabric.ConfirmedExpectedKind, tracer ResolutionTracer) contextfabric.SubjectResolution {
	t.Helper()
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20
	deps := backend.deps()
	if tracer != nil {
		deps.ResolutionTracer = tracer
	}
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("platform"),
		deps, confirmedKind, nil, frame, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	return res
}

// THE HARM, at the production entry point. This is the row's t2: the offer is
// the last point at which the substitution can still be refused, because the
// receipt it mints is what the next turn commits on.
func TestAMemberKindCandidateIsNeverOfferedAsTheScopeAnchor(t *testing.T) {
	t.Parallel()
	res := resolveMentionScope(t, mentionScopeBackend("platform"), mentionScopeFrame("platform"),
		confirmedTeam(), nil)
	if got := candidateKinds(res)[contextfabric.SubjectTeam]; got != 0 {
		t.Fatalf("offered %d candidate(s) of the declared MEMBER kind as the resolution's subject; kinds=%v. "+
			"On a children_of_scope frame the subject is the ANCHOR, whose kind I11 says is never the member's -- "+
			"offering a member is what let the next turn commit it on caller_canonical_id.", got, candidateKinds(res))
	}
}

// AND IT NEVER COMMITS ONE EITHER. The measured row offered a weak lexical hit
// and needed a turn to launder it, but the same pool with an exact match
// commits inside ONE turn and never mints a receipt at all. A fix that only
// withheld the offer would leave that shape substituting silently, so the
// claim "never commits the mentioned-but-not-scoped subject" is asserted on
// the path that can reach it directly.
func TestAMemberKindCandidateNeverCommitsAsTheScopeAnchor(t *testing.T) {
	t.Parallel()
	backend := mentionScopeBackend("platform")
	// The lone survivor of the member-kind filter, at exact strength: this
	// clears the ordinary lone-candidate floor with nothing to compete
	// against it.
	exact := candidateNode(contextfabric.SubjectTeam, "team.v2:github:platform", "platform", 1, "*")
	backend.searchResults["platform"] = []CandidateNode{exact}
	backend.searchKindResults["platform"][contextfabric.SubjectTeam] = []CandidateNode{exact}
	res := resolveMentionScope(t, backend, mentionScopeFrame("platform"), confirmedTeam(), nil)
	for _, committed := range res.Committed {
		if committed.Kind == contextfabric.SubjectTeam {
			t.Fatalf("committed %q, a subject of the declared MEMBER kind, as the answer's subject on a "+
				"children_of_scope frame -- invariant I11 says the resolved anchor's kind is never the "+
				"member's, so this commit answers a question the caller did not ask", committed.CanonicalID)
		}
	}
}

// THE TURN STILL ENDS WITH A QUESTION, not with a collapse to no_match. This
// is CHAOS-5385's own measured lesson applied to a second cause: withholding
// every offer and returning an empty pool ended a conversation one turn before
// the engine would have offered the real candidates. The prompt must also be
// TRUE -- the shipped emptied-pool constant says the subjects were matched by
// semantic similarity alone, which is false for a lexical member-kind hit, so
// a build that reused it would tell the caller something that did not happen.
func TestAWithheldMemberKindPoolClarifiesWithItsOwnBasis(t *testing.T) {
	t.Parallel()
	res := resolveMentionScope(t, mentionScopeBackend("platform"), mentionScopeFrame("platform"),
		confirmedTeam(), nil)
	if len(res.Candidates) != 0 {
		t.Fatalf("offered %d candidate(s); this arm's fixture leaves nothing offerable after the member "+
			"kind is withheld, so a non-empty pool means the withholding did not happen", len(res.Candidates))
	}
	if res.ClarificationPrompt == "" {
		t.Fatal("no clarification prompt beside an emptied offer pool: the turn collapses to no_match and " +
			"the conversation ends, which is the cost CHAOS-5385 measured and refused")
	}
	if res.ClarificationPrompt == contextfabric.OfferPoolEmptiedClarificationPrompt {
		t.Fatalf("prompt = the VECTOR-ONLY emptied-pool constant, which states these subjects matched by " +
			"semantic similarity alone; the withheld candidate here matched lexically, so this prompt is false")
	}
	if res.ClarificationPrompt != contextfabric.OfferPoolAnchorKindWithheldClarificationPrompt {
		t.Fatalf("prompt = %q, want the anchor-kind-withheld constant", res.ClarificationPrompt)
	}
	// It names nothing it withheld. The constant interpolates nothing, so
	// this is structural rather than incidental -- the same discipline the
	// vector-only constant already carries.
	if strings.Contains(res.ClarificationPrompt, "platform") {
		t.Fatalf("the prompt names a subject the caller cannot see in the result: %q", res.ClarificationPrompt)
	}
}

// NEGATIVE CONTROL 1 -- a frame with NO scope/grouping axis is untouched.
// Without this arm a "fix" that simply refused every candidate of a confirmed
// kind would satisfy every pin above while breaking the ordinary named-subject
// question, where the confirmed kind IS the subject's own kind.
func TestANamedSubjectFrameStillOffersItsOwnKind(t *testing.T) {
	t.Parallel()
	namedKind := contextfabric.SubjectTeam
	named := &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:  contextfabric.SubjectExpressionNamed,
			Named: &contextfabric.NamedSubjectExpression{Terms: []string{"platform"}, ExpectedKind: &namedKind},
		},
	}
	res := resolveMentionScope(t, mentionScopeBackend("platform"), named, confirmedTeam(), nil)
	if got := candidateKinds(res)[contextfabric.SubjectTeam]; got == 0 {
		t.Fatalf("team candidates = 0 on a named_subject frame whose OWN expected kind is team; kinds=%v. "+
			"A named subject is not a member of anything -- it IS the subject -- so nothing here may be withheld.",
			candidateKinds(res))
	}
}

// NEGATIVE CONTROL 2 -- a discovered_kind frame is untouched, and it fails
// ALONE. Its members ARE the subjects it commits, so the member kind is the
// only admissible kind; a withholding keyed on "the frame declared this kind"
// rather than on the SCOPE AXIS would empty this pool completely.
func TestADiscoveredKindFrameStillOffersItsMembers(t *testing.T) {
	t.Parallel()
	discovered := &contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalAssessState},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind:       contextfabric.SubjectExpressionDiscoveredKind,
			Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectTeam},
		},
	}
	res := resolveMentionScope(t, mentionScopeBackend("platform"), discovered, confirmedTeam(), nil)
	if got := candidateKinds(res)[contextfabric.SubjectTeam]; got == 0 {
		t.Fatalf("team candidates = 0 on a discovered_kind frame whose member kind IS what it commits; kinds=%v",
			candidateKinds(res))
	}
}

// NEGATIVE CONTROL 3 -- a scope-anchored frame whose ANCHOR kind is known
// keeps its anchor. CHAOS-5393 admits that kind through the confirmed-kind
// filter; this withholding must not take it back out, and the two decisions
// must not collide.
func TestAKnownScopeAnchorKindIsStillOffered(t *testing.T) {
	t.Parallel()
	backend := mentionScopeBackend("platform")
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("platform"),
		backend.deps(), confirmedTeam(), nil, mentionScopeFrame("platform"), contextfabric.SubjectRepository)
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if got := candidateKinds(res)[contextfabric.SubjectRepository]; got == 0 {
		t.Fatalf("repository candidates = 0 with a receipt-declared scope anchor kind of repository; kinds=%v. "+
			"The anchor is exactly what this frame commits, and withholding the MEMBER kind must never reach it.",
			candidateKinds(res))
	}
	if got := candidateKinds(res)[contextfabric.SubjectTeam]; got != 0 {
		t.Fatalf("team candidates = %d beside a known anchor kind; kinds=%v. The member kind stays withheld "+
			"whether or not the anchor's kind is known -- I11 does not depend on that.", got, candidateKinds(res))
	}
}

// mentionScopeCapture keeps the folded decision_summary line AND the
// per-candidate offer_pool dispositions it folded, so the reported counts can
// be checked against their own source rather than against a second literal
// this test also wrote.
type mentionScopeCapture struct {
	summaries    []ResolutionTraceEvent
	dispositions []ResolutionTraceEvent
}

func (c *mentionScopeCapture) Trace(event ResolutionTraceEvent) {
	switch {
	case event.Stage == "decision_summary":
		c.summaries = append(c.summaries, event)
	case event.Stage == "offer_pool" && event.OfferPoolDisposition != "":
		c.dispositions = append(c.dispositions, event)
	}
}

// THE OBSERVABLE, BY VALUE AND WITH EXPLICIT ZEROS.
//
// Both arms are required and they fail for different reasons. Without the
// withheld arm the count could be hardcoded to zero and nothing would notice;
// without the untouched arm the count could be hardcoded to the pool size and
// every ordinary resolution would report a withholding that never happened.
func TestTheDecisionSummaryNamesWhyTheOfferWasWithheld(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name          string
		frame         *contextfabric.QuestionFrame
		confirmedKind *contextfabric.ConfirmedExpectedKind
		wantWithheld  int
		wantScope     string
		wantReason    string
	}{
		{
			name:  "the member kind is withheld from the offer",
			frame: mentionScopeFrame("platform"), confirmedKind: confirmedTeam(),
			wantWithheld: 1, wantScope: "team", wantReason: "frame_member_kind",
		},
		{
			// EXPLICIT ZEROS. This is the ordinary line, and it is what
			// gives the arm above its meaning: if the keys appeared only
			// when something was withheld, their absence on a normal line
			// could not be told from a build that stopped emitting them.
			name:  "no scope axis, nothing withheld",
			frame: nil, confirmedKind: confirmedTeam(),
			wantWithheld: 0, wantScope: "none", wantReason: "none",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			capture := &mentionScopeCapture{}
			resolveMentionScope(t, mentionScopeBackend("platform"), testCase.frame, testCase.confirmedKind, capture)

			if len(capture.summaries) != 1 {
				t.Fatalf("captured %d decision_summary events, want exactly 1", len(capture.summaries))
			}
			got := capture.summaries[0]
			if got.OfferPoolAnchorKindWithheld != testCase.wantWithheld {
				t.Errorf("offer_pool_anchor_kind_withheld on the FOLDED line = %d, want %d",
					got.OfferPoolAnchorKindWithheld, testCase.wantWithheld)
			}
			if got.OfferPoolAnchorKindWithheldScope != testCase.wantScope {
				t.Errorf("offer_pool_anchor_kind_withheld_scope = %q, want %q -- the count alone cannot tell an "+
					"operator WHICH kind was refused the offer", got.OfferPoolAnchorKindWithheldScope, testCase.wantScope)
			}
			if got.OfferPoolAnchorKindWithheldReason != testCase.wantReason {
				t.Errorf("offer_pool_anchor_kind_withheld_reason = %q, want %q",
					got.OfferPoolAnchorKindWithheldReason, testCase.wantReason)
			}

			// THE IDENTITY: the folded count must equal the number of
			// per-candidate dispositions it folded, never a second tally
			// computed beside it. This is the only assertion that goes red
			// when the counter drifts from the decision it reports while
			// still agreeing with a literal above.
			withheld := 0
			for _, event := range capture.dispositions {
				if event.OfferPoolDisposition == offerPoolAnchorKindWithheldDisposition {
					withheld++
				}
			}
			if withheld != got.OfferPoolAnchorKindWithheld {
				t.Errorf("the folded line reports %d withheld but %d per-candidate disposition events were "+
					"emitted -- the count is not the decision it claims to summarise",
					got.OfferPoolAnchorKindWithheld, withheld)
			}
		})
	}
}

// THE DECISION ITSELF, unit-level and total over BOTH axes it reads.
//
// The offer-pool arms above each drive one frame variant with one confirmed
// kind; this quantifies over the union, so neither a variant added beside
// children_of_scope nor a confirmed-kind state can silently acquire — or lose
// — a withholding nobody decided on.
func TestTheSubjectOfferScopeWithholdsOnlyAScopeAnchoredConfirmedMemberKind(t *testing.T) {
	t.Parallel()
	team := contextfabric.SubjectTeam
	namedFrame := &contextfabric.QuestionFrame{SubjectExpression: contextfabric.SubjectExpression{
		Kind:  contextfabric.SubjectExpressionNamed,
		Named: &contextfabric.NamedSubjectExpression{Terms: []string{"platform"}, ExpectedKind: &team},
	}}
	discoveredFrame := &contextfabric.QuestionFrame{SubjectExpression: contextfabric.SubjectExpression{
		Kind:       contextfabric.SubjectExpressionDiscoveredKind,
		Discovered: &contextfabric.DiscoveredSetExpression{MemberKind: contextfabric.SubjectTeam},
	}}
	groupedFrame := &contextfabric.QuestionFrame{SubjectExpression: contextfabric.SubjectExpression{
		Kind: contextfabric.SubjectExpressionGroupedMembers,
		Grouped: &contextfabric.GroupedSetExpression{
			GroupKind: contextfabric.SubjectTeam, MemberKind: contextfabric.SubjectProject},
	}}
	kindlessScopedFrame := &contextfabric.QuestionFrame{SubjectExpression: contextfabric.SubjectExpression{
		Kind:   contextfabric.SubjectExpressionChildrenOfScope,
		Scoped: &contextfabric.ScopedSetExpression{AnchorTerms: []string{"platform"}},
	}}
	for _, testCase := range []struct {
		name          string
		frame         *contextfabric.QuestionFrame
		confirmedKind *contextfabric.ConfirmedExpectedKind
		anchorScope   anchorPoolKindScope
		wantKind      contextfabric.SubjectKind
		wantSource    string
	}{
		{
			name:  "children_of_scope withholds its CONFIRMED member kind",
			frame: mentionScopeFrame("platform"), confirmedKind: confirmedTeam(),
			wantKind: contextfabric.SubjectTeam, wantSource: subjectOfferScopeFrameMemberKind,
		},
		{
			// THE NARROWING, and it fails alone. With no confirmed kind the
			// confirmed-kind filter never ran, so the anchor is still in the
			// pool and the member's commit is a real contest CHAOS-5393
			// already pinned as accepted behaviour
			// (TestWithNoConfirmedKindTheAnchorChangesNothing). Withholding
			// here would take a served answer away to fix a defect this turn
			// does not have.
			name:  "the same frame with NO confirmed kind decides nothing",
			frame: mentionScopeFrame("platform"), confirmedKind: nil,
			wantSource: subjectOfferScopeNone,
		},
		{
			// A confirmed kind that is not the member kind narrowed the pool
			// to something else entirely, so its survivors are not member-kind
			// candidates and there is nothing here to refuse.
			name:          "a confirmed kind other than the member kind decides nothing",
			frame:         mentionScopeFrame("platform"),
			confirmedKind: &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectRepository},
			wantSource:    subjectOfferScopeNone,
		},
		{
			// THE DEFENSIVE ARM. Two decisions must never make opposite
			// claims about one kind -- admitting it to the pool while
			// refusing it the offer.
			name:  "an anchor scope admitting that same kind decides nothing",
			frame: mentionScopeFrame("platform"), confirmedKind: confirmedTeam(),
			anchorScope: anchorPoolKindScope{Kind: contextfabric.SubjectTeam, Source: anchorPoolKindScopeReceipt},
			wantSource:  subjectOfferScopeNone,
		},
		{
			name:  "a nil frame decides nothing",
			frame: nil, confirmedKind: confirmedTeam(), wantSource: subjectOfferScopeNone,
		},
		{
			name:  "named_subject withholds nothing",
			frame: namedFrame, confirmedKind: confirmedTeam(), wantSource: subjectOfferScopeNone,
		},
		{
			name:  "discovered_kind withholds nothing",
			frame: discoveredFrame, confirmedKind: confirmedTeam(), wantSource: subjectOfferScopeNone,
		},
		{
			name:          "grouped_members withholds nothing",
			frame:         groupedFrame,
			confirmedKind: &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectProject},
			wantSource:    subjectOfferScopeNone,
		},
		{
			name:  "a scope-anchored frame with no declared member kind decides nothing",
			frame: kindlessScopedFrame, confirmedKind: confirmedTeam(), wantSource: subjectOfferScopeNone,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			scope := decideSubjectOfferScope(testCase.frame, testCase.confirmedKind, testCase.anchorScope)
			if scope.MemberKind != testCase.wantKind {
				t.Errorf("MemberKind = %q, want %q", scope.MemberKind, testCase.wantKind)
			}
			if scope.Source != testCase.wantSource {
				t.Errorf("Source = %q, want %q", scope.Source, testCase.wantSource)
			}
			if scope.withholds(contextfabric.SubjectRepository) {
				t.Errorf("withholds(repository) = true; only the DECLARED member kind is ever withheld")
			}
			if got := scope.withholds(contextfabric.SubjectTeam); got != (testCase.wantKind == contextfabric.SubjectTeam) {
				t.Errorf("withholds(team) = %v, want %v", got, testCase.wantKind == contextfabric.SubjectTeam)
			}
		})
	}
}

// THE EMPTY KIND IS NEVER WITHHELD. A candidate whose kind failed to resolve
// carries the zero value, and a scope that also carried the zero value would
// withhold every one of them -- a whole class of candidates disappearing from
// every offer through a comparison nobody wrote deliberately.
func TestTheZeroSubjectOfferScopeWithholdsNothing(t *testing.T) {
	t.Parallel()
	var scope subjectOfferScope
	if scope.withholds("") {
		t.Fatal("the zero scope withholds the zero kind: an unresolved candidate would vanish from every offer")
	}
	kind, reason := scope.observable()
	if kind != subjectOfferScopeNone || reason != subjectOfferScopeNone {
		t.Fatalf("observable() = (%q, %q), want both %q -- a line with no decision must never render empty values",
			kind, reason, subjectOfferScopeNone)
	}
}

// THE RECEIPT ARRIVAL, which is the row's own t3 and the only path that
// reaches pre_committed_exact_hint here.
//
// A receipt-derived hint carries Source "prior_subject_receipt", which
// deliberately does NOT short-circuit: it is the engine's guess at what a
// conversational reference bound to, not a caller naming a subject. It
// arrives already Committed, and without the demotion it commits on
// caller_canonical_id with identity_proven set -- the engine crediting the
// caller with an identifier the engine itself had offered one turn earlier.
func TestAReceiptArrivalOfTheMemberKindIsDemotedNotCommitted(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:platform-owners", Label: "platform owners"}
	backend := mentionScopeBackend("platform")
	backend.exactHints = map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 1, "*"),
	}
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20
	req.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "prior_subject_receipt"},
	}
	res, _, bases, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("platform"),
		backend.deps(), confirmedTeam(), nil, mentionScopeFrame("platform"), "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(res.Committed) != 0 {
		t.Fatalf("committed %v through the pre-committed arrival path; this is the row's own t3, and the "+
			"basis it records is caller_canonical_id for a subject the ENGINE offered", res.Committed)
	}
	if len(bases) != 0 {
		t.Fatalf("recorded %d commit basis/bases for a resolution that must commit nothing", len(bases))
	}
	for _, candidate := range res.Candidates {
		if candidate.State == contextfabric.ResolutionCommitted {
			t.Fatalf("candidate %q is still Committed after demotion", candidate.Subject.CanonicalID)
		}
	}
}

// THE BOUNDARY, and it is a POSITIVE control rather than a second harm. A
// CALLER-EXPLICIT hint (any Source but prior_subject_receipt) is the caller
// naming a subject by canonical id, which is an authoritative direct ask --
// not an offer this engine minted and then read back as consent. It
// short-circuits above this seam and must keep committing, or "name the
// subject you mean" would stop being an answer the caller can give.
func TestACallerExplicitHintOfTheMemberKindStillCommits(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:platform-owners", Label: "platform owners"}
	backend := mentionScopeBackend("platform")
	backend.exactHints = map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 1, "*"),
	}
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20
	req.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "workbench"},
	}
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("platform"),
		backend.deps(), confirmedTeam(), nil, mentionScopeFrame("platform"), "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(res.Committed) != 1 || res.Committed[0].CanonicalID != subject.CanonicalID {
		t.Fatalf("committed %v, want the caller's own explicitly named subject; a caller who names a subject "+
			"by canonical id has not been offered anything, so nothing here is a substitution", res.Committed)
	}
}

// THE CENSUS RESCUE, the one commit path that does not select out of
// commitIndex: it looks its candidate up by attested key, so the funnel guard
// cannot reach it and it carries its own conjunct. Driven through the
// implementation directly because reaching evidence_census end to end needs a
// truncated search, a census backend and a stalled resolution -- none of which
// this assertion is about.
func TestTheEvidenceCensusRescueRefusesAWithheldKind(t *testing.T) {
	t.Parallel()
	candidate := contextfabric.SubjectCandidate{
		ReceiptID: "receipt_census_withheld_kind",
		Subject: contextfabric.SubjectRef{
			Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:platform-owners", Label: "platform owners",
		},
		State: contextfabric.ResolutionProposed, MatchReasons: []string{"probe"},
		Confidence: 0.5, MatchMechanisms: []contextfabric.MatchMechanism{contextfabric.MatchLexical},
	}
	key := SubjectKey(candidate.Subject)
	pool := map[string]contextfabric.SubjectCandidate{key: candidate}
	scope := decideSubjectOfferScope(mentionScopeFrame("platform"), confirmedTeam(), anchorPoolKindScope{})

	// WITHOUT the scope the rescue commits: that is what makes the arm below
	// a real refusal rather than a fixture that could never commit anyway.
	admitted, _, _ := resolveFromMergedCandidatesWithSubjectScope(pool, map[string]string{}, map[string]bool{}, 10,
		true, true, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, nil,
		"request_census_pin_000", key, false, false, nil, subjectOfferScope{})
	if len(admitted.Committed) != 1 {
		t.Fatalf("the control arm committed %d subject(s), want 1; without a committing control this test "+
			"cannot tell a refusal from a fixture that never reached the rescue", len(admitted.Committed))
	}

	refused, _, _ := resolveFromMergedCandidatesWithSubjectScope(pool, map[string]string{}, map[string]bool{}, 10,
		true, true, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, nil,
		"request_census_pin_001", key, false, false, nil, scope)
	if len(refused.Committed) != 0 {
		t.Fatalf("evidence_census committed %v of the declared MEMBER kind; the funnel guard cannot reach this "+
			"path, so it needs its own conjunct and no longer has one", refused.Committed)
	}
}
