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
	"slices"
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
		"request_census_pin_000", key, false, false, nil, subjectCommitPolicy{})
	if len(admitted.Committed) != 1 {
		t.Fatalf("the control arm committed %d subject(s), want 1; without a committing control this test "+
			"cannot tell a refusal from a fixture that never reached the rescue", len(admitted.Committed))
	}

	refused, _, _ := resolveFromMergedCandidatesWithSubjectScope(pool, map[string]string{}, map[string]bool{}, 10,
		true, true, nil, 0, false, 10, 20, true, DefaultCommitGatePolicy(), nil, nil, false, nil,
		"request_census_pin_001", key, false, false, nil, subjectCommitPolicy{scope: scope})
	if len(refused.Committed) != 0 {
		t.Fatalf("evidence_census committed %v of the declared MEMBER kind; the funnel guard cannot reach this "+
			"path, so it needs its own conjunct and no longer has one", refused.Committed)
	}
}

// THE ESCAPE THE COUNTED r1 FOUND, and it is the one path the three guards
// cannot see. The caller-hint short circuit is entered when ANY hint is
// caller-sourced -- but FinalizeExactResolutionWithBasis then commits EVERY
// retained hint in candidatesBySubject, caller-sourced or receipt-derived
// alike, and returns before the merged-candidate resolution (and therefore
// before all three guards) ever runs.
//
// So the boundary the positive control above draws is real for a hint list of
// ONE, and false for a MIXED list: one caller-explicit hint of any kind is
// enough to carry an engine-minted prior_subject_receipt of the withheld
// member kind out through the same exit, committed. That is exactly the
// laundering this ticket exists to stop -- the engine crediting the caller
// with an identifier the engine itself minted -- only reached one turn
// earlier and by a different door.
//
// The caller-explicit half must still commit: refusing it would break "name
// the subject you mean", which is the whole reason that exemption exists.
func TestAMixedHintListDoesNotCarryAReceiptOfTheWithheldKindThroughTheShortCircuit(t *testing.T) {
	t.Parallel()
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository.v2:github:platform", Label: "platform"}
	member := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:platform-owners", Label: "platform owners"}
	backend := mentionScopeBackend("platform")
	backend.exactHints = map[string]CandidateNode{
		SubjectKey(anchor): candidateNode(anchor.Kind, anchor.CanonicalID, anchor.Label, 1, "*"),
		SubjectKey(member): candidateNode(member.Kind, member.CanonicalID, member.Label, 1, "*"),
	}
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20
	req.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		// caller-explicit: authoritative, must keep committing
		{Kind: anchor.Kind, ID: anchor.CanonicalID, Label: anchor.Label, Source: "workbench"},
		// engine-minted: must NOT commit, on this path as on every other
		{Kind: member.Kind, ID: member.CanonicalID, Label: member.Label, Source: "prior_subject_receipt"},
	}
	res, _, bases, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("platform"),
		backend.deps(), confirmedTeam(), nil, mentionScopeFrame("platform"), "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	for _, subject := range res.Committed {
		if subject.Kind == contextfabric.SubjectTeam {
			t.Fatalf("committed %v -- the withheld member kind rode out through the caller-hint short "+
				"circuit on a mixed hint list, which is the same laundering the three guards refuse "+
				"one turn later; bases=%v", res.Committed, bases)
		}
	}
	if len(res.Committed) != 1 || res.Committed[0].CanonicalID != anchor.CanonicalID {
		t.Fatalf("committed %v, want exactly the caller's own explicitly named anchor -- refusing it "+
			"would break naming a subject by canonical id, which is not a substitution", res.Committed)
	}
	// DEMOTED, NOT DROPPED. The withheld subject must still reach the caller
	// as a Proposed candidate: this exit returns immediately, so if it were
	// dropped here the turn would collapse to a bare answer with no account of
	// the subject it refused, and the caller would have nothing to clarify
	// against.
	proposed := 0
	for _, candidate := range res.Candidates {
		if candidate.Subject.Kind != contextfabric.SubjectTeam {
			continue
		}
		if candidate.State == contextfabric.ResolutionCommitted {
			t.Fatalf("candidate %q is still Committed after the short circuit", candidate.Subject.CanonicalID)
		}
		proposed++
	}
	if proposed != 1 {
		t.Fatalf("the withheld subject appears %d times among the returned candidates, want exactly 1 -- "+
			"withholding demotes it, it does not delete the caller's only account of what was refused", proposed)
	}
}

