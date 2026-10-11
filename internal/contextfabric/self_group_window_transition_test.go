package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type selfGroupInterpreter struct{ firstFrame string }

func (i selfGroupInterpreter) Interpret(_ context.Context, _ storage.Principal, req InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	if len(req.PriorWindowReceipts) == 0 && i.firstFrame != "self" {
		var fr *QuestionFrame
		gate := FrameGate{Outcome: FrameGateNotProposed}
		if i.firstFrame == "discovered" {
			f := QuestionFrame{Version: QuestionFrameVersion,
				SubjectExpression: SubjectExpression{Kind: SubjectExpressionDiscoveredKind, Discovered: &DiscoveredSetExpression{MemberKind: SubjectTeam}},
				Obligations:       []AnswerObligation{ObligationState}, Goals: []InvestigationGoal{GoalAssessState}, Temporal: TemporalIntentCurrent}
			v := ValidateFrame(f, nil, ShapeDiscoveredCohort)
			gate = DecideFrameGate(v, true)
			fr = &v.Frame
		}
		return InterpretedQuestion{Shape: ShapeDiscoveredCohort, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}},
			QuestionFamilyOutcome{Family: QuestionFamilyGroupedCohortStatus, Source: QuestionFamilySourceModel, Frame: fr, Gate: gate,
				WinningSample: FamilySample{ModelFamily: QuestionFamilyGroupedCohortStatus, GroupKind: SubjectTeam}, Version: QuestionFamilyTableVersion}, nil
	}
	frame := QuestionFrame{
		Version: QuestionFrameVersion,
		SubjectExpression: SubjectExpression{
			Kind:    SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{GroupKind: SubjectTeam, MemberKind: SubjectTeam},
		},
		Obligations: []AnswerObligation{ObligationState},
		Goals:       []InvestigationGoal{GoalAssessState},
		Temporal:    TemporalIntentCurrent,
	}
	validated := ValidateFrame(frame, nil, ShapeDiscoveredCohort)
	gate := DecideFrameGate(validated, true)
	return InterpretedQuestion{Shape: ShapeDiscoveredCohort, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}},
		QuestionFamilyOutcome{
			Family: QuestionFamilyGroupedCohortStatus, Source: QuestionFamilySourceModel,
			Frame: &frame, Gate: gate,
			WinningSample: FamilySample{ModelFamily: QuestionFamilyGroupedCohortStatus, GroupKind: SubjectTeam},
			Version:       QuestionFamilyTableVersion,
		}, nil
}

type capturingStore struct{ *staticResultStore }

func (s capturingStore) Save(ctx context.Context, p storage.Principal, result InvestigationResult, w SourceWatermarkSnapshot, e RebuildEpoch, k string, r ReuseRetrievalIdentity, v ReusePromptVersions, a ReuseVersionAuthorities, n int64, x string, sem SemanticStateWrite) error {
	s.results[result.ResultID] = result
	if sem.State != nil {
		s.states[result.ResultID] = sem.State
	}
	return s.staticResultStore.Save(ctx, p, result, w, e, k, r, v, a, n, x, sem)
}

func runSelfGroupTwoTurn(t *testing.T, first string) (InvestigationResult, InvestigationResult) {
	store := &staticResultStore{results: map[string]InvestigationResult{}, states: map[string]*PersistedSemanticState{}}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	telemetry := &recordingTelemetry{}
	fresh := validInvestigationResult()
	n := 0
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			bases:      provenCommitBases(project),
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return fresh, nil
		}),
		Interpreter: selfGroupInterpreter{firstFrame: first},
		Results:     capturingStore{store},
		Telemetry:   telemetry,
	})
	_ = n
	req := validInvestigationRequest()
	r1, err := engine.Investigate(context.Background(), acceptancePrincipal(), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("t1 status=%q basis=%q id=%q windowclar=%v", r1.Status, r1.RefusalBasis, r1.ResultID, r1.WindowClarification != nil)
	if r1.WindowClarification == nil || len(r1.WindowClarification.Options) == 0 {
		return r1, InvestigationResult{}
	}
	req2 := validInvestigationRequest()
	req2.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: r1.ResultID, ReceiptID: r1.WindowClarification.Options[0].ReceiptID}}
	r2, err := engine.Investigate(context.Background(), acceptancePrincipal(), req2)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("t2 status=%q basis=%q claimed=%d", r2.Status, r2.RefusalBasis, len(r2.ClaimedFacts))
	return r1, r2
}

// TestSelfGroupRefusesOnAnEstablishedWindowTransition: a window receipt does not
// launder a group kind equal to its member kind. Turn one is frame-less, a
// legal discovered-team frame, or itself self-grouped; turn two (the window
// redemption) reads the question as a self-group. Every cell must end
// no_match / frame_invariant_violated with no claimed fact. Rule changed
// deliberately: CHAOS-5582 dropped every fresh refusing gate on such a turn;
// I6 is now kept, every other invariant is still dropped (see
// TestCHAOS5582_NoFreshGateDecidesAnEstablishedTransition).
func TestSelfGroupRefusesOnAnEstablishedWindowTransition(t *testing.T) {
	for _, first := range []string{"self", "none", "discovered"} {
		t.Run(first, func(t *testing.T) {
			_, r2 := runSelfGroupTwoTurn(t, first)
			if r2.ResultID == "" {
				if first != "self" {
					t.Fatalf("turn one offered no window")
				}
				return
			}
			if r2.Status != InvestigationNoMatch || r2.RefusalBasis != contractsv1.ContextFabricRefusalBasisFrameInvariantViolated || len(r2.ClaimedFacts) != 0 {
				t.Errorf("redemption turn not refused: status=%q basis=%q claimed=%d", r2.Status, r2.RefusalBasis, len(r2.ClaimedFacts))
			}
		})
	}
}

// TestSelfGroupRefusalOnATransitionIsNamedOnTheDecisionLine: the continuation
// decision line says the fresh refusal decided the turn and which invariant.
func TestSelfGroupRefusalOnATransitionIsNamedOnTheDecisionLine(t *testing.T) {
	question := validInvestigationRequest().Question
	prior := continuationPrior(t, continuationPriorID, question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	interpreter := refusingGate5582{family: QuestionFamilyDiscoveredCohortRanking, gate: FrameGate{Outcome: FrameGateRejectedInvalid, FailedInvariant: FrameInvariantI6}}
	run := axis5582Investigate(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}, interpreter, continuationRequest(question))
	if run.err != nil {
		t.Fatalf("Investigate() error = %v", run.err)
	}
	line := run.soleDecisionLine(t)
	if run.result.Status != InvestigationNoMatch || run.result.RefusalBasis != contractsv1.ContextFabricRefusalBasisFrameInvariantViolated {
		t.Errorf("status=%q basis=%q, want no_match/frame_invariant_violated", run.result.Status, run.result.RefusalBasis)
	}
	if got := line["composition_outcome"]; got != string(CompositionFreshRefused) {
		t.Errorf("composition_outcome = %v, want %s", got, CompositionFreshRefused)
	}
	if got := line["composition_failed_invariant"]; got != string(FrameInvariantI6) {
		t.Errorf("composition_failed_invariant = %v, want i6", got)
	}
}
