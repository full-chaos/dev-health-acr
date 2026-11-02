package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12595: a question that asks only for the current state of one handle
// ("What is the status of pull request 999 in R?") needs no evidence window.
// Before this change every single_subject question fell back to
// recent_activity_lookup, and the CHAOS-4040 gate asked the caller for a
// window it does not need (reach run part 1, turns 3 and 7).

func handleStateFrame(kind SubjectKind, goals []InvestigationGoal, temporal TemporalIntent) *QuestionFrame {
	expected := kind
	frame := DeriveFrameObligations(QuestionFrame{
		Goals:             goals,
		SubjectExpression: SubjectExpression{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{Terms: []string{"999"}, ExpectedKind: &expected}},
		Temporal:          temporal,
		Version:           QuestionFrameVersion,
	}, nil)
	return &frame
}

var handleStateTestKinds = []SubjectKind{SubjectPullRequest, SubjectWorkItem, contractsv1.ContextFabricSubjectCIRun, SubjectDeployment, SubjectIncident}

var handleStateTestPicks = []struct {
	name       string
	class      WindowClass
	confidence WindowConfidence
}{
	{"omitted", "", ""},
	{"recent_activity_high", WindowClassRecentActivityLookup, WindowConfidenceHigh},
	{"trend_high", WindowClassTrendAssessment, WindowConfidenceHigh},
	{"state_snapshot_high", WindowClassStateSnapshot, WindowConfidenceHigh},
}

func singleSubjectInterpretation(class WindowClass, confidence WindowConfidence) InterpretedQuestion {
	return InterpretedQuestion{
		Shape: ShapeSingleSubject, RequestedJudgment: "status of pull request 999",
		SubjectTerms: []string{"999"}, TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactPullRequests}},
		WindowClass:      class, WindowConfidence: confidence,
	}
}

func TestClassifyWindowForHandleStateIsStateSnapshot(t *testing.T) {
	t.Parallel()
	for _, kind := range handleStateTestKinds {
		for _, pick := range handleStateTestPicks {
			outcome := classifyWindowFor(singleSubjectInterpretation(pick.class, pick.confidence), handleStateFrame(kind, []InvestigationGoal{GoalAssessState}, TemporalIntentCurrent))
			if outcome.Class != WindowClassStateSnapshot {
				t.Errorf("%s/%s: class = %q, want state_snapshot", kind, pick.name, outcome.Class)
			}
			wantBasis, wantDowngraded := WindowClassBasisHandleState, pick.class != ""
			if pick.class == WindowClassStateSnapshot {
				wantBasis, wantDowngraded = WindowClassBasisModel, false
			}
			if outcome.Basis != wantBasis || outcome.Downgraded != wantDowngraded {
				t.Errorf("%s/%s: basis=%q downgraded=%v, want %q/%v", kind, pick.name, outcome.Basis, outcome.Downgraded, wantBasis, wantDowngraded)
			}
			if _, ok := DefaultRelativeID(outcome, windowDefaultPolicy); ok {
				t.Errorf("%s/%s: a default window was drawn for a handle-state question", kind, pick.name)
			}
		}
	}
}

// Each clause of frameAsksHandleState, failed on its own, keeps the shape
// fallback the frame had before this change.
func TestClassifyWindowForKeepsTheClassOutsideHandleState(t *testing.T) {
	t.Parallel()
	noKind := DeriveFrameObligations(QuestionFrame{
		Goals:             []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{Terms: []string{"999"}}},
		Temporal:          TemporalIntentCurrent,
		Version:           QuestionFrameVersion,
	}, nil)
	// A frame whose Kind is not named_subject but which carries a Named
	// pointer breaks invariant I1; the Kind clause is what keeps it out.
	mislabelled := *handleStateFrame(SubjectPullRequest, []InvestigationGoal{GoalAssessState}, TemporalIntentCurrent)
	mislabelled.SubjectExpression.Kind = SubjectExpressionExplicitSet
	cases := map[string]*QuestionFrame{
		"kind not named_subject": &mislabelled,
		"nil frame":              nil,
		"repository subject":     handleStateFrame(SubjectRepository, []InvestigationGoal{GoalAssessState}, TemporalIntentCurrent),
		"project subject":        handleStateFrame(SubjectProject, []InvestigationGoal{GoalAssessState}, TemporalIntentCurrent),
		"a second goal":          handleStateFrame(SubjectPullRequest, []InvestigationGoal{GoalAssessState, GoalExplainDrivers}, TemporalIntentCurrent),
		"a non-state goal only":  handleStateFrame(SubjectPullRequest, []InvestigationGoal{GoalExplainDrivers}, TemporalIntentCurrent),
		"a time series":          handleStateFrame(SubjectPullRequest, []InvestigationGoal{GoalAssessState}, TemporalIntentTimeSeries),
		"no stated kind":         &noKind,
	}
	for name, frame := range cases {
		for _, pick := range handleStateTestPicks {
			interpretation := singleSubjectInterpretation(pick.class, pick.confidence)
			got := classifyWindowFor(interpretation, frame)
			want := ClassifyWindow(interpretation, pick.class, pick.confidence)
			if got != want {
				t.Errorf("%s/%s: classifyWindowFor = %+v, want ClassifyWindow's %+v", name, pick.name, got, want)
			}
		}
	}
}

