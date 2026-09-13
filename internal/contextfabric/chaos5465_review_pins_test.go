package contextfabric

import (
	"context"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"testing"
)

type r4ValidatedInterpreter struct {
	frame       *QuestionFrame
	sampleGroup SubjectKind
}

func (i r4ValidatedInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	v := ValidateFrame(*i.frame, nil, ShapeOpen)
	q := InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}
	gate := DecideFrameGate(v, true)
	outcome := (RuntimeQuestionInterpreter{}).recordFamilyResolution(context.Background(), acceptancePrincipal(), q, ModelExecutionReceipt{
		QuestionFamily: QuestionFamilyGroupedCohortStatus, GroupKind: i.sampleGroup,
		QuestionFrame: &v.Frame, FrameOutcome: v.Outcome, FrameGateOutcome: gate.Outcome,
	})
	return q, outcome, nil
}

type r4FrameGraph struct {
	graphReaderStub
	seen *QuestionFrame
}

func (g *r4FrameGraph) DiscoverContext(_ context.Context, _ storage.Principal, req GraphDiscoveryRequest) (GraphContext, error) {
	g.seen = req.Frame
	member := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_r4", Label: "fixture"}
	return GraphContext{Cohort: &Cohort{Kind: SubjectTeam, Members: []CohortMember{{Subject: member, Rank: 1, InclusionReasons: []string{"fixture"}}}, Rationale: "fixture", Complete: true}}, nil
}

func TestReviewR4_ValidatedFrameComposition(t *testing.T) {
	for _, carriedGroup := range []SubjectKind{SubjectTeam, ""} {
		t.Run(string(carriedGroup), func(t *testing.T) {
			req := continuationRequest(validInvestigationRequest().Question)
			family := QuestionFamilyGroupedCohortStatus
			if carriedGroup == "" {
				family = QuestionFamilyDiscoveredCohortRanking
			}
			prior := r4CheckedPrior(t, continuationPriorID, req.Question, family, carriedGroup)
			frame := &QuestionFrame{Goals: []InvestigationGoal{GoalAssessState}, SubjectExpression: SubjectExpression{
				Kind: SubjectExpressionGroupedMembers, Grouped: &GroupedSetExpression{GroupKind: SubjectProject, MemberKind: SubjectTeam},
			}}
			valid := ValidateFrame(*frame, nil, ShapeOpen)
			if valid.Outcome != FrameValidationOutcomeValid {
				t.Fatalf("fixture invalid: %+v", valid.Failure)
			}
			// Turn one saved a validated frame on the carried reading, as a
			// production turn does; retrieval must receive THAT frame.
			member := SubjectRepository
			if carriedGroup == "" {
				member = SubjectTeam
			}
			harness := newContinuationHarness(t, withFramedCarrier(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}, prior, member), r4ValidatedInterpreter{frame, SubjectProject})
			graph := &r4FrameGraph{graphReaderStub: harness.engine.graph.(graphReaderStub)}
			harness.engine.graph = graph
			result := harness.investigate(t, req)
			d := harness.soleDecision(t)
			if graph.seen == nil {
				t.Fatalf("discovery was not reached (disposition=%s reason=%s composition=%s refusal_basis=%q)", d.Disposition, d.Reason, d.CompositionOutcome, result.RefusalBasis)
			}
			_, _, servedGroup := servedPlanAxes(result)
			failure, invalid := ValidateFramePhaseA1(*graph.seen)
			executedGroup, _ := graph.seen.SubjectExpression.GroupKind()
			t.Logf("fresh_valid=%v fresh_gate=%s disposition=%s accepted_group=%q executed_kind=%q executed_group=%q invalid=%v invariant=%s detail=%s served_group=%q", valid.Outcome == FrameValidationOutcomeValid, DecideFrameGate(valid, true).Outcome, d.Disposition, d.AcceptedGroupKind(), graph.seen.SubjectExpression.Kind, executedGroup, invalid, failure.Invariant, failure.Detail, servedGroup)
			if invalid {
				t.Errorf("admission converted a validated frame to invalid retrieval input")
			}
			// AND IT IS THE CARRIED FRAME, not the fresh one: the reading the
			// caller confirmed is the one retrieval executes.
			if d.Accepted == nil || d.Accepted.State == nil || d.Accepted.State.Frame == nil || !framesEqual(*graph.seen, *d.Accepted.State.Frame) {
				t.Errorf("discovery received a frame that is not the carried one")
			}
		})
	}
}

