package contextfabric

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func tupleFamilyOutcomeForTest(t *testing.T) QuestionFamilyOutcome {
	t.Helper()
	frame := ValidateFrame(prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate), nil, "").Frame
	return QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}), WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project"}}
}

func TestTerminalAxisConflictVetoWithTupleReadingIsServed(t *testing.T) {
	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	prior := validInvestigationResult()
	prior.ResultID = "result_prior_window_axis"
	prior.WindowClarification = &WindowClarification{Options: []WindowOption{{ReceiptID: "winr_axis", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd}}}
	store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
	asOf := time.Unix(100, 0).UTC()
	telemetry := &recordingTelemetry{}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store, Telemetry: telemetry,
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalValidTime, AsOf: &asOf}}, outcome: tupleFamilyOutcomeForTest(t)},
	})
	request := validInvestigationRequest()
	request.Consumer.Surface = "mcp"
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "winr_axis"}}
	request.Question += " Include the drivers."
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	if err != nil {
		t.Fatalf("a window veto must be served, got %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("status %s", result.Status)
	}
	assertTerminalNotSaved(t, result, telemetry, BudgetAssertWindowVeto)
}

func assertTerminalNotSaved(t *testing.T, result InvestigationResult, telemetry *recordingTelemetry, site BudgetAssertStage) {
	t.Helper()
	if !slices.Contains(result.Limitations, contractsv1.ContextFabricTerminalNotSavedLimitation) {
		t.Errorf("served answer does not say it was not saved: %v", result.Limitations)
	}
	if len(telemetry.terminalSaveSkipped) != 1 || telemetry.terminalSaveSkipped[0].Site != site || telemetry.terminalSaveSkipped[0].Outcome != TerminalSaveSkippedPayloadRejected {
		t.Errorf("skip not recorded once at %s: %+v", site, telemetry.terminalSaveSkipped)
	}
	if err := ValidateResult(result); err != nil {
		t.Errorf("served result invalid: %v", err)
	}
}

// firstSaveSupersededStore loses the structure claim race on its first Save.
type firstSaveSupersededStore struct {
	*staticResultStore
	calls int
}

func (s *firstSaveSupersededStore) Save(ctx context.Context, p storage.Principal, r InvestigationResult, w SourceWatermarkSnapshot, e RebuildEpoch, k string, ri ReuseRetrievalIdentity, pv ReusePromptVersions, va ReuseVersionAuthorities, g int64, parent string, semantic SemanticStateWrite) error {
	s.calls++
	if s.calls == 1 {
		return &ErrStructureOfferSuperseded{Members: []StructureNeedKind{contractsv1.ContextFabricStructureNeedWindow}}
	}
	return s.staticResultStore.Save(ctx, p, r, w, e, k, ri, pv, va, g, parent, semantic)
}

func TestTerminalSupersessionVetoWithTupleReadingIsServed(t *testing.T) {
	payload := workItemTuplePayloadFixture(t)
	project := payload.SubjectResolution.Candidates[0]
	project.State = ResolutionAmbiguous
	project.Confidence = 0.9
	project.EvidenceRefIDs = []string{"evidence:project:p1"}
	repo := SubjectCandidate{ReceiptID: "receipt-2", Subject: SubjectRef{Kind: SubjectRepository, CanonicalID: "repo-1", Label: "Project"}, State: ResolutionAmbiguous, MatchedTerms: []string{"project"}, MatchReasons: []string{"exact"}, Confidence: 0.9, EvidenceRefIDs: []string{"evidence:repo:r1"}}
	resolution := SubjectResolution{Candidates: []SubjectCandidate{project, repo}, Committed: []SubjectRef{}}
	store := &firstSaveSupersededStore{staticResultStore: &staticResultStore{results: map[string]InvestigationResult{}}}
	telemetry := &recordingTelemetry{}
	engine, err := NewEngine(EngineDependencies{
		Telemetry:   telemetry,
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactHealth}}}, outcome: tupleFamilyOutcomeForTest(t)},
		Graph:       &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: resolution}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{}, nil
		}),
		Results: store, Requirements: registryDeriver{},
	}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_supersession_tuple" }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, validInvestigationRequestWithConfirmedWindow())
	t.Logf("saves=%d status=%s err=%v", store.calls, result.Status, err)
	if err != nil {
		t.Fatalf("a supersession veto must be served, got %v", err)
	}
	assertTerminalNotSaved(t, result, telemetry, BudgetAssertStructureVeto)
}

// Other save errors stay errors; the strict validator still refuses a served
// answer that carries members.
func TestTerminalSaveKeepsOtherPersistenceErrors(t *testing.T) {
	for _, saveErr := range []error{errors.New("connection reset"), ErrSemanticStateReplayConflict} {
		engine := mustReuseTestEngine(t, EngineDependencies{Results: failingSaveStore{staticResultStore: &staticResultStore{results: map[string]InvestigationResult{}}, saveErr: saveErr}})
		result := InvestigationResult{ResultID: "result_other_error", Status: InvestigationNoMatch}
		if err := engine.saveTerminalResult(context.Background(), storage.Principal{OrgID: "org-1"}, BudgetAssertSubjectlessTerminal, &result, nil, ResponseBudget{}, nil, nil, "", 0, "", semanticStateCapture{}); !errors.Is(err, saveErr) {
			t.Errorf("err=%v want %v", err, saveErr)
		}
	}
	// A committed tuple payload with a foreign ref is rejected, never degraded.
	rejected := workItemTuplePayloadFixture(t)
	rejected.EvidenceRefIDs = append(append([]string(nil), rejected.EvidenceRefIDs...), "foreign")
	engine := mustReuseTestEngine(t, EngineDependencies{Results: &resultStoreStub{}})
	err := engine.saveResult(context.Background(), storage.Principal{OrgID: "org-1"}, BudgetAssertDecisive, rejected, nil, nil, "", 0, "", semanticStateCapture{Write: SemanticStateOf(workItemTupleSemanticStateFixture())})
	if !errors.Is(err, errWorkItemTuplePayloadRejected) {
		t.Errorf("decisive tuple with members must stay rejected, got %v", err)
	}
}