// mixedHintCapture keeps the folded line, the per-candidate offer_pool
// dispositions, AND the per-subject decision events, because the short
// circuit's provenance token lives on the decision events and the count it
// feeds lives on the folded line -- checking one against the other is the
// whole point.
type mixedHintCapture struct {
	summaries    []ResolutionTraceEvent
	dispositions []ResolutionTraceEvent
	decisions    []ResolutionTraceEvent
}

func (c *mixedHintCapture) Trace(event ResolutionTraceEvent) {
	switch {
	case event.Stage == "decision_summary":
		c.summaries = append(c.summaries, event)
	case event.Stage == "offer_pool" && event.OfferPoolDisposition != "":
		c.dispositions = append(c.dispositions, event)
	case event.Stage == "decision":
		c.decisions = append(c.decisions, event)
	}
}

// THE OBSERVABLE FOR THE SHORT-CIRCUIT EXIT, and it needs BOTH arms.
//
// This exit returns before any offer-pool event is emitted on the ordinary
// path, so before this fix a withholding that happened HERE moved no number on
// the very line that reports withholding, and a committed subject's PROVENANCE
// -- caller-named or engine-minted -- appeared nowhere at Info at all. Those
// are the two regressions r1 named as invisible.
//
// The withheld arm alone would pass with the counter hardcoded to 1 and the
// provenance count hardcoded to 0; the passthrough arm alone would pass with
// both hardcoded the other way. Together neither literal survives.
func TestTheShortCircuitReportsWhatItWithheldAndWhoseIdentifierItCommitted(t *testing.T) {
	t.Parallel()
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository.v2:github:platform", Label: "platform"}
	member := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:platform-owners", Label: "platform owners"}
	for _, testCase := range []struct {
		name              string
		frame             *contextfabric.QuestionFrame
		wantWithheld      int
		wantEngineMinted  int
		wantCommittedKind []contextfabric.SubjectKind
	}{
		{
			// The scope-anchored frame: the engine-minted receipt of the
			// member kind is refused the commit and SAID SO on the line.
			name:  "the receipt of the withheld kind is refused and counted",
			frame: mentionScopeFrame("platform"), wantWithheld: 1, wantEngineMinted: 0,
			wantCommittedKind: []contextfabric.SubjectKind{contextfabric.SubjectRepository},
		},
		{
			// NO scope axis, SAME hint list. The receipt commits exactly as
			// it always did -- this fix takes nothing away from a question
			// with no scope to withhold on -- and the provenance counter is
			// what makes that commit visible rather than silent. This is the
			// non-zero that proves the counter is not stuck at 0.
			name:  "with no scope axis the engine-minted receipt still commits, and is counted as such",
			frame: nil, wantWithheld: 0, wantEngineMinted: 1,
			wantCommittedKind: []contextfabric.SubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectTeam},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			backend := mentionScopeBackend("platform")
			backend.exactHints = map[string]CandidateNode{
				SubjectKey(anchor): candidateNode(anchor.Kind, anchor.CanonicalID, anchor.Label, 1, "*"),
				SubjectKey(member): candidateNode(member.Kind, member.CanonicalID, member.Label, 1, "*"),
			}
			capture := &mixedHintCapture{}
			req := testRequest()
			req.Options.MaxSubjectCandidates = 20
			req.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
				{Kind: anchor.Kind, ID: anchor.CanonicalID, Label: anchor.Label, Source: "workbench"},
				{Kind: member.Kind, ID: member.CanonicalID, Label: member.Label, Source: "prior_subject_receipt"},
			}
			deps := backend.deps()
			deps.ResolutionTracer = capture
			res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
				storage.Principal{OrgID: "org_1"}, req, testInterpreted("platform"),
				deps, confirmedTeam(), nil, testCase.frame, "")
			if err != nil {
				t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
			}

			gotKinds := make([]contextfabric.SubjectKind, 0, len(res.Committed))
			for _, subject := range res.Committed {
				gotKinds = append(gotKinds, subject.Kind)
			}
			slices.Sort(gotKinds)
			want := slices.Clone(testCase.wantCommittedKind)
			slices.Sort(want)
			if !slices.Equal(gotKinds, want) {
				t.Fatalf("committed kinds = %v, want %v", gotKinds, want)
			}

			if len(capture.summaries) != 1 {
				t.Fatalf("captured %d decision_summary events, want exactly 1", len(capture.summaries))
			}
			got := capture.summaries[0]
			if got.OfferPoolAnchorKindWithheld != testCase.wantWithheld {
				t.Errorf("offer_pool_anchor_kind_withheld on the FOLDED line = %d, want %d -- the short "+
					"circuit contributed nothing to this counter before r1",
					got.OfferPoolAnchorKindWithheld, testCase.wantWithheld)
			}
			if got.DecisionCommittedEngineMinted != testCase.wantEngineMinted {
				t.Errorf("decision_committed_engine_minted = %d, want %d -- without it the line carries "+
					"committed ids and a SET of bases but never says whose identifier each id was",
					got.DecisionCommittedEngineMinted, testCase.wantEngineMinted)
			}

			// THE IDENTITY, both ways: the folded counters must equal the
			// events they folded, never a second tally computed beside them.
			withheld := 0
			for _, event := range capture.dispositions {
				if event.OfferPoolDisposition == offerPoolAnchorKindWithheldDisposition {
					withheld++
				}
			}
			if withheld != got.OfferPoolAnchorKindWithheld {
				t.Errorf("the folded line reports %d withheld but %d per-candidate dispositions were emitted",
					got.OfferPoolAnchorKindWithheld, withheld)
			}
			minted := 0
			for _, event := range capture.decisions {
				if event.Outcome != "committed" {
					continue
				}
				if event.CommitSubjectProvenance == "" {
					t.Errorf("committed decision event for %q carries no commit_subject_provenance -- this "+
						"exit is the one place that provenance is known", event.Subject.CanonicalID)
				}
				if event.CommitSubjectProvenance == contextfabric.CommitSubjectProvenanceEngineMinted {
					minted++
				}
			}
			if minted != got.DecisionCommittedEngineMinted {
				t.Errorf("the folded line reports %d engine-minted commits but %d decision events carried "+
					"the token", got.DecisionCommittedEngineMinted, minted)
			}
		})
	}
}

