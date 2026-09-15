package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestWorkItemProspectiveTerminalsPersist(t *testing.T) {
	for _, mode := range []string{"no_match", "unprojected", "ambiguous", "window_required"} {
		t.Run(mode, func(t *testing.T) { runWorkItemProspectiveTerminal(t, mode) })
	}
}

func runWorkItemProspectiveTerminal(t *testing.T, mode string) {
	state := validWorkItemTupleSemanticState(t)
	interpretation := bootstrapInterpretation()
	interpretation.Shape = ShapeDiscoveredCohort
	interpretation.SubjectTerms = []string{"Project Alpha"}
	interpreter := &countingInterpreter{interpretation: interpretation, family: QuestionFamilyOutcome{
		Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel,
		Frame: state.Frame, WinningSampleIndex: 0,
		WinningSample: FamilySample{ModelFamily: QuestionFamilyScopedCohortStatus, ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project Alpha"},
	}}
	store := &resultStoreStub{}
	telemetry := &recordingTelemetry{}
	graph := graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
	if mode == "unprojected" {
		graph.resolution.GraphNotProjected = true
	}
	if mode == "ambiguous" {
		graph.resolution.Candidates = []SubjectCandidate{{ReceiptID: "receipt_project_01", Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project-one", Label: "Project One"}, State: ResolutionProposed, MatchReasons: []string{"ambiguous"}, Confidence: 0.6}}
		graph.resolution.ClarificationPrompt = "Which project?"
	}
	engine := mustReuseTestEngine(t, EngineDependencies{Interpreter: interpreter, Graph: graph, Results: store, Telemetry: telemetry})
	request := validInvestigationRequestWithConfirmedWindow()
	request.Question = "What is the status of the work items in Project Alpha?"
	if mode == "window_required" {
		request.TimeContext.EvidenceWindow = nil
	}
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	t.Logf("Investigate status=%q err=%v saved=%q", result.Status, err, store.saved.ResultID)
	for _, event := range telemetry.semanticStatePersistences {
		t.Logf("persistence site=%s decision=%s absence=%s", event.Site, event.Decision, event.Absence)
	}
	if err != nil {
		t.Fatalf("fresh scoped question must preserve its bounded terminal answer: %v", err)
	}
	wantStatus := InvestigationNoMatch
	if mode == "ambiguous" || mode == "window_required" {
		wantStatus = InvestigationClarificationRequired
	}
	if result.Status != wantStatus || store.saved.Status != wantStatus {
		t.Fatalf("terminal status=%s saved=%s want=%s", result.Status, store.saved.Status, wantStatus)
	}
	if store.saved.ResultID == "" {
		t.Fatal("no saved answer")
	}
	if err := store.saved.Validate(); err != nil {
		t.Fatalf("invalid saved result: %v", err)
	}
}

func TestWorkItemProspectiveTerminalSaveDomain(t *testing.T) {
	for _, name := range []string{"empty", "offer", "window", "decisive_site", "decisive_status", "unmeasured_census", "zero_census", "committed", "committed_candidate", "cohort", "claims", "paths", "drivers", "remaining", "readiness", "conflicts", "refs", "labels", "judgment", "state", "pressures"} {
		t.Run(name, func(t *testing.T) {
			defer reportWorkItemTerminalTestPanic(t)
			state := workItemTupleSemanticStateFixture()
			state.WorkItemCensus = nil
			result := InvestigationResult{ResultID: "result_prospective", Status: InvestigationNoMatch}
			site := BudgetAssertSubjectlessTerminal
			sample := workItemTuplePayloadFixture(t)
			switch name {
			case "offer":
				result.Status = InvestigationClarificationRequired
				result.SubjectResolution.Candidates = sample.SubjectResolution.Candidates
				result.SubjectResolution.Candidates[0].State = ResolutionAmbiguous
			case "window":
				site = BudgetAssertWindowConfirmationRequired
				result.Status = InvestigationClarificationRequired
			case "decisive_site":
				site = BudgetAssertDecisive
			case "decisive_status":
				result.Status = InvestigationComplete
			case "unmeasured_census":
				state.WorkItemCensus = &WorkItemTupleCensus{State: WorkItemMembershipCensusUnmeasured}
			case "zero_census":
				state.WorkItemCensus = &WorkItemTupleCensus{State: WorkItemMembershipCensusExact}
			case "committed":
				result.SubjectResolution.Committed = sample.SubjectResolution.Committed
			case "committed_candidate":
				result.SubjectResolution.Candidates = sample.SubjectResolution.Candidates
			case "cohort":
				result.Cohort = &Cohort{Members: []CohortMember{}}
			case "claims":
				result.ClaimedFacts = []ClaimedFact{{}}
			case "paths":
				result.Paths = []RelationshipPath{{}}
			case "drivers":
				result.Drivers = []DriverJudgment{{}}
			case "remaining":
				result.RemainingWork = []Finding{{}}
			case "readiness":
				result.ReadinessGaps = []Finding{{}}
			case "conflicts":
				result.Conflicts = []Finding{{}}
			case "refs":
				result.EvidenceRefIDs = []string{"foreign"}
			case "labels":
				result.EvidenceRefLabels = map[string]string{"foreign": "label"}
			case "judgment":
				result.DirectJudgment = "answer"
			case "state":
				result.CurrentState = "answer"
			case "pressures":
				result.StrongestPressures = []string{"answer"}
			}
			store := &resultStoreStub{}
			engine := mustReuseTestEngine(t, EngineDependencies{Results: store})
			err := engine.saveResult(context.Background(), storage.Principal{OrgID: "org-1"}, site, result, nil, nil, "", 0, "", semanticStateCapture{Write: SemanticStateOf(state)})
			want := name == "empty" || name == "offer" || name == "window"
			if (err == nil) != want || (store.saved.ResultID != "") != want {
				t.Errorf("save err=%v persisted=%s want allowed=%v", err, store.saved.ResultID, want)
			}
		})
	}
}

func TestWorkItemProspectiveTerminalNilReading(t *testing.T) {
	defer reportWorkItemTerminalTestPanic(t)
	if workItemTuplePreMembershipTerminal(BudgetAssertSubjectlessTerminal, InvestigationResult{Status: InvestigationNoMatch}, nil) {
		t.Error("nil reading qualified for tuple terminal exemption")
	}
}

func reportWorkItemTerminalTestPanic(t *testing.T) {
	if recovered := recover(); recovered != nil {
		t.Errorf("work-item guard panic: %v", recovered)
	}
}
