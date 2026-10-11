package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// countedTeams is the graph context of a follow-up that counts three teams
// under its anchor.
func countedTeams() *GraphContext {
	graphContext := emptyGraphContext()
	graphContext.Cohort = countingCohort(SubjectTeam, 3)
	return &graphContext
}

// TestTheBoundAnchorKindLetsTheCountServeTheCarriedRepository is the
// reviewed seam where stages disagree about an applied anchor: turn one
// proves a repository; the same question again, with the model now stating a
// project as the anchor kind, re-commits the repository. Switched off, the
// ledger applies the repository while the count refuses it and the answer
// carries no count. Switched on, the binding's kind is the reading's, and the
// served document counts the three teams under that repository.
func TestTheBoundAnchorKindLetsTheCountServeTheCarriedRepository(t *testing.T) {
	reCommitted := needTurnResponse{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeAlpha}},
		bases:      provenCommitBases(probeAlpha), context: countedTeams(),
	}
	first := identityProvenResponse(probeAlpha)
	first.context = countedTeams()
	for _, tc := range []struct {
		name      string
		on        bool
		bound     BoundAnchorKindDecision
		decision  string
		counted   bool
		agreement AnchorBindingAgreement
	}{
		{"switched off: the model's kind refuses the carried repository", false, BoundAnchorKindSwitchOff, string(CountPopulationScopeAnchorUnresolved), false, AnchorBindingDisagree},
		{"switched on: one kind for every stage", true, BoundAnchorKindAppliedCarried, string(CountPopulationScopeAnchorCommitted), true, AnchorBindingAgree},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newAnchorProbeRig(t, false)
			rig.engine.anchorBindingCountEnabled = tc.on
			rig.interpreter.read(SubjectRepository, false)
			one := rig.turn(t, needTurnRequest("request_bound_count_one", true), first)
			rig.verify()
			rig.interpreter.read(SubjectProject, false)
			two := rig.turn(t, continuingNeedTurn(needTurnRequest("request_bound_count_two", true), one.result.ResultID), reCommitted)
			if got := servedLedgerAnchor(two); got.ID != probeAlpha.CanonicalID {
				t.Fatalf("premise: the served ledger holds %+v; the carried repository is applied", got)
			}
			line := two.transitions[0]
			if line.Admission != AnchorBindingAdmissionAdmitted || line.BoundAnchorKind != tc.bound || line.ServedCount != tc.decision || line.Agreement != tc.agreement {
				t.Fatalf("line admission/bound/count/agreement = %s/%s/%s/%s, want admitted/%s/%s/%s", line.Admission, line.BoundAnchorKind, line.ServedCount, line.Agreement, tc.bound, tc.decision, tc.agreement)
			}
			claims := 0
			for _, claim := range two.result.ClaimedFacts {
				if claim.Kind != contractsv1.ContextFabricFactCardinality {
					continue
				}
				claims++
				if claim.Subject.CanonicalID != probeAlpha.CanonicalID || claim.Value.Integer == nil || *claim.Value.Integer != 3 {
					t.Fatalf("cardinality claim = %+v, want three teams under %s", claim, probeAlpha.CanonicalID)
				}
			}
			if (claims == 1) != tc.counted || claims > 1 {
				t.Fatalf("served cardinality claims = %d, want counted=%v", claims, tc.counted)
			}
			if two.saved == nil || two.saved.ScopeAnchor.Kind != map[bool]SubjectKind{false: SubjectProject, true: SubjectRepository}[tc.on] {
				t.Fatalf("persisted reading anchor kind = %+v, want the kind every stage read", two.saved.ScopeAnchor)
			}
			if tc.on && two.saved.ScopeAnchor.KindSource != ScopeAnchorKindAnchorBinding {
				t.Fatalf("persisted kind source = %q, want %q", two.saved.ScopeAnchor.KindSource, ScopeAnchorKindAnchorBinding)
			}
		})
	}
}