// ============================================================================
// The three r2 repros, as permanent pins, plus the enumeration they rest on.
// ============================================================================

// r2 FINDING 2. The withheld candidates must come back INSIDE the caller's own
// bound. The first version appended them after FinalizeExactResolution had
// truncated, so the returned set exceeded Options.MaxSubjectCandidates —
// measured as max=1 candidates=2. The bound is the caller's contract and is not
// ours to overrun for a candidate we are refusing anyway.
func TestTheShortCircuitRespectsMaxSubjectCandidatesWhileWithholding(t *testing.T) {
	t.Parallel()
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository.v2:github:platform", Label: "platform"}
	member := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:platform-owners", Label: "platform owners"}
	backend := mentionScopeBackend("platform")
	backend.exactHints = map[string]CandidateNode{
		SubjectKey(anchor): candidateNode(anchor.Kind, anchor.CanonicalID, anchor.Label, 1, "*"),
		SubjectKey(member): candidateNode(member.Kind, member.CanonicalID, member.Label, 1, "*"),
	}
	req := testRequest()
	req.Options.MaxSubjectCandidates = 1
	req.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		{Kind: anchor.Kind, ID: anchor.CanonicalID, Label: anchor.Label, Source: "workbench"},
		{Kind: member.Kind, ID: member.CanonicalID, Label: member.Label, Source: contextfabric.SubjectHintSourcePriorSubjectReceipt},
	}
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("platform"),
		backend.deps(), confirmedTeam(), nil, mentionScopeFrame("platform"), "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	if len(res.Candidates) > req.Options.MaxSubjectCandidates {
		t.Fatalf("returned %d candidates for MaxSubjectCandidates=%d -- a candidate this resolution is "+
			"REFUSING must not be the one that overruns the caller's own bound",
			len(res.Candidates), req.Options.MaxSubjectCandidates)
	}
	// The committable candidate keeps the slot, which is the half of the
	// original reasoning that was right.
	if len(res.Committed) != 1 || res.Committed[0].CanonicalID != anchor.CanonicalID {
		t.Fatalf("committed %v, want the caller's own named anchor to keep the one available slot", res.Committed)
	}
}

