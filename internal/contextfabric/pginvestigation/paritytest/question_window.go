package paritytest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A window offer redeemed for the identical question governs that turn's time,
// whether or not the caller threads its conversation. A threading caller names
// the turn it continues as the parent and sends an empty subject-hint list; a
// caller that does not thread sends only the window receipt. Both redeem the
// same offer for the same question and must be served alike -- under the
// confirmed window, never refused because the model read the re-asked question
// on another time axis. RunQuestionWindowRedemptionSuite drives the REAL
// Engine.Investigate over a store through the production interpreter adapter
// (contextfabric.RuntimeQuestionInterpreter) with only the model seam
// scripted, and reads the decision off the production decision line.

var questionWindowPrincipal = storage.Principal{OrgID: "org_question_window"}

var questionWindowSubject = contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project-question-window", Label: "Question Window"}

// questionWindowModel answers the Nth interpret call with the Nth scripted
// interpretation.
type questionWindowModel struct {
	mu    sync.Mutex
	turns []contextfabric.InterpretedQuestion
	next  int
}

func (m *questionWindowModel) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.next >= len(m.turns) {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, fmt.Errorf("question-window model: no interpretation scripted for call %d", m.next+1)
	}
	interpreted := m.turns[m.next]
	m.next++
	return interpreted, questionWindowReceipt(), nil
}

func (m *questionWindowModel) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, fmt.Errorf("question-window model: synthesis is not scripted")
}

func questionWindowReceipt() contextfabric.ModelExecutionReceipt {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	return contextfabric.ModelExecutionReceipt{
		Operation: contextfabric.ModelOperationInterpret, Provider: "scripted", Model: "scripted-model", ModelVersion: "scripted-model-v1",
		PromptVersion: "scripted-prompt-v1", SchemaVersion: "scripted-schema-v1", EvaluatorVersion: "scripted-eval-v1",
		StartedAt: at, CompletedAt: at, Attempts: 1,
		InputDigest: strings.Repeat("a", 64), OutputDigest: strings.Repeat("b", 64), Outcome: "success",
	}
}

type questionWindowGraph struct{}

func (questionWindowGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "question-window-key", Epoch: 0}, nil
}

func (questionWindowGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	bases := contextfabric.CommitBasisSet{}
	bases.Record(questionWindowSubject, contextfabric.CommitBasisCallerCanonicalID)
	return contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{questionWindowSubject}, Candidates: []contextfabric.SubjectCandidate{}},
		contextfabric.StructureOfferMaterial{}, bases, nil, nil
}

func (questionWindowGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	return contextfabric.GraphContext{Paths: []contextfabric.RelationshipPath{}, Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}}}, nil
}

type questionWindowFacts struct{}

func (questionWindowFacts) ReadFacts(context.Context, storage.Principal, contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	return contextfabric.CanonicalFactBundle{Facts: []contextfabric.CanonicalFact{}, Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[contextfabric.FactKind]string{}, Watermarks: map[contextfabric.FactKind]string{}}, nil
}

type questionWindowSynth struct{}

func (questionWindowSynth) Synthesize(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
	return result("result-question-window-synth", "how is the project trending?"), nil
}

// QuestionWindowRedemption is one executed two-turn conversation: what the
// second turn served and the decision line it emitted.
type QuestionWindowRedemption struct {
	Threaded     bool
	Status       contractsv1.ContextFabricInvestigationStatus
	AxisVetoed   bool
	Window       *contractsv1.ContextFabricEffectiveEvidenceWindow
	PlanFamily   contextfabric.QuestionFamily
	PlanSource   contextfabric.QuestionFamilySource
	DecisionLine map[string]any
}

// RunQuestionWindowRedemptionSuite runs the conversation twice against a fresh
// store each time -- once threaded, once not -- and returns both second turns.
// Turn one asks a trend question with no window and is offered one; turn two
// re-asks the identical question redeeming the trailing-90-day offer while the
// model reads it as a 90-day range.
func RunQuestionWindowRedemptionSuite(t *testing.T, newStore func(t *testing.T) contextfabric.InvestigationResultStore) []QuestionWindowRedemption {
	t.Helper()
	var runs []QuestionWindowRedemption
	for _, threaded := range []bool{false, true} {
		runs = append(runs, runQuestionWindowRedemption(t, newStore(t), threaded))
	}
	return runs
}

