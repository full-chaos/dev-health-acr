package contextfabric

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// prodStatusPeriodFrame is the frame the interpreter wrote for a question that
// states a status in a relative clause and a period on a verb: children of a
// project, work-item members, a status qualifier, and a bounded window.
func prodStatusPeriodFrame() QuestionFrame {
	frame := frameWith([]InvestigationGoal{GoalAssessState}, scopedExpression(SubjectWorkItem), TemporalIntentBoundedWindow, nil)
	frame.SubjectExpression.Scoped.MemberQualifier = MemberQualifierStatus
	frame.SubjectExpression.Scoped.MemberQualifierValue = "done"
	return frame
}

type interpreterPath int

const (
	serverInterpretedPath interpreterPath = iota
	suppliedInterpretedPath
)

// runInterpretedTupleCase walks Engine.Investigate through the real
// RuntimeQuestionInterpreter: frame validation, the frame gate and family
// resolution run on the frame as the interpreter returned it.
func runInterpretedTupleCase(t *testing.T, path interpreterPath, question string, frame QuestionFrame, timeContext TimeContext) statusFilterRun {
	t.Helper()
	var run statusFilterRun
	payload := workItemTuplePayloadFixture(t)
	run.graph = &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: payload.SubjectResolution, bases: provenCommitBases(payload.SubjectResolution.Committed...)}}
	telemetry := &recordingTelemetry{}
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	gate, _ := NewWorkItemMembershipGate(1, 0)
	interpreted := InterpretedQuestion{Shape: ShapeExplicitCohort, RequestedJudgment: "none", TimeContext: timeContext, FactRequirements: []FactRequirement{{Kind: FactStatus}}}
	if path == suppliedInterpretedPath {
		interpreted.WindowClass, interpreted.WindowConfidence = WindowClassExplicitWindow, WindowConfidenceHigh
	}
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.QuestionFrame = &frame
	receipt.QuestionFamily = QuestionFamilyScopedCohortStatus
	receipt.ScopeAnchorKind = SubjectProject
	receipt.ScopeAnchorTerm = "Alpha"
	receipt.RequestedSubjectKind = SubjectWorkItem
	request := statedPeriodRequest(question)
	request.Consumer.Surface = mcpSurface
	interpreter := RuntimeQuestionInterpreter{Sink: &fakeReceiptSink{}}
	switch path {
	case serverInterpretedPath:
		interpreter.Runtime = fakeModelRuntime{interpreted: interpreted, receipt: receipt}
	case suppliedInterpretedPath:
		receipt.Provider = "client-supplied"
		interpreter.Supplied = &scriptedSuppliedRuntime{interpreted: interpreted, receipt: receipt}
		request = suppliedInvestigationRequest(request)
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreter,
		Graph:       run.graph,
		CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
			return true, ""
		},
		WorkItemMembership: tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, membershipRequest WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
			run.reads++
			run.request = membershipRequest
			lease, err := gate.Acquire(ctx)
			return lease, WorkItemMembershipResult{Census: WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 1}, Members: []WorkItemMembershipMember{{CanonicalID: payload.Cohort.Members[0].Subject.CanonicalID, WorkItemID: "work-1"}}}, err
		}),
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			run.factReads++
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1"}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{Status: InvestigationComplete, DirectJudgment: "Available work items.", CurrentState: "Available work items.", DeterministicAnswer: "Available work items.", StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, Warnings: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"}}, nil
		}), Results: store, Requirements: registryDeriver{}, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_status_period_001" }})
	if err != nil {
		t.Fatal(err)
	}
	run.result, run.invokedErr = engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, request)
	run.admissions = telemetry.workItemTupleAdmissions
	return run
}