// r2 FINDING 3. The ORDINARY arrival path commits engine-minted receipts, and
// before the choke point it stamped no provenance at all — so
// decision_committed_engine_minted read a confident ZERO on a turn where an
// engine-minted receipt had committed. A measured zero that is false is worse
// than an absent field, because nothing distinguishes it from a real one.
//
// The receipt here is of the ANCHOR kind, so nothing withholds it: it commits
// through pre_committed_exact_hint exactly as it always has. Only the reporting
// changes.
func TestAnArrivalCommitCarriesItsProvenanceAndIsCounted(t *testing.T) {
	t.Parallel()
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository.v2:github:platform", Label: "platform"}
	backend := mentionScopeBackend("platform")
	backend.exactHints = map[string]CandidateNode{
		SubjectKey(anchor): candidateNode(anchor.Kind, anchor.CanonicalID, anchor.Label, 1, "*"),
	}
	capture := &mixedHintCapture{}
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20
	req.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		{Kind: anchor.Kind, ID: anchor.CanonicalID, Label: anchor.Label, Source: contextfabric.SubjectHintSourcePriorSubjectReceipt},
	}
	deps := backend.deps()
	deps.ResolutionTracer = capture
	res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("platform"),
		deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	// No frame and no confirmed kind on purpose: this pin is about the
	// ORDINARY arrival commit, the one every question can reach, so nothing
	// about the scope axis is allowed to be load-bearing for it. (A
	// scope-anchored frame with a confirmed member kind would narrow the
	// subject pool and drop this arrival before it ever committed -- which is
	// this ticket's own mechanism, and a different test.)
	if len(res.Committed) != 1 || res.Committed[0].CanonicalID != anchor.CanonicalID {
		t.Fatalf("committed %v, want the anchor-kind receipt to commit exactly as before -- this pin is about "+
			"REPORTING, and a pin that changed the behaviour would be measuring the wrong thing", res.Committed)
	}
	committed := 0
	for _, event := range capture.decisions {
		if event.Outcome != "committed" {
			continue
		}
		committed++
		if event.CommitSubjectProvenance != contextfabric.CommitSubjectProvenanceEngineMinted {
			t.Errorf("commit gate %q stamped commit_subject_provenance=%q, want %q -- this receipt was minted "+
				"by the engine, and a path that does not say so makes the folded count read a false zero",
				event.CommitGate, event.CommitSubjectProvenance, contextfabric.CommitSubjectProvenanceEngineMinted)
		}
	}
	if committed != 1 {
		t.Fatalf("captured %d committed decision events, want 1", committed)
	}
	if len(capture.summaries) != 1 {
		t.Fatalf("captured %d decision_summary events, want 1", len(capture.summaries))
	}
	if got := capture.summaries[0].DecisionCommittedEngineMinted; got != 1 {
		t.Fatalf("decision_committed_engine_minted = %d on a turn that committed an engine-minted receipt, "+
			"want 1 -- reading 0 here is the false zero this pin exists for", got)
	}
}

// r2 FINDING 4. An engine-produced hint never takes the caller's exemption, and
// is never reported as caller-named. The answer-reuse authorization recheck
// produces exactly such a hint; the old single-string test treated every source
// but one as the caller's own.
//
// Driven with the WITHHELD kind, because the exemption is what is under test:
// if this source is misclassified it both escapes the withholding and lies on
// the line, which is why one pin can hold both halves.
func TestAnAnswerReuseRecheckHintIsEngineMintedNotCallerNamed(t *testing.T) {
	t.Parallel()
	member := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team.v2:github:platform-owners", Label: "platform owners"}
	backend := mentionScopeBackend("platform")
	backend.exactHints = map[string]CandidateNode{
		SubjectKey(member): candidateNode(member.Kind, member.CanonicalID, member.Label, 1, "*"),
	}
	req := testRequest()
	req.Options.MaxSubjectCandidates = 20
	req.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		{Kind: member.Kind, ID: member.CanonicalID, Label: member.Label, Source: contextfabric.SubjectHintSourceAnswerReuseRecheck},
	}
	res, _, bases, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("platform"),
		backend.deps(), confirmedTeam(), nil, mentionScopeFrame("platform"), "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	for _, subject := range res.Committed {
		if subject.Kind == contextfabric.SubjectTeam {
			t.Fatalf("committed %v -- an answer-reuse recheck hint took the CALLER'S exemption from the "+
				"withholding; the caller never named this subject in this request; bases=%v", res.Committed, bases)
		}
	}
}