func runQuestionWindowRedemption(t *testing.T, store contextfabric.InvestigationResultStore, threaded bool) QuestionWindowRedemption {
	t.Helper()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	start := now.Add(-90 * 24 * time.Hour)
	model := &questionWindowModel{turns: []contextfabric.InterpretedQuestion{
		{Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, WindowClass: contextfabric.WindowClassTrendAssessment},
		{Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &now}, WindowClass: contextfabric.WindowClassTrendAssessment},
	}}
	var logs bytes.Buffer
	next := 0
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: contextfabric.RuntimeQuestionInterpreter{Runtime: model},
		Graph:       questionWindowGraph{},
		Facts:       questionWindowFacts{},
		Synthesizer: questionWindowSynth{},
		Results:     store,
		Telemetry:   contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))),
	}, contextfabric.EngineOptions{
		ServiceVersion: "question-window-parity",
		Now:            func() time.Time { return now },
		NewResultID: func() string {
			next++
			return fmt.Sprintf("result_question_window_%t_%02d", threaded, next)
		},
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1,
		RequestID:     "request_question_window_one",
		Question:      "How has the project been trending?",
		TimeContext:   contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "context-fabric-workbench", Version: "0.1.0", Surface: "workbench"},
	}
	first, err := engine.Investigate(context.Background(), questionWindowPrincipal, request)
	if err != nil {
		t.Fatalf("turn one: %v", err)
	}
	if first.WindowClarification == nil {
		t.Fatalf("fixture defect: turn one must offer a window; status=%s", first.Status)
	}
	receipt := ""
	for _, option := range first.WindowClarification.Options {
		if option.RelativeID == contextfabric.RelativeWindowTrailing90D {
			receipt = option.ReceiptID
		}
	}
	if receipt == "" {
		t.Fatalf("fixture defect: turn one offered no trailing-90-day window")
	}
	second := request
	second.RequestID = "request_question_window_two"
	second.PriorWindowReceipts = []contextfabric.BoundSubjectReceipt{{ResultID: first.ResultID, ReceiptID: receipt}}
	if threaded {
		second.ParentResultID = first.ResultID
		second.RequestedScope.SubjectHints = []contractsv1.ContextFabricSubjectHint{}
	}
	logs.Reset()
	served, err := engine.Investigate(context.Background(), questionWindowPrincipal, second)
	if err != nil {
		t.Fatalf("turn two: %v", err)
	}
	run := QuestionWindowRedemption{Threaded: threaded, Status: served.Status, Window: served.EffectiveEvidenceWindow}
	if served.AnswerPlan != nil {
		run.PlanFamily, run.PlanSource = served.AnswerPlan.Family, served.AnswerPlan.FamilySource
	}
	scanner := bufio.NewScanner(bytes.NewReader(logs.Bytes()))
	scanner.Buffer(make([]byte, 0, 1<<16), 1<<22)
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("emitted line is not JSON: %v", err)
		}
		if line["msg"] == "context fabric window continuation decision" {
			if run.DecisionLine != nil {
				t.Fatalf("turn two emitted more than one decision line")
			}
			run.DecisionLine = line
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading emitted lines: %v", err)
	}
	if run.DecisionLine == nil {
		t.Fatalf("turn two emitted no decision line")
	}
	run.AxisVetoed = run.DecisionLine["interpreted_axis_outcome"] == "vetoed"
	return run
}

// AssertQuestionWindowRedemptionParity fails unless the threaded and the
// unthreaded second turns were served alike and each decision line names its
// own parent relation. wantConfirmed additionally requires both to be served
// under the confirmed window with the sampled axis overridden -- the outcome on
// a store that proves the offering turn's graph epoch; a store that cannot
// prove it withholds the continuation on both shapes alike.
func AssertQuestionWindowRedemptionParity(t *testing.T, runs []QuestionWindowRedemption, wantConfirmed bool) {
	t.Helper()
	if len(runs) != 2 || runs[0].Threaded || !runs[1].Threaded {
		t.Fatalf("want one unthreaded then one threaded run, got %d", len(runs))
	}
	unthreaded, threaded := runs[0], runs[1]
	if got := unthreaded.DecisionLine["parent_reference"]; got != "absent" {
		t.Errorf("unthreaded parent_reference = %v, want absent", got)
	}
	if got := threaded.DecisionLine["parent_reference"]; got != "window_receipt_result" {
		t.Errorf("threaded parent_reference = %v, want window_receipt_result", got)
	}
	if unthreaded.Status != threaded.Status || unthreaded.PlanFamily != threaded.PlanFamily || unthreaded.PlanSource != threaded.PlanSource ||
		(unthreaded.Window == nil) != (threaded.Window == nil) || unthreaded.AxisVetoed != threaded.AxisVetoed {
		t.Errorf("served differently: unthreaded status=%s family=%s/%s window=%v vetoed=%v; threaded status=%s family=%s/%s window=%v vetoed=%v",
			unthreaded.Status, unthreaded.PlanFamily, unthreaded.PlanSource, unthreaded.Window != nil, unthreaded.AxisVetoed,
			threaded.Status, threaded.PlanFamily, threaded.PlanSource, threaded.Window != nil, threaded.AxisVetoed)
	}
	for _, key := range []string{"continuation_disposition", "decision_reason", "interpreted_axis_outcome", "executed_axis", "carried_axis", "question_window_confirmed", "refusal_basis"} {
		if unthreaded.DecisionLine[key] != threaded.DecisionLine[key] {
			t.Errorf("%s differs: unthreaded %v, threaded %v", key, unthreaded.DecisionLine[key], threaded.DecisionLine[key])
		}
	}
	if !wantConfirmed {
		return
	}
	for _, run := range runs {
		if run.AxisVetoed || run.DecisionLine["interpreted_axis_outcome"] != "overridden_by_receipt" || run.DecisionLine["executed_axis"] != "current" ||
			run.DecisionLine["question_window_confirmed"] != true {
			t.Errorf("threaded=%v: decision %v/%v executed=%v confirmed=%v, want overridden_by_receipt under the confirmed window",
				run.Threaded, run.DecisionLine["decision_reason"], run.DecisionLine["interpreted_axis_outcome"], run.DecisionLine["executed_axis"], run.DecisionLine["question_window_confirmed"])
		}
		if run.Window == nil || run.Window.Provenance != contextfabric.WindowClarificationConfirmed || run.Window.RelativeID != contextfabric.RelativeWindowTrailing90D {
			t.Errorf("threaded=%v: served window %+v, want the confirmed trailing-90-day window", run.Threaded, run.Window)
		}
	}
}