func handleStateEngine(t *testing.T, interpreter QuestionInterpreter, telemetry *recordingTelemetry) (*Engine, *acceptanceGraphReader) {
	t.Helper()
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreter,
		Graph:       graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{}, errors.New("synthesis is not under test")
		}),
		Results:   newMapResultStore(),
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "handle-state-window-test",
		Now:            func() time.Time { return time.Date(2026, 11, 2, 13, 35, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_handlestate0001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine, graph
}

// The reach-run turn-3 shape through the real Engine.Investigate: a pull
// request status question with no stated window reaches resolution with no
// window question, whatever the model's class pick; the same question about a
// repository reads an inferred window and still offers it (CHAOS-4451: the
// window is its only question, so the turn answers with the offer instead of
// ending on the CHAOS-4040 gate).
func TestHandleStateQuestionIsNotWindowGated(t *testing.T) {
	t.Parallel()
	for _, pick := range handleStateTestPicks {
		for _, kind := range []SubjectKind{SubjectPullRequest, SubjectRepository} {
			telemetry := &recordingTelemetry{}
			interpreter := &countingInterpreter{
				interpretation: singleSubjectInterpretation(pick.class, pick.confidence),
				family:         QuestionFamilyOutcome{Family: QuestionFamilySubjectInvestigation, Frame: handleStateFrame(kind, []InvestigationGoal{GoalAssessState}, TemporalIntentCurrent)},
			}
			request := validInvestigationRequest()
			request.Question = "What is the status of pull request 999 in full-chaos/dev-health-web?"
			if kind == SubjectRepository {
				request.Question = "What is the status of full-chaos/dev-health-web?"
			}
			engine, graph := handleStateEngine(t, interpreter, telemetry)
			result, _ := engine.Investigate(context.Background(), acceptancePrincipal(), request)
			if result.Status == InvestigationClarificationRequired {
				t.Errorf("%s/%s: status %q, want no window gate: the window is not a question beyond itself", kind, pick.name, result.Status)
			}
			offered := result.WindowClarification != nil && result.EffectiveEvidenceWindow != nil && result.EffectiveEvidenceWindow.Provenance == WindowInferredDefault
			wantOffered := kind == SubjectRepository && pick.class != WindowClassStateSnapshot
			if offered != wantOffered {
				t.Errorf("%s/%s: inferred window offered = %v (status %q), want %v", kind, pick.name, offered, result.Status, wantOffered)
			}
			// No offer means the turn went straight to the decisive resolution:
			// one resolve call that is NOT the gate's offers-only pass. An
			// offer means the gate's pass ran first, then the decisive one.
			if !wantOffered && (graph.resolveCalls != 1 || OffersOnlyResolution(graph.resolveCtxs[0])) {
				t.Errorf("%s/%s: resolve calls = %d (offers-only %v), want one decisive resolution", kind, pick.name, graph.resolveCalls, offersOnlyMarks(graph.resolveCtxs))
			}
			if wantOffered && (graph.resolveCalls != 2 || !OffersOnlyResolution(graph.resolveCtxs[0]) || OffersOnlyResolution(graph.resolveCtxs[1])) {
				t.Errorf("%s/%s: resolve calls = %d (offers-only %v), want the offers-only pass then the decisive resolution", kind, pick.name, graph.resolveCalls, offersOnlyMarks(graph.resolveCtxs))
			}
			if len(telemetry.windowClassifications) != 1 {
				t.Fatalf("%s/%s: window classification lines = %d, want 1", kind, pick.name, len(telemetry.windowClassifications))
			}
			recorded := telemetry.windowClassifications[0]
			if recorded.ModelClass != pick.class {
				t.Errorf("%s/%s: recorded model_class = %q, want the model's own %q", kind, pick.name, recorded.ModelClass, pick.class)
			}
			if kind == SubjectPullRequest && recorded.Outcome.Class != WindowClassStateSnapshot {
				t.Errorf("%s/%s: recorded class = %q, want state_snapshot", kind, pick.name, recorded.Outcome.Class)
			}
		}
	}
}

// A named_subject may carry several terms: a second handle, the handle's
// container (the CHAOS-12523 reading of "pull request P in R"), or a
// secondary alias. The current state of named handles needs no window however
// many terms the frame carries (codex r1 on the CHAOS-12595 PR raised the
// two-handle case; this pins the decision).
func TestHandleStateHoldsForSeveralTerms(t *testing.T) {
	t.Parallel()
	frame := handleStateFrame(SubjectPullRequest, []InvestigationGoal{GoalAssessState}, TemporalIntentCurrent)
	frame.SubjectExpression.Named.Terms = []string{"999", "full-chaos/dev-health-web"}
	if outcome := classifyWindowFor(singleSubjectInterpretation("", ""), frame); outcome.Class != WindowClassStateSnapshot || outcome.Basis != WindowClassBasisHandleState {
		t.Fatalf("two-term handle frame = %+v, want state_snapshot by handle_state", outcome)
	}
}
