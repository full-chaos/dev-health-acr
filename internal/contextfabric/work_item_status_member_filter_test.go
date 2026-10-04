package contextfabric

import (
	"context"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func statusQualifiedTupleFrame(qualifier MemberQualifier, value string) QuestionFrame {
	frame := prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate)
	frame.SubjectExpression.Scoped.MemberQualifier = qualifier
	frame.SubjectExpression.Scoped.MemberQualifierValue = value
	return frame
}

func TestWorkItemTupleAdmitsOnlyAStatusQualifierWithAClosedSetValue(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	policy := workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus)
	current := TimeContext{Axis: TemporalCurrent}
	for _, status := range WorkItemStatusVocabulary() {
		frame := statusQualifiedTupleFrame(MemberQualifierStatus, status)
		if got := prospectiveWorkItemTupleAdmission(&frame, policy, current); got != workItemTupleProspective {
			t.Errorf("status %q admission=%d, want prospective", status, got)
		}
		if got := workItemTupleStatusFilter(&frame); got != status {
			t.Errorf("status %q filter=%q", status, got)
		}
		if got := workItemTupleMemberFilterBasis(&frame); got != WorkItemMemberFilterStatus {
			t.Errorf("status %q basis=%q", status, got)
		}
	}
	for _, tc := range []struct {
		name      string
		qualifier MemberQualifier
		value     string
		basis     string
	}{
		{"status without a value", MemberQualifierStatus, "", WorkItemMemberFilterStatusWithoutValue},
		{"status outside the closed set", MemberQualifierStatus, "stuck", WorkItemMemberFilterStatus},
		{"assignee with a value", MemberQualifierAssignee, "alice", WorkItemMemberFilterAssignee},
		{"assignee carrying a closed-set status word", MemberQualifierAssignee, "blocked", WorkItemMemberFilterAssignee},
		{"assignee without a value", MemberQualifierAssignee, "", WorkItemMemberFilterAssignee},
		{"unrecognized qualifier", MemberQualifierUnrecognized, "", WorkItemMemberFilterUnrecognized},
	} {
		frame := statusQualifiedTupleFrame(tc.qualifier, tc.value)
		if got := prospectiveWorkItemTupleAdmission(&frame, policy, current); got != workItemTupleRefused {
			t.Errorf("%s: admission=%d, want refused", tc.name, got)
		}
		if got := workItemTupleStatusFilter(&frame); got != "" {
			t.Errorf("%s: a refused frame must carry no filter, got %q", tc.name, got)
		}
		if got := workItemTupleMemberFilterBasis(&frame); got != tc.basis {
			t.Errorf("%s: basis=%q, want %q", tc.name, got, tc.basis)
		}
		if got := prospectiveWorkItemTupleAdmission(&frame, policy, current).refusalBasis(); got != contractsv1.ContextFabricRefusalBasisMemberKindUnservable {
			t.Errorf("%s: refusal basis %q", tc.name, got)
		}
	}
	// A status qualifier does not widen the time axis: status is current-only.
	frame := statusQualifiedTupleFrame(MemberQualifierStatus, "blocked")
	if got := prospectiveWorkItemTupleAdmission(&frame, policy, TimeContext{Axis: TemporalValidTime}); got != workItemTupleRefused {
		t.Errorf("status qualifier on a historical axis admission=%d, want refused", got)
	}
	frame.Temporal = TemporalIntentBoundedWindow
	if got := prospectiveWorkItemTupleAdmission(&frame, policy, current); got != workItemTupleRefused {
		t.Errorf("status qualifier on a bounded-window frame admission=%d, want refused", got)
	}
}