func TestMemberTimeRoleBinderReadsAPresentCopulaStatusAsNoRole(t *testing.T) {
	cases := []struct {
		question string
		reason   MemberTimeRoleReason
		role     MemberTimeRole
	}{
		{"which work items of project Alpha that are closed were created in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCreated},
		{"which work items of project Alpha are closed were created in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCreated},
		{"Which issues that are done were updated in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleUpdated},
		{"Which issues that are now closed were opened in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCreated},
		{"Which issues that are new were closed in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCompleted},
		{"Which issue that is closed was created in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCreated},
		{"Which issues that are created and closed were updated in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleUpdated},
		// a form that leads into the period governs it
		{"Which issues are closed in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCompleted},
		{"Which issues are closed within the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCompleted},
		{"Which issues are created and closed in the last 30 days?", MemberTimeRoleAmbiguous, ""},
		{"Which issues are created or opened in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCreated},
		{"Which issues that were created are done in the last 30 days?", MemberTimeRoleAmbiguous, ""},
		{"Which issues that were created are closed within the last 30 days?", MemberTimeRoleAmbiguous, ""},
		{"which work items of project Alpha were created and closed in the last 30 days?", MemberTimeRoleAmbiguous, ""},
		// a past copula is a predicate, not a present state
		{"Which issues that were closed were created in the last 30 days?", MemberTimeRoleAmbiguous, ""},
		// a form after the period is left as it was
		{"Which issues in the last 30 days are closed and were created?", MemberTimeRoleAmbiguous, ""},
		{"Which issues are in progress in the last 30 days?", MemberTimeRoleNoVerb, ""},
	}
	for _, tc := range cases {
		outcome := BindMemberTimeRole(tc.question, windowSpanOf(t, tc.question))
		if outcome.Reason != tc.reason || outcome.Role != tc.role {
			t.Errorf("%q: reason=%s role=%q, want %s %q", tc.question, outcome.Reason, outcome.Role, tc.reason, tc.role)
		}
	}
}

// The question forms walked through Engine.Investigate with the frame the
// interpreter wrote on prod: a status qualifier and a period frame on the
// current axis. The status is stated in a relative clause or before the noun,
// in the singular and the plural.
func TestWorkItemStatusPeriodQuestionFormsServeOnTheFullPath(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	cases := []struct {
		question string
		column   string
		role     string
	}{
		{"which work items of project Alpha that are closed were created in the last 30 days?", "created_at", "created"},
		{"which work items of project Alpha are closed were created in the last 30 days?", "created_at", "created"},
		{"which work item of project Alpha that is closed was created in the last 30 days?", "created_at", "created"},
		{"which work item of project Alpha that's closed was created in the last 30 days?", "created_at", "created"},
		{"which closed work items of project Alpha were created in the last 30 days?", "created_at", "created"},
		{"which closed work item of project Alpha was created in the last 30 days?", "created_at", "created"},
		{"which work items of project Alpha were closed in the last 30 days?", "completed_at", "completed"},
		{"which work item of project Alpha was closed in the last 30 days?", "completed_at", "completed"},
		{"which work items of project Alpha were created and closed in the last 30 days?", "", "ambiguous"},
		// a past copula reads as the completion event, which may be the
		// period's own role: the reading is asked for, never guessed
		{"which work items of project Alpha that were closed were created in the last 30 days?", "", "ambiguous"},
		{"which work item of project Alpha that was closed was created in the last 30 days?", "", "ambiguous"},
	}
	for _, tc := range cases {
		run := runInterpretedTupleCase(t, serverInterpretedPath, tc.question, prodStatusPeriodFrame(), TimeContext{Axis: TemporalCurrent})
		if run.invokedErr != nil {
			t.Fatalf("%q: %v", tc.question, run.invokedErr)
		}
		if len(run.admissions) != 1 || run.admissions[0].MemberTimeRole != tc.role {
			t.Errorf("%q: admission lines %+v, want one with member_time_role %s", tc.question, run.admissions, tc.role)
		}
		if tc.column == "" {
			if run.result.Status != InvestigationNoMatch || run.reads != 0 || !limitationsContain(run.result.Limitations, memberTimeRoleClarificationLimitation(MemberTimeRoleAmbiguous)) {
				t.Errorf("%q: status=%s reads=%d limitations=%q, want no_match, no read and the two-roles sentence", tc.question, run.result.Status, run.reads, run.result.Limitations)
			}
			continue
		}
		if run.result.Status == InvestigationNoMatch || run.reads != 1 || run.request.Status != "done" || run.request.TimeColumn != tc.column {
			t.Errorf("%q: status=%s reads=%d read status=%q column=%q, want one read on done and %s", tc.question, run.result.Status, run.reads, run.request.Status, run.request.TimeColumn, tc.column)
		}
		if run.request.TimeEnd.Sub(run.request.TimeStart) < 29*24*time.Hour {
			t.Errorf("%q: read window %s..%s, want the stated 30 days", tc.question, run.request.TimeStart, run.request.TimeEnd)
		}
		if !limitationsContain(run.result.Limitations, "(the "+tc.column+" field)") || !limitationsContain(run.result.Limitations, "current status is done") {
			t.Errorf("%q: limitations %q, want the %s period and the done status named", tc.question, run.result.Limitations, tc.column)
		}
		if run.result.Cohort == nil || len(run.result.Cohort.Members) != 1 {
			t.Errorf("%q: cohort %+v, want the one member served", tc.question, run.result.Cohort)
		}
	}
	// No role verb with a period: the answer is the one it was before.
	run := runInterpretedTupleCase(t, serverInterpretedPath, "which work items of project Alpha are in progress in the last 30 days?", inProgressPeriodFrame(), TimeContext{Axis: TemporalCurrent})
	if run.invokedErr != nil || run.result.Status != InvestigationNoMatch || run.reads != 0 || !limitationsContain(run.result.Limitations, memberTimeRoleClarificationLimitation(MemberTimeRoleNoVerb)) {
		t.Errorf("no role verb: err=%v status=%s reads=%d limitations=%q, want the did-not-say sentence", run.invokedErr, run.result.Status, run.reads, run.result.Limitations)
	}
	if len(run.admissions) != 1 || run.admissions[0].MemberFilter != WorkItemMemberFilterWindowRoleUnresolved || run.admissions[0].MemberTimeRole != string(MemberTimeRoleNoVerb) {
		t.Errorf("no role verb: admission lines %+v, want window_role_unresolved and no_verb", run.admissions)
	}
	if strings.Contains(memberTimeRoleClarificationLimitation(MemberTimeRoleNoVerb), "more than one") || !strings.Contains(memberTimeRoleClarificationLimitation(MemberTimeRoleAmbiguous), "more than one") {
		t.Error("the two clarification sentences must differ on whether the question named more than one role")
	}
}

func inProgressPeriodFrame() QuestionFrame {
	frame := prodStatusPeriodFrame()
	frame.SubjectExpression.Scoped.MemberQualifierValue = "in_progress"
	return frame
}

// Every copula or relative-clause shape outside the one status shape fails
// closed: the outcome is ambiguous (the readings are offered), never one bound
// role.
func TestMemberTimeRoleBinderFailsClosedOnUnlistedCopulaShapes(t *testing.T) {
	for _, question := range []string{
		"which work items of project Alpha that were closed were created in the last 30 days?",
		"which work item of project Alpha that was closed was created in the last 30 days?",
		"which work items of project Alpha that are not closed were created in the last 30 days?",
		"which work items of project Alpha that have been closed were created in the last 30 days?",
		"which work item of project Alpha that has been closed was created in the last 30 days?",
		"which work items of project Alpha that were closed by Sam were created in the last 30 days?",
		"which work items of project Alpha that are closed by Sam were created in the last 30 days?",
		"which work items of project Alpha that are assigned to Sam and that are closed were created in the last 30 days?",
		"which work items of project Alpha that are closed and that are new were updated in the last 30 days?",
		"which work items of project Alpha that are being closed were created in the last 30 days?",
		"which work items of project Alpha that are closed now were created in the last 30 days?",
		"which work items of project Alpha that are closed were not created in the last 30 days?",
		"which work items of project Alpha are closed that were created in the last 30 days?",
		"which work items of project Alpha that are closed got created in the last 30 days?",
		"which work items of project Alpha that is closed were reopened in the last 30 days?",
		"which work items of project Alpha that are closed exist in the last 30 days?",
		"is anything that is closed created in the last 30 days?",
		"Sam's closed issues were created in the last 30 days?",
		"which work items of project Alpha that are assigned to Sam and whose status is closed were created in the last 30 days?",
	} {
		outcome := BindMemberTimeRole(question, windowSpanOf(t, question))
		if outcome.Reason != MemberTimeRoleAmbiguous || outcome.Role != "" {
			t.Errorf("%q: reason=%s role=%q, want ambiguous", question, outcome.Reason, outcome.Role)
		}
	}
	for _, question := range []string{
		"which work items of project Alpha that are closed were created in the last 30 days?",
		"which work items of project Alpha which are closed were created in the last 30 days?",
		"which work items of project Alpha whose status is closed were created in the last 30 days?",
		"which work items of project Alpha that are closed or done were created in the last 30 days?",
		"which work items of project Alpha that are already closed were created in the last 30 days?",
		"which work items of project Alpha that are closed and were created in the last 30 days?",
		"which work items of project Alpha that're closed were created in the last 30 days?",
		"which work item of project Alpha that's closed was created in the last 30 days?",
		"which work item of project Alpha that’s closed was created in the last 30 days?",
		"which work item of project Alpha which's closed was created in the last 30 days?",
	} {
		outcome := BindMemberTimeRole(question, windowSpanOf(t, question))
		if outcome.Reason != MemberTimeRoleBound || outcome.Role != MemberTimeRoleCreated {
			t.Errorf("%q: reason=%s role=%q, want bound created", question, outcome.Reason, outcome.Role)
		}
	}
}

func TestWorkItemCurrentFrameWithCallerWindowAndTwoRolesAsksForTheReading(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	run := runTupleFilterCase(t, currentWorkItemFrame(), WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 1}, callerWindowRequest("Which work items in Project Alpha were created and closed?"))
	if run.invokedErr != nil {
		t.Fatal(run.invokedErr)
	}
	if run.reads != 0 || run.result.Status != InvestigationNoMatch || !limitationsContain(run.result.Limitations, memberTimeRoleClarificationLimitation(MemberTimeRoleAmbiguous)) {
		t.Fatalf("reads=%d status=%s limitations=%q, want no read and the two-roles sentence", run.reads, run.result.Status, run.result.Limitations)
	}
	if len(run.admissions) != 1 || run.admissions[0].Admitted || run.admissions[0].MemberFilter != WorkItemMemberFilterWindowRoleUnresolved || run.admissions[0].MemberTimeRole != string(MemberTimeRoleAmbiguous) {
		t.Errorf("admission = %+v, want refused window_role_unresolved ambiguous", run.admissions)
	}
}

func TestWorkItemAdmissionLineNamesTheMemberTimeRoleOnlyFromItsVocabulary(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	run := runTupleFilterCase(t, currentWorkItemFrame(), WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 1}, statedPeriodRequest("Which work items in Project Alpha were closed?"))
	if run.invokedErr != nil {
		t.Fatal(run.invokedErr)
	}
	if len(run.admissions) != 1 || run.admissions[0].MemberTimeRole != WorkItemMemberTimeRoleNotEvaluated {
		t.Errorf("no committed window: admission = %+v, want member_time_role not_evaluated", run.admissions)
	}
	for _, token := range WorkItemMemberTimeRoleVocabulary() {
		if workItemMemberTimeRoleLogValue(token) != token {
			t.Errorf("vocabulary token %q is not logged as itself", token)
		}
	}
	if got := workItemMemberTimeRoleLogValue("closed issues"); got != WorkItemMemberTimeRoleNotEvaluated {
		t.Errorf("a token outside the vocabulary is logged as %q", got)
	}
}