func TestReviewR4_ComparisonUsesEffectiveGroup(t *testing.T) {
	req := continuationRequest(validInvestigationRequest().Question)
	prior := r4CheckedPrior(t, continuationPriorID, req.Question, QuestionFamilyGroupedCohortStatus, SubjectTeam)
	frame := &QuestionFrame{Goals: []InvestigationGoal{GoalAssessState}, SubjectExpression: SubjectExpression{
		Kind: SubjectExpressionGroupedMembers, Grouped: &GroupedSetExpression{GroupKind: SubjectProject, MemberKind: SubjectRepository},
	}}
	valid := ValidateFrame(*frame, nil, ShapeOpen)
	if valid.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("fixture invalid: %+v", valid.Failure)
	}
	interpreter := r4ValidatedInterpreter{frame, SubjectTeam}
	q, f, _ := interpreter.Interpret(context.Background(), acceptancePrincipal(), req)
	freshPlan := PlanAnswer(PlanAnswerInput{Family: f, Interpretation: q})
	h := newContinuationHarness(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}, interpreter)
	h.investigate(t, req)
	d := h.soleDecision(t)
	t.Logf("fresh_plan_group=%q accepted_group=%q compared_fresh_group=%q agreement=%v conflict_fields=%v", freshPlan.GroupKind, d.AcceptedGroupKind(), d.Fresh.GroupKind, d.Agreement, d.ConflictFieldTokens())
	if d.Agreement && freshPlan.GroupKind != d.AcceptedGroupKind() {
		t.Error("Info comparison reports agreement despite replacing effective group axis")
	}
}

func TestReviewR4_SaveRaceDecision(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "decisive", true: "subjectless_terminal"}[terminal], func(t *testing.T) {
			req := continuationRequest(validInvestigationRequest().Question)
			prior := r4CheckedPrior(t, continuationPriorID, req.Question, QuestionFamilyGroupedCohortStatus, SubjectTeam)
			store := withCarrierStates(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}})
			h := newContinuationHarness(t, store, forcedFamilyInterpreter{family: QuestionFamilyDiscoveredCohortRanking})
			race := &supersessionRacingResultStore{staticResultStore: store, conflictMembers: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow}}
			h.engine.results = race
			if terminal {
				h.engine.graph = graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}}
			}
			result := h.investigate(t, req)
			d := h.soleDecision(t)
			t.Logf("save_calls=%d status=%s window_present=%v decision=%s reason=%s applied_window=%q canonicalization=%v", race.saveCalls, result.Status, result.EffectiveEvidenceWindow != nil, d.Disposition, d.Reason, d.AppliedWindowToken(), h.telemetry.windowCanonicalizationOutcomes)
			if d.Applies() && result.EffectiveEvidenceWindow == nil {
				t.Error("continuation event says applied after save-time window veto discarded result")
			}
		})
	}
}

func TestReviewR4_ByteIdentityControl(t *testing.T) {
	req := continuationRequest(validInvestigationRequest().Question)
	prior := r4CheckedPrior(t, continuationPriorID, req.Question, QuestionFamilyGroupedCohortStatus, SubjectTeam)
	req.Question += " "
	if CanonicalizeQuestion(req.Question) != CanonicalizeQuestion(prior.Question) {
		t.Fatal("fixture must have equal canonical identities")
	}
	h := newContinuationHarness(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}, forcedFamilyInterpreter{family: QuestionFamilyDiscoveredCohortRanking})
	h.investigate(t, req)
	d := h.soleDecision(t)
	t.Logf("raw_equal=%v canonical_equal=true disposition=%s reason=%s", req.Question == prior.Question, d.Disposition, d.Reason)
	if d.Reason != ContinuationReasonChangedQuestion {
		t.Error("canonical equality must not admit byte-different continuation")
	}
}

func TestReviewR4_WindowConflictControls(t *testing.T) {
	for _, relative := range []RelativeWindowID{RelativeWindowTrailing90D, RelativeWindowTrailing30D} {
		t.Run(string(relative), func(t *testing.T) {
			req := continuationRequest(validInvestigationRequest().Question)
			prior := r4CheckedPrior(t, continuationPriorID, req.Question, QuestionFamilyGroupedCohortStatus, SubjectTeam)
			req.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: relative}
			h := newContinuationHarness(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}, forcedFamilyInterpreter{family: QuestionFamilyDiscoveredCohortRanking})
			result := h.investigate(t, req)
			d := h.soleDecision(t)
			t.Logf("explicit=%s disposition=%s reason=%s window_present=%v", relative, d.Disposition, d.Reason, result.EffectiveEvidenceWindow != nil)
			if relative == RelativeWindowTrailing90D && !d.Applies() {
				t.Error("agreeing receipt must apply")
			}
			if relative == RelativeWindowTrailing30D && d.Reason != ContinuationReasonWindowVeto {
				t.Error("conflicting window must veto before admission")
			}
		})
	}
}