func TestWorkItemStatusQualifiedFrameIsAStoredTupleButNeverReused(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	frame := statusQualifiedTupleFrame(MemberQualifierStatus, "blocked")
	state := &PersistedSemanticState{Family: QuestionFamilyScopedCohortStatus, FramePresent: true, Frame: &frame, ScopeAnchor: SemanticScopeAnchor{Kind: SubjectProject}}
	if !workItemTupleSemanticState(state) {
		t.Fatal("a status-qualified tuple is not classified as a work-item tuple, so a stored answer would be served without its census disclosure")
	}
	assignee := statusQualifiedTupleFrame(MemberQualifierAssignee, "alice")
	state.Frame = &assignee
	if workItemTupleSemanticState(state) {
		t.Fatal("an assignee-qualified frame classified as a work-item tuple")
	}
	telemetry := &reuseDecisionTelemetry{recordingTelemetry: &recordingTelemetry{}}
	engine := &Engine{telemetry: telemetry}
	state.Frame = &frame
	_, hit, err := engine.tryReuseWorkItemTuple(context.Background(), storage.Principal{OrgID: "org-1"}, InvestigationRequest{}, ResolvedGraphBinding{}, StoredInvestigationResult{SemanticState: state, SemanticStateRead: SemanticStateReadAvailable}, WorkItemTupleClassification{Disposition: WorkItemTupleEligible})
	if hit || err != nil {
		t.Fatalf("a status-filtered stored answer was reused: hit=%v err=%v", hit, err)
	}
	if len(telemetry.decisions) != 1 || telemetry.decisions[0] != "member_filter_not_reusable" {
		t.Fatalf("reuse decisions = %v, want the member filter named as the reason", telemetry.decisions)
	}
}

type reuseDecisionTelemetry struct {
	*recordingTelemetry
	decisions []string
}

func (r *reuseDecisionTelemetry) RecordWorkItemReuse(_ context.Context, _ storage.Principal, event WorkItemReuseEvent) {
	r.decisions = append(r.decisions, event.Decision)
}

type statusFilterRun struct {
	result      InvestigationResult
	request     WorkItemMembershipRequest
	reads       int
	factReads   int
	graph       *dispatchGraphProbe
	admissions  []WorkItemTupleAdmissionEvent
	invokedErr  error
	savedCensus *WorkItemTupleCensus
}

func runStatusFilterTuple(t *testing.T, frame QuestionFrame, population int) statusFilterRun {
	t.Helper()
	return runStatusFilterTupleCensus(t, frame, WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: population})
}

func runStatusFilterTupleCensus(t *testing.T, frame QuestionFrame, census WorkItemMembershipCensus) statusFilterRun {
	t.Helper()
	return runTupleFilterCase(t, frame, census, validInvestigationRequestWithConfirmedWindow())
}

func runTupleFilterCase(t *testing.T, frame QuestionFrame, census WorkItemMembershipCensus, investigation InvestigationRequest) statusFilterRun {
	t.Helper()
	population := census.AuthorizedPopulation
	var run statusFilterRun
	frame = ValidateFrame(frame, nil, "").Frame
	payload := workItemTuplePayloadFixture(t)
	run.graph = &dispatchGraphProbe{graphReaderStub: graphReaderStub{resolution: payload.SubjectResolution, bases: provenCommitBases(payload.SubjectResolution.Committed...)}}
	telemetry := &recordingTelemetry{}
	store := &staticResultStore{results: map[string]InvestigationResult{}}
	gate, _ := NewWorkItemMembershipGate(1, 0)
	outcome := QuestionFamilyOutcome{Family: QuestionFamilyScopedCohortStatus, Source: QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: workItemTupleFrameGate(DecideFrameGate(ValidateFrame(frame, nil, ""), true), &frame, workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus), TimeContext{Axis: TemporalCurrent}), WinningSample: FamilySample{ScopeAnchorKind: SubjectProject, ScopeAnchorTerm: "Project"}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactHealth}}}, outcome: outcome},
		Graph:       run.graph,
		CandidateVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, SubjectKind, string) (bool, CandidateVerificationReason) {
			return true, ""
		},
		WorkItemMembership: tupleMembershipFunc(func(ctx context.Context, _ storage.Principal, request WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error) {
			run.reads++
			run.request = request
			lease, err := gate.Acquire(ctx)
			m := WorkItemMembershipResult{Census: census}
			if population > 0 {
				m.Members = []WorkItemMembershipMember{{CanonicalID: payload.Cohort.Members[0].Subject.CanonicalID, WorkItemID: "work-1"}}
			}
			return lease, m, err
		}),
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			run.factReads++
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1"}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{Status: InvestigationComplete, DirectJudgment: "Available work items.", CurrentState: "Available work items.", DeterministicAnswer: "Available work items.", StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{}, ClaimedFacts: []ClaimedFact{}, Warnings: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Versions: VersionSet{Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1"}}, nil
		}), Results: store, Requirements: registryDeriver{}, Telemetry: telemetry,
	}, EngineOptions{ServiceVersion: "test", NewResultID: func() string { return "result_status_filter_001" }})
	if err != nil {
		t.Fatal(err)
	}
	run.result, run.invokedErr = engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1"}, investigation)
	run.admissions = telemetry.workItemTupleAdmissions
	if store.savedSemantic != nil && store.savedSemantic.State != nil {
		run.savedCensus = store.savedSemantic.State.WorkItemCensus
	}
	return run
}

