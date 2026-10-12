package contextfabric

import (
	"reflect"
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
			// The persisted reading is the model's, whatever the switch: the
			// bound kind is re-decided from each turn's own admission.
			if two.saved == nil || two.saved.ScopeAnchor.Kind != SubjectProject || two.saved.ScopeAnchor.KindSource != "" {
				t.Fatalf("persisted reading anchor = %+v, want the model's own kind %q", two.saved.ScopeAnchor, SubjectProject)
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
	pending := func(kind SubjectKind) anchorBindingParent {
		b := heldBinding(AnchorBindingPendingWindowConfirmation, anchorRef{Kind: kind, ID: string(kind) + ":pending"})
		return anchorBindingParent{Status: AnchorBindingParentPresent, Binding: b}
	}
	for _, tc := range []struct {
		name      string
		on        bool
		pendingOn bool
		receipt   *confirmedStructureMember
		parent    anchorBindingParent
		admission AnchorBindingAdmission
		model     SubjectKind
		frame     *QuestionFrame
		want      BoundAnchorKindDecision
		wantKind  SubjectKind
		wantSrc   ScopeAnchorKindSource
	}{
		{"off", false, false, receipt, admitted(SubjectRepository), AnchorBindingAdmissionAdmitted, SubjectProject, frame, BoundAnchorKindSwitchOff, SubjectProject, ""},
		{"nothing names a kind", true, false, nil, anchorBindingParent{Status: AnchorBindingParentNoReference}, "", SubjectProject, frame, BoundAnchorKindNone, SubjectProject, ""},
		{"a refused carry names no kind", true, false, nil, admitted(SubjectRepository), AnchorBindingAdmissionRefusedExpired, SubjectProject, frame, BoundAnchorKindNone, SubjectProject, ""},
		{"an unevaluated carry names no kind", true, false, nil, admitted(SubjectRepository), "", SubjectProject, frame, BoundAnchorKindNone, SubjectProject, ""},
		{"admitted carry agrees", true, false, nil, admitted(SubjectProject), AnchorBindingAdmissionAdmitted, SubjectProject, frame, BoundAnchorKindAgrees, SubjectProject, ""},
		{"admitted carry applied", true, false, nil, admitted(SubjectRepository), AnchorBindingAdmissionAdmitted, SubjectProject, frame, BoundAnchorKindAppliedCarried, SubjectRepository, ScopeAnchorKindAnchorBinding},
		{"admitted carry over a silent model", true, false, nil, admitted(SubjectRepository), AnchorBindingAdmissionAdmitted, "", frame, BoundAnchorKindAppliedCarried, SubjectRepository, ScopeAnchorKindAnchorBinding},
		{"the receipt outranks the carry", true, false, receipt, admitted(SubjectRepository), AnchorBindingAdmissionAdmitted, SubjectRepository, frame, BoundAnchorKindAppliedReceipt, SubjectProject, ScopeAnchorKindAnchorBinding},
		{"the frame refuses the member kind", true, false, nil, admitted(SubjectTeam), AnchorBindingAdmissionAdmitted, SubjectProject, frame, BoundAnchorKindRejectedByFrame, SubjectProject, ""},
		{"no scoped frame refuses every kind", true, false, nil, admitted(SubjectRepository), AnchorBindingAdmissionAdmitted, SubjectProject, nil, BoundAnchorKindRejectedByFrame, SubjectProject, ""},
		{"a pending carry names no kind without the pending carry", true, false, nil, pending(SubjectRepository), AnchorBindingAdmissionAdmitted, SubjectProject, frame, BoundAnchorKindNone, SubjectProject, ""},
		{"a pending carry names its kind with the pending carry", true, true, nil, pending(SubjectRepository), AnchorBindingAdmissionAdmitted, SubjectProject, frame, BoundAnchorKindAppliedCarried, SubjectRepository, ScopeAnchorKindAnchorBinding},
		{"a receipt still names its kind beside a held pending binding", true, false, receipt, pending(SubjectRepository), AnchorBindingAdmissionAdmitted, SubjectRepository, frame, BoundAnchorKindAppliedReceipt, SubjectProject, ScopeAnchorKindAnchorBinding},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &Engine{anchorBindingCountEnabled: tc.on, anchorBindingPendingCarryEnabled: tc.pendingOn}
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

// TestThePersistedReadingKeepsTheModelsKind: a reading whose kind was applied
// from the binding persists the kind the reading stated before it, so no
// later turn can carry a bound kind its own admission did not make; and a
// committed-hint kind persists as before.
func TestThePersistedReadingKeepsTheModelsKind(t *testing.T) {
	for name, tc := range map[string]struct {
		sample     FamilySample
		wantKind   SubjectKind
		wantSource ScopeAnchorKindSource
	}{
		"bound over a stated kind":    {FamilySample{ScopeAnchorKind: SubjectRepository, ScopeAnchorKindSource: ScopeAnchorKindAnchorBinding, ModelScopeAnchorKind: SubjectProject}, SubjectProject, ""},
		"bound over a silent model":   {FamilySample{ScopeAnchorKind: SubjectRepository, ScopeAnchorKindSource: ScopeAnchorKindAnchorBinding}, "", ""},
		"bound over a committed hint": {FamilySample{ScopeAnchorKind: SubjectRepository, ScopeAnchorKindSource: ScopeAnchorKindAnchorBinding, ModelScopeAnchorKind: SubjectTeam, ModelScopeAnchorKindSource: ScopeAnchorKindCommittedHint}, SubjectTeam, ScopeAnchorKindCommittedHint},
		"a stated kind":               {FamilySample{ScopeAnchorKind: SubjectProject}, SubjectProject, ""},
		"a committed hint":            {FamilySample{ScopeAnchorKind: SubjectTeam, ScopeAnchorKindSource: ScopeAnchorKindCommittedHint}, SubjectTeam, ScopeAnchorKindCommittedHint},
	} {
		state := BuildSemanticState(SemanticStateInput{
			Outcome:         QuestionFamilyOutcome{Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone, WinningSample: tc.sample},
			FamilyVersion:   QuestionFamilyTableVersion,
			RequestIdentity: SemanticRequestIdentityOf(validInvestigationRequest(), ""),
		})
		if state.ScopeAnchor.Kind != tc.wantKind || state.ScopeAnchor.KindSource != tc.wantSource {
			t.Errorf("%s: persisted anchor = %+v, want kind %q source %q", name, state.ScopeAnchor, tc.wantKind, tc.wantSource)
		}
		if _, err := EncodeSemanticState(state); err != nil {
			t.Errorf("%s: the persisted reading does not encode: %v", name, err)
		}
	}
}

// TestTheReadbackKeepsACountServedOnTheBoundKind: a count served on the
// bound kind, read back by id, is the count the client was served. The
// persisted reading keeps the model's kind, so the readback's own
// re-derivation does not count; the repair then leaves the served claim as
// it stands rather than force a decision it has no evidence for.
func TestTheReadbackKeepsACountServedOnTheBoundKind(t *testing.T) {
	first := identityProvenResponse(probeAlpha)
	first.context = countedTeams()
	rig := newAnchorProbeRig(t, false)
	rig.engine.anchorBindingCountEnabled = true
	rig.interpreter.read(SubjectRepository, false)
	one := rig.turn(t, needTurnRequest("request_readback_one", true), first)
	rig.verify()
	rig.interpreter.read(SubjectProject, false)
	two := rig.turn(t, continuingNeedTurn(needTurnRequest("request_readback_two", true), one.result.ResultID), needTurnResponse{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{probeAlpha}}, bases: provenCommitBases(probeAlpha), context: countedTeams(),
	})
	if two.transitions[0].ServedCount != string(CountPopulationScopeAnchorCommitted) {
		t.Fatalf("premise: served count %s, want anchor_committed on the bound kind", two.transitions[0].ServedCount)
	}
	stored := rig.store.results[two.result.ResultID]
	readback := stored
	readback.ClaimedFacts = append([]ClaimedFact(nil), stored.ClaimedFacts...)
	if RepairStoredCardinalityClaimSubject(acceptancePrincipal(), &readback, two.saved, SemanticStateReadAvailable) {
		t.Fatalf("the readback repaired the served claim: %+v", readback.ClaimedFacts)
	}
	if !reflect.DeepEqual(readback.ClaimedFacts, stored.ClaimedFacts) || len(readback.ClaimedFacts) == 0 {
		t.Fatalf("readback claims = %+v, want the served three teams on alpha unchanged", readback.ClaimedFacts)
	}
	claim := readback.ClaimedFacts[0]
	if claim.Subject.CanonicalID != probeAlpha.CanonicalID || claim.Value.Integer == nil || *claim.Value.Integer != 3 {
		t.Fatalf("readback claim = %+v, want three teams on %s", claim, probeAlpha.CanonicalID)
	}
}