// TestApplyBoundAnchorKindDecisionTable is the decision's input space: the
// switch, the receipt, the admitted carry, agreement with the model and the
// frame's refusal.
func TestApplyBoundAnchorKindDecisionTable(t *testing.T) {
	frame := countingFrame(SubjectTeam)
	receipt := &confirmedStructureMember{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: SubjectProject, AppliedValue: "project:receipt"}
	admitted := func(kind SubjectKind) anchorBindingParent {
		b := heldBinding(AnchorBindingBound, anchorRef{Kind: kind, ID: string(kind) + ":carried"})
		return anchorBindingParent{Status: AnchorBindingParentPresent, Binding: b}
	}
	for _, tc := range []struct {
		name      string
		on        bool
		receipt   *confirmedStructureMember
		parent    anchorBindingParent
		admission AnchorBindingAdmission
		model     SubjectKind
		frame     *QuestionFrame
		want      BoundAnchorKindDecision
		wantKind  SubjectKind
		wantSrc   ScopeAnchorKindSource
	}{
		{"off", false, receipt, admitted(SubjectRepository), AnchorBindingAdmissionAdmitted, SubjectProject, frame, BoundAnchorKindSwitchOff, SubjectProject, ""},
		{"nothing names a kind", true, nil, anchorBindingParent{Status: AnchorBindingParentNoReference}, "", SubjectProject, frame, BoundAnchorKindNone, SubjectProject, ""},
		{"a refused carry names no kind", true, nil, admitted(SubjectRepository), AnchorBindingAdmissionRefusedExpired, SubjectProject, frame, BoundAnchorKindNone, SubjectProject, ""},
		{"an unevaluated carry names no kind", true, nil, admitted(SubjectRepository), "", SubjectProject, frame, BoundAnchorKindNone, SubjectProject, ""},
		{"admitted carry agrees", true, nil, admitted(SubjectProject), AnchorBindingAdmissionAdmitted, SubjectProject, frame, BoundAnchorKindAgrees, SubjectProject, ""},
		{"admitted carry applied", true, nil, admitted(SubjectRepository), AnchorBindingAdmissionAdmitted, SubjectProject, frame, BoundAnchorKindAppliedCarried, SubjectRepository, ScopeAnchorKindAnchorBinding},
		{"admitted carry over a silent model", true, nil, admitted(SubjectRepository), AnchorBindingAdmissionAdmitted, "", frame, BoundAnchorKindAppliedCarried, SubjectRepository, ScopeAnchorKindAnchorBinding},
		{"the receipt outranks the carry", true, receipt, admitted(SubjectRepository), AnchorBindingAdmissionAdmitted, SubjectRepository, frame, BoundAnchorKindAppliedReceipt, SubjectProject, ScopeAnchorKindAnchorBinding},
		{"the frame refuses the member kind", true, nil, admitted(SubjectTeam), AnchorBindingAdmissionAdmitted, SubjectProject, frame, BoundAnchorKindRejectedByFrame, SubjectProject, ""},
		{"no scoped frame refuses every kind", true, nil, admitted(SubjectRepository), AnchorBindingAdmissionAdmitted, SubjectProject, nil, BoundAnchorKindRejectedByFrame, SubjectProject, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &Engine{anchorBindingCountEnabled: tc.on}
			tracker := &anchorBindingTracker{receipt: tc.receipt, parent: tc.parent, admission: tc.admission}
			outcome := QuestionFamilyOutcome{Frame: tc.frame, WinningSample: FamilySample{ScopeAnchorKind: tc.model}}
			got := engine.applyBoundAnchorKind(tracker, outcome)
			if tracker.boundKindDecision() != tc.want || got.WinningSample.ScopeAnchorKind != tc.wantKind || got.WinningSample.ScopeAnchorKindSource != tc.wantSrc {
				t.Fatalf("decision/kind/source = %s/%s/%q, want %s/%s/%q", tracker.boundKindDecision(), got.WinningSample.ScopeAnchorKind, got.WinningSample.ScopeAnchorKindSource, tc.want, tc.wantKind, tc.wantSrc)
			}
		})
	}
	if got := (&Engine{anchorBindingCountEnabled: true}).applyBoundAnchorKind(nil, QuestionFamilyOutcome{WinningSample: FamilySample{ScopeAnchorKind: SubjectProject}}); got.WinningSample.ScopeAnchorKind != SubjectProject {
		t.Fatal("with the shadow off the reading changed")
	}
}

// TestABoundKindReadsAsTheSameScopeAnchorOnAContinuation: a kind the engine
// applied from the binding does not make a continued reading differ from a
// fresh proposal that states another kind; a committed-hint kind still
// matches only a proposal that states none.
func TestABoundKindReadsAsTheSameScopeAnchorOnAContinuation(t *testing.T) {
	bound := SemanticScopeAnchor{Kind: SubjectRepository, Term: "a", KindSource: ScopeAnchorKindAnchorBinding}
	hint := SemanticScopeAnchor{Kind: SubjectRepository, Term: "a", KindSource: ScopeAnchorKindCommittedHint}
	for name, tc := range map[string]struct {
		carried, fresh SemanticScopeAnchor
		want           bool
	}{
		"bound kind, fresh project":     {bound, SemanticScopeAnchor{Kind: SubjectProject, Term: "a"}, true},
		"bound kind, another term":      {bound, SemanticScopeAnchor{Kind: SubjectRepository, Term: "b"}, false},
		"committed hint, fresh project": {hint, SemanticScopeAnchor{Kind: SubjectProject, Term: "a"}, false},
		"committed hint, fresh silent":  {hint, SemanticScopeAnchor{Term: "a"}, true},
	} {
		if got := sameScopeAnchor(tc.carried, tc.fresh); got != tc.want {
			t.Errorf("%s: sameScopeAnchor = %v, want %v", name, got, tc.want)
		}
	}
	state := BuildSemanticState(SemanticStateInput{
		Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone, WinningSample: FamilySample{ScopeAnchorKind: SubjectRepository, ScopeAnchorKindSource: ScopeAnchorKindAnchorBinding}},
		FamilyVersion:   QuestionFamilyTableVersion,
		RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
	})
	if _, err := EncodeSemanticState(state); err != nil {
		t.Fatalf("a reading carrying the anchor-binding kind source does not persist: %v", err)
	}
}