func limitationsContain(limitations []string, fragment string) bool {
	for _, limitation := range limitations {
		if strings.Contains(limitation, fragment) {
			return true
		}
	}
	return false
}

func TestWorkItemStatusQualifiedTupleReadsMembersFilteredAndDisclosesIt(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	run := runStatusFilterTuple(t, statusQualifiedTupleFrame(MemberQualifierStatus, "blocked"), 1)
	if run.invokedErr != nil {
		t.Fatal(run.invokedErr)
	}
	if run.reads != 1 || run.request.Status != "blocked" {
		t.Fatalf("membership reads=%d status=%q, want one read carrying blocked", run.reads, run.request.Status)
	}
	if run.factReads != 1 || run.result.Cohort == nil || len(run.result.Cohort.Members) != 1 {
		t.Fatalf("fact reads=%d cohort=%+v", run.factReads, run.result.Cohort)
	}
	if !limitationsContain(run.result.Limitations, workItemStatusFilterDisclosure("blocked")) {
		t.Fatalf("the served members carry no filter disclosure: %v", run.result.Limitations)
	}
	if limitationsContain(run.result.Limitations, "No work item in this project") {
		t.Fatalf("a non-empty match set claimed no match: %v", run.result.Limitations)
	}
	if len(run.admissions) != 1 || !run.admissions[0].Admitted || run.admissions[0].MemberFilter != WorkItemMemberFilterStatus {
		t.Fatalf("settled admission = %+v, want admitted with member_filter=status", run.admissions)
	}
	if err := ValidateWorkItemTuplePayload(run.result, storage.Principal{OrgID: "org-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkItemStatusQualifiedTupleWithNoMatchIsANamedResult(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	run := runStatusFilterTuple(t, statusQualifiedTupleFrame(MemberQualifierStatus, "blocked"), 0)
	if run.invokedErr != nil {
		t.Fatal(run.invokedErr)
	}
	if run.factReads != 0 || run.result.Cohort == nil || len(run.result.Cohort.Members) != 0 {
		t.Fatalf("fact reads=%d cohort=%+v", run.factReads, run.result.Cohort)
	}
	if !limitationsContain(run.result.Limitations, workItemStatusNoMatchDisclosure("blocked")) {
		t.Fatalf("an empty match set is not named: %v", run.result.Limitations)
	}
	if run.savedCensus == nil || run.savedCensus.State != WorkItemMembershipCensusExact || run.savedCensus.Value != 0 {
		t.Fatalf("saved census = %+v, want exact zero", run.savedCensus)
	}
}

func TestWorkItemUnqualifiedTupleReadsNoStatusAndCarriesNoFilterDisclosure(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	run := runStatusFilterTuple(t, prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate), 0)
	if run.invokedErr != nil {
		t.Fatal(run.invokedErr)
	}
	if run.request.Status != "" || limitationsContain(run.result.Limitations, "current status is") || limitationsContain(run.result.Limitations, "No work item in this project") {
		t.Fatalf("an unqualified read carried a filter: status=%q limitations=%v", run.request.Status, run.result.Limitations)
	}
	if len(run.admissions) != 1 || run.admissions[0].MemberFilter != WorkItemMemberFilterWindowNotApplied {
		t.Fatalf("settled admission = %+v, want member_filter=window_not_applied (the request committed a window the membership did not use)", run.admissions)
	}
}

func TestWorkItemAssigneeQualifiedTupleStaysRefusedBeforeAnyRead(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	run := runStatusFilterTuple(t, statusQualifiedTupleFrame(MemberQualifierAssignee, "alice"), 1)
	if run.reads != 0 || run.factReads != 0 || run.graph.resolveCalls != 0 || run.graph.discoverCalls != 0 {
		t.Fatalf("an assignee-qualified tuple performed I/O: membership=%d facts=%d resolve=%d discover=%d", run.reads, run.factReads, run.graph.resolveCalls, run.graph.discoverCalls)
	}
	if len(run.admissions) != 1 || run.admissions[0].Admitted || run.admissions[0].MemberFilter != WorkItemMemberFilterAssignee {
		t.Fatalf("settled admission = %+v, want refused with member_filter=assignee", run.admissions)
	}
	if run.result.Cohort != nil && len(run.result.Cohort.Members) != 0 {
		t.Fatalf("assignee-qualified tuple served members: %+v", run.result.Cohort)
	}
}

func TestWorkItemNoMatchDisclosureNeedsAnExactZeroCensus(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, tc := range []struct {
		name   string
		census *WorkItemTupleCensus
		want   bool
	}{
		{"exact zero", &WorkItemTupleCensus{State: WorkItemMembershipCensusExact, Value: 0}, true},
		{"exact non-zero", &WorkItemTupleCensus{State: WorkItemMembershipCensusExact, Value: 2}, false},
		{"floor with no counted value", &WorkItemTupleCensus{State: WorkItemMembershipCensusFloor, Value: 0}, false},
		{"unmeasured", &WorkItemTupleCensus{State: WorkItemMembershipCensusUnmeasured, Value: 0}, false},
		{"exact zero with a denied partition", &WorkItemTupleCensus{State: WorkItemMembershipCensusExact, Value: 0, gap: &workItemAuthorizationGap{}}, false},
		{"no census", nil, false},
	} {
		got := withWorkItemMemberFilterLimitations(InvestigationResult{}, workItemMemberFilter{Status: "blocked"}, tc.census)
		if has := limitationsContain(got.Limitations, workItemStatusNoMatchDisclosure("blocked")); has != tc.want {
			t.Errorf("%s: no-match disclosure present=%v, want %v", tc.name, has, tc.want)
		}
		if !limitationsContain(got.Limitations, workItemStatusFilterDisclosure("blocked")) {
			t.Errorf("%s: filter disclosure missing", tc.name)
		}
	}
}

func TestWorkItemStatusFilteredReadNeverDisclosesTheDeniedPartition(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, tc := range []struct {
		name       string
		authorized int
		denied     int
	}{{"partially denied", 2, 7}, {"all denied", 0, 7}, {"none denied", 2, 0}} {
		census := WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: tc.authorized, DeniedPopulation: tc.denied, CappedPopulation: tc.authorized + tc.denied}
		run := runStatusFilterTupleCensus(t, statusQualifiedTupleFrame(MemberQualifierStatus, "blocked"), census)
		if run.invokedErr != nil {
			t.Fatalf("%s: %v", tc.name, run.invokedErr)
		}
		if hasWorkItemAuthorizationGapLimitation(run.result.Limitations) {
			t.Errorf("%s: a filtered read disclosed the denied partition, which would give the denied items' status distribution: %v", tc.name, run.result.Limitations)
		}
		for _, limitation := range run.result.Limitations {
			if strings.Contains(limitation, "7") {
				t.Errorf("%s: the denied count reached the answer: %q", tc.name, limitation)
			}
		}
		if run.result.Status == InvestigationDegraded {
			t.Errorf("%s: a filtered read degraded on a denied partition", tc.name)
		}
		if !limitationsContain(run.result.Limitations, workItemStatusDeniedExclusion) {
			t.Errorf("%s: the fixed exclusion statement is missing: %v", tc.name, run.result.Limitations)
		}
	}
	// The output must not depend on the denied partition at all.
	a := runStatusFilterTupleCensus(t, statusQualifiedTupleFrame(MemberQualifierStatus, "blocked"), WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 0, DeniedPopulation: 7})
	b := runStatusFilterTupleCensus(t, statusQualifiedTupleFrame(MemberQualifierStatus, "blocked"), WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 0, DeniedPopulation: 0})
	if strings.Join(a.result.Limitations, "|") != strings.Join(b.result.Limitations, "|") || a.result.Status != b.result.Status {
		t.Errorf("filtered answer differs with the denied count: %v / %v", a.result.Limitations, b.result.Limitations)
	}
}

func TestWorkItemUnfilteredReadStillDisclosesTheDeniedPartition(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	census := WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 2, DeniedPopulation: 7, CappedPopulation: 9}
	run := runStatusFilterTupleCensus(t, prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate), census)
	if run.invokedErr != nil || !hasWorkItemAuthorizationGapLimitation(run.result.Limitations) {
		t.Fatalf("the unfiltered partition disclosure was lost: err=%v %v", run.invokedErr, run.result.Limitations)
	}
}