// THE ENUMERATION ITSELF, and the reason it is a pin rather than a comment: a
// new internal hint producer that forgets to classify itself would otherwise
// acquire the caller's exemption silently, which is exactly how the answer-reuse
// source acquired it.
func TestTheEngineMintedHintSourceSetIsClosedAndTotal(t *testing.T) {
	t.Parallel()
	want := []string{
		contextfabric.SubjectHintSourceAnswerReuseRecheck,
		contextfabric.SubjectHintSourcePriorSubjectReceipt,
	}
	got := contextfabric.EngineMintedSubjectHintSources()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("engine-minted hint sources = %v, want %v -- a source added to the engine without being "+
			"classified here silently gains the caller's exemption", got, want)
	}
	// The returned slice is a copy: a consumer must not be able to widen the
	// enumeration it is asking about.
	got[0] = "mutated"
	if again := contextfabric.EngineMintedSubjectHintSources(); slices.Contains(again, "mutated") {
		t.Fatalf("EngineMintedSubjectHintSources() handed out its own backing array")
	}
	// One assertion per enumerated source, so a deletion cannot hide inside a
	// set comparison someone later loosens.
	for _, source := range want {
		if !contextfabric.SubjectHintIsEngineMinted(source) {
			t.Errorf("SubjectHintIsEngineMinted(%q) = false, want true", source)
		}
		if got := contextfabric.ClassifySubjectHintProvenance(source); got != contextfabric.CommitSubjectProvenanceEngineMinted {
			t.Errorf("ClassifySubjectHintProvenance(%q) = %q, want %q", source, got, contextfabric.CommitSubjectProvenanceEngineMinted)
		}
	}
	// The DEFAULT side, which is what makes the enumeration meaningful: a wire
	// request's own sources are not enumerable and are the caller's words.
	for _, source := range []string{"workbench", "cli", "", "prior_subject_receipt_lookalike"} {
		if contextfabric.SubjectHintIsEngineMinted(source) {
			t.Errorf("SubjectHintIsEngineMinted(%q) = true, want false", source)
		}
		if got := contextfabric.ClassifySubjectHintProvenance(source); got != contextfabric.CommitSubjectProvenanceCallerNamed {
			t.Errorf("ClassifySubjectHintProvenance(%q) = %q, want %q", source, got, contextfabric.CommitSubjectProvenanceCallerNamed)
		}
	}
}

// THE POLICY'S OWN TOTALITY. provenanceFor must never return an empty string,
// and `resolved` and `unknown` must stay distinct -- collapsing them is how a
// missing measurement became a measured value in the first place.
func TestTheCommitPolicyProvenanceIsTotalAndDistinguishesUnknownFromResolved(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository.v2:github:platform"}
	if got := (subjectCommitPolicy{}).provenanceFor(subject); got != contextfabric.CommitSubjectProvenanceUnknown {
		t.Errorf("a policy with no hint set reports %q, want %q -- 'I was not told' is not 'retrieval found it'",
			got, contextfabric.CommitSubjectProvenanceUnknown)
	}
	known := subjectCommitPolicy{hintProvenance: map[string]string{}}
	if got := known.provenanceFor(subject); got != contextfabric.CommitSubjectProvenanceResolved {
		t.Errorf("a policy that knows its (empty) hint set reports %q, want %q", got, contextfabric.CommitSubjectProvenanceResolved)
	}
	named := subjectCommitPolicy{hintProvenance: map[string]string{
		SubjectKey(subject): contextfabric.CommitSubjectProvenanceEngineMinted,
	}}
	if got := named.provenanceFor(subject); got != contextfabric.CommitSubjectProvenanceEngineMinted {
		t.Errorf("a hinted subject reports %q, want %q", got, contextfabric.CommitSubjectProvenanceEngineMinted)
	}
}
