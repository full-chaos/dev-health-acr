package contextfabric

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func periodTupleFrame() QuestionFrame {
	frame := prospectiveTupleFrame(GoalAssessState, GoalCountOrAggregate)
	frame.Temporal = TemporalIntentBoundedWindow
	return frame
}

func statedPeriodRequest(question string) InvestigationRequest {
	request := validInvestigationRequest()
	request.Question = question
	return request
}

func TestMemberTimeRoleRegistryBindsEachFormAndRejectsLookalikes(t *testing.T) {
	forms := map[MemberTimeRole][]string{
		MemberTimeRoleCreated:   {"created", "opened", "filed", "raised", "added", "new"},
		MemberTimeRoleCompleted: {"completed", "finished", "closed", "done", "resolved", "shipped", "delivered"},
		MemberTimeRoleUpdated:   {"updated", "changed", "modified", "touched"},
	}
	for role, words := range forms {
		for _, word := range words {
			for _, text := range []string{word, strings.ToUpper(word), strings.Title(word)} {
				question := "Which work items were " + text + " in the last 30 days?"
				outcome := BindMemberTimeRole(question, windowSpanOf(t, question))
				if outcome.Reason != MemberTimeRoleBound || outcome.Role != role {
					t.Errorf("%q: reason=%s role=%q, want bound %q", question, outcome.Reason, outcome.Role, role)
				}
			}
		}
	}
	for _, question := range []string{
		"Which newly filed-away items exist in the last 30 days?",
		"Which closed-source items exist in the last 30 days?",
		"Which unclosed items exist in the last 30 days?",
		"Which items are newly tracked in the last 30 days?",
		"Which items are undone in the last 30 days?",
		"What is the state of work items in the last 30 days?",
		"Which items were active in the last 30 days?",
		"Which items were in progress in the last 30 days?",
	} {
		outcome := BindMemberTimeRole(question, windowSpanOf(t, question))
		if outcome.Reason != MemberTimeRoleNoVerb || outcome.Role != "" {
			t.Errorf("%q: reason=%s role=%q, want no verb", question, outcome.Reason, outcome.Role)
		}
	}
}

func windowSpanOf(t *testing.T, question string) BoundWindowSpan {
	t.Helper()
	spans := BindWindowSpans(question)
	if len(spans) != 1 {
		t.Fatalf("%q: window spans = %d, want 1", question, len(spans))
	}
	return spans[0]
}

func TestMemberTimeRoleClauseAndAmbiguity(t *testing.T) {
	cases := []struct {
		question string
		reason   MemberTimeRoleReason
		role     MemberTimeRole
	}{
		{"Items created and closed in the last 30 days?", MemberTimeRoleAmbiguous, ""},
		{"Items created or opened in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCreated},
		{"Closed items are listed elsewhere. Which were created in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCreated},
		{"Which were created in the last 30 days? Closed ones too.", MemberTimeRoleBound, MemberTimeRoleCreated},
		{"Which items in the last 30 days? Created ones.", MemberTimeRoleNoVerb, ""},
		{"Among previously created work items, which were closed in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCompleted},
		{"Which work items were created, closed in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCompleted},
		{"Which pré-closed work items were created in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCreated},
		{"Which work items were created in the last 30 days, post-closed?", MemberTimeRoleBound, MemberTimeRoleCreated},
		{"Which done-for-you items were created in the last 30 days?", MemberTimeRoleBound, MemberTimeRoleCreated},
	}
	for _, tc := range cases {
		outcome := BindMemberTimeRole(tc.question, windowSpanOf(t, tc.question))
		if outcome.Reason != tc.reason || outcome.Role != tc.role {
			t.Errorf("%q: reason=%s role=%q, want %s %q", tc.question, outcome.Reason, outcome.Role, tc.reason, tc.role)
		}
	}
}

func TestMemberTimeRoleBinderCarriesOffsetsNeverText(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(BoundMemberTimeRole{}), reflect.TypeOf(MemberTimeRoleOutcome{})} {
		for i := 0; i < typ.NumField(); i++ {
			if typ.Field(i).Type.Kind() == reflect.String && typ.Field(i).Type.Name() == "string" && typ.Field(i).Name != "Grammar" {
				t.Errorf("%s.%s is a bare string: the binder must carry offsets, not question text", typ.Name(), typ.Field(i).Name)
			}
		}
	}
	question := "Which items were CLOSED in the last 30 days?"
	outcome := BindMemberTimeRole(question, windowSpanOf(t, question))
	if len(outcome.Bound) != 1 || strings.ToLower(question[outcome.Bound[0].SpanStart:outcome.Bound[0].SpanEnd]) != "closed" {
		t.Fatalf("offsets do not locate the form: %+v", outcome.Bound)
	}
}

func TestWorkItemWindowAdmission(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	policy := workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus)
	current := TimeContext{Axis: TemporalCurrent}
	committed := workItemTupleWindowBasis{Committed: true, Role: MemberTimeRoleCreated, RoleReason: MemberTimeRoleBound}
	frame := periodTupleFrame()
	if got := prospectiveWorkItemWindowAdmission(&frame, policy, current, committed); got != workItemTupleProspective {
		t.Fatalf("committed window with one role: admission=%d", got)
	}
	for _, tc := range []struct {
		name    string
		mutate  func(*QuestionFrame)
		axis    TemporalAxis
		basis   workItemTupleWindowBasis
		wantTok string
	}{
		{"no committed window", nil, TemporalCurrent, workItemTupleWindowBasis{Role: MemberTimeRoleCreated}, WorkItemMemberFilterWindowNotServed},
		{"role missing", nil, TemporalCurrent, workItemTupleWindowBasis{Committed: true, RoleReason: MemberTimeRoleNoVerb}, WorkItemMemberFilterWindowRoleUnresolved},
		{"role ambiguous", nil, TemporalCurrent, workItemTupleWindowBasis{Committed: true, RoleReason: MemberTimeRoleAmbiguous}, WorkItemMemberFilterWindowRoleUnresolved},
		{"historical axis needs status history", nil, TemporalValidTime, committed, WorkItemMemberFilterWindowNotServed},
		{"period comparison", func(f *QuestionFrame) { f.Temporal = TemporalIntentPeriodComparison }, TemporalCurrent, committed, WorkItemMemberFilterWindowNotServed},
		{"time series", func(f *QuestionFrame) { f.Temporal = TemporalIntentTimeSeries }, TemporalCurrent, committed, WorkItemMemberFilterWindowNotServed},
		{"assignee qualifier", func(f *QuestionFrame) {
			f.SubjectExpression.Scoped.MemberQualifier = MemberQualifierAssignee
			f.SubjectExpression.Scoped.MemberQualifierValue = "alice"
		}, TemporalCurrent, committed, WorkItemMemberFilterWindowNotServed},
		{"ordering requested", func(f *QuestionFrame) {
			f.Goals = []InvestigationGoal{GoalRankOrSurvey}
			f.Emphasis = []AnswerEmphasis{EmphasisPositiveOutliers}
		}, TemporalCurrent, committed, WorkItemMemberFilterWindowNotServed},
	} {
		f := periodTupleFrame()
		if tc.mutate != nil {
			tc.mutate(&f)
		}
		got := prospectiveWorkItemWindowAdmission(&f, policy, TimeContext{Axis: tc.axis}, tc.basis)
		if f.Temporal == TemporalIntentBoundedWindow && got != workItemTupleRefused {
			t.Errorf("%s: admission=%d, want refused", tc.name, got)
		}
		if f.Temporal != TemporalIntentBoundedWindow && got != workItemTupleNotApplicable {
			t.Errorf("%s: a non-window intent reached the window arm: %d", tc.name, got)
		}
		if tok := workItemWindowFilterBasis(&f, policy, TimeContext{Axis: tc.axis}, tc.basis); tok != tc.wantTok {
			t.Errorf("%s: token=%q want %q", tc.name, tok, tc.wantTok)
		}
	}
	// A frame that is current is never routed through the window arm.
	current2 := prospectiveTupleFrame(GoalAssessState)
	if got := prospectiveWorkItemWindowAdmission(&current2, policy, current, committed); got != workItemTupleNotApplicable {
		t.Errorf("current frame admission=%d", got)
	}
}

func TestWorkItemWindowBasisFollowsTheServersCommittedWindowOnly(t *testing.T) {
	stated := requestWindowCanonicalization{BinderProposal: ProposeWindowFromSpans("Which items were created in the last 30 days?")}
	if b := deriveWorkItemTupleWindowBasis("Which items were created in the last 30 days?", stated, false); !b.Committed || b.Role != MemberTimeRoleCreated {
		t.Errorf("stated trailing window: %+v", b)
	}
	// A bare calendar period is a proposal, never a commitment.
	bare := "Which items were created last month?"
	if b := deriveWorkItemTupleWindowBasis(bare, requestWindowCanonicalization{BinderProposal: ProposeWindowFromSpans(bare)}, false); b.Committed || b.Role != "" {
		t.Errorf("bare last month committed: %+v", b)
	}
	none := "Which items were created?"
	if b := deriveWorkItemTupleWindowBasis(none, requestWindowCanonicalization{BinderProposal: ProposeWindowFromSpans(none)}, false); b.Committed {
		t.Errorf("no window committed: %+v", b)
	}
	explicit := requestWindowCanonicalization{Effective: validEffectiveWindowForTest(t)}
	if b := deriveWorkItemTupleWindowBasis("Which items were closed?", explicit, false); !b.Committed || b.Role != MemberTimeRoleCompleted {
		t.Errorf("caller window with the verb elsewhere in the question: %+v", b)
	}
}

func validEffectiveWindowForTest(t *testing.T) *EffectiveEvidenceWindow {
	t.Helper()
	start, end, ok := relativeWindowBounds(RelativeWindowTrailing30D, time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("no bounds")
	}
	return &EffectiveEvidenceWindow{Start: &start, End: &end, RelativeID: RelativeWindowTrailing30D, Provenance: WindowQuestionStated}
}

func TestWorkItemWindowTupleReadsTheBoundTimeFieldOverTheDisclosedWindow(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, tc := range []struct {
		question string
		column   string
		field    string
	}{
		{"Which work items in Project Alpha were created in the last 30 days?", "created_at", "created"},
		{"Which work items in Project Alpha were closed in the last 30 days?", "completed_at", "completed"},
		{"Which work items in Project Alpha were touched in the last 30 days?", "updated_at", "last updated"},
	} {
		run := runTupleFilterCase(t, periodTupleFrame(), WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 1}, statedPeriodRequest(tc.question))
		if run.invokedErr != nil {
			t.Fatalf("%s: %v", tc.question, run.invokedErr)
		}
		if run.reads != 1 || run.request.TimeColumn != tc.column {
			t.Fatalf("%s: reads=%d column=%q, want one read on %s", tc.question, run.reads, run.request.TimeColumn, tc.column)
		}
		window := run.result.EffectiveEvidenceWindow
		if window == nil || window.Start == nil || window.End == nil || !window.Start.Truncate(time.Microsecond).Equal(run.request.TimeStart) || !window.End.Truncate(time.Microsecond).Equal(run.request.TimeEnd) {
			t.Fatalf("%s: the read window %v..%v is not the disclosed window %+v", tc.question, run.request.TimeStart, run.request.TimeEnd, window)
		}
		if run.request.Status != "" {
			t.Errorf("%s: a window read carried a status %q", tc.question, run.request.Status)
		}
		if !limitationsContain(run.result.Limitations, "Members are the work items "+tc.field+" from ") || !limitationsContain(run.result.Limitations, tc.column) {
			t.Errorf("%s: no window disclosure naming the field: %v", tc.question, run.result.Limitations)
		}
		if limitationsContain(run.result.Limitations, "current status is") {
			t.Errorf("%s: a status disclosure on a window-only read", tc.question)
		}
		if len(run.admissions) != 1 || !run.admissions[0].Admitted || run.admissions[0].MemberFilter != WorkItemMemberFilterWindow {
			t.Fatalf("%s: settled admission = %+v", tc.question, run.admissions)
		}
		if run.factReads != 1 || run.result.Cohort == nil || len(run.result.Cohort.Members) != 1 {
			t.Errorf("%s: fact reads=%d cohort=%+v", tc.question, run.factReads, run.result.Cohort)
		}
	}
}

func TestWorkItemWindowTupleWithNoMatchIsANamedResult(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	run := runTupleFilterCase(t, periodTupleFrame(), WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true}, statedPeriodRequest("Which work items in Project Alpha were closed in the last 30 days?"))
	if run.invokedErr != nil {
		t.Fatal(run.invokedErr)
	}
	if run.factReads != 0 || run.result.Cohort == nil || len(run.result.Cohort.Members) != 0 {
		t.Fatalf("fact reads=%d cohort=%+v", run.factReads, run.result.Cohort)
	}
	if !limitationsContain(run.result.Limitations, "No work item in this project within the authorized scope was completed in that period") {
		t.Fatalf("an empty window is not named: %v", run.result.Limitations)
	}
	if run.savedCensus == nil || run.savedCensus.State != WorkItemMembershipCensusExact || run.savedCensus.Value != 0 {
		t.Fatalf("saved census = %+v", run.savedCensus)
	}
}

func TestWorkItemWindowAndStatusFiltersCompose(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	frame := periodTupleFrame()
	frame.SubjectExpression.Scoped.MemberQualifier = MemberQualifierStatus
	frame.SubjectExpression.Scoped.MemberQualifierValue = "blocked"
	run := runTupleFilterCase(t, frame, WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true}, statedPeriodRequest("Which blocked work items in Project Alpha were created in the last 30 days?"))
	if run.invokedErr != nil {
		t.Fatal(run.invokedErr)
	}
	if run.request.Status != "blocked" || run.request.TimeColumn != "created_at" {
		t.Fatalf("request = %+v, want status and window together", run.request)
	}
	if !limitationsContain(run.result.Limitations, "and a current status of blocked") {
		t.Errorf("the combined empty result does not name both filters: %v", run.result.Limitations)
	}
}

func TestWorkItemWindowWithoutOneReadingOffersTheThreeAndReadsNothing(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, question := range []string{
		"Which work items in Project Alpha are there in the last 30 days?",
		"Which work items in Project Alpha were created and closed in the last 30 days?",
	} {
		run := runTupleFilterCase(t, periodTupleFrame(), WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 1}, statedPeriodRequest(question))
		if run.invokedErr != nil {
			t.Fatalf("%s: %v", question, run.invokedErr)
		}
		if run.reads != 0 || run.factReads != 0 || run.graph.resolveCalls != 0 {
			t.Fatalf("%s: a clarification performed I/O: membership=%d facts=%d resolve=%d", question, run.reads, run.factReads, run.graph.resolveCalls)
		}
		if !limitationsContain(run.result.Limitations, memberTimeRoleClarificationLimitation()) {
			t.Fatalf("%s: the three readings are not offered: %v", question, run.result.Limitations)
		}
		for _, limitation := range run.result.Limitations {
			if strings.Contains(limitation, "Project Alpha") || (limitation != memberTimeRoleClarificationLimitation() && strings.Contains(limitation, "30 days")) {
				t.Errorf("%s: question text reached the answer: %q", question, limitation)
			}
		}
		if run.result.Cohort != nil && len(run.result.Cohort.Members) != 0 {
			t.Errorf("%s: members were served", question)
		}
		if len(run.admissions) != 1 || run.admissions[0].Admitted || run.admissions[0].MemberFilter != WorkItemMemberFilterWindowRoleUnresolved {
			t.Errorf("%s: settled admission = %+v", question, run.admissions)
		}
	}
	// The sentence names every reading from the closed vocabulary, and nothing else.
	sentence := memberTimeRoleClarificationLimitation()
	for _, role := range MemberTimeRoleVocabulary() {
		if !strings.Contains(sentence, string(role)) {
			t.Errorf("the clarification does not offer %q: %s", role, sentence)
		}
	}
}

func TestWorkItemWindowThatNeedsStatusHistoryStaysRefusedAndNamed(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	for _, tc := range []struct {
		name     string
		question string
		axis     TemporalAxis
	}{
		{"historical axis", "Which work items in Project Alpha were in progress last March?", TemporalValidTime},
		{"no stated window", "Which work items in Project Alpha were created in March?", TemporalCurrent},
		{"active needs history", "Which work items in Project Alpha were active in the last 30 days?", TemporalCurrent},
		{"no verb on a historical axis", "Which work items in Project Alpha were there in the last 30 days?", TemporalValidTime},
		{"assignee has no source", "Which work items assigned to alice in Project Alpha were created in the last 30 days?", TemporalCurrent},
	} {
		request := statedPeriodRequest(tc.question)
		request.TimeContext.Axis = tc.axis
		frame := periodTupleFrame()
		if tc.name == "assignee has no source" {
			frame.SubjectExpression.Scoped.MemberQualifier = MemberQualifierAssignee
			frame.SubjectExpression.Scoped.MemberQualifierValue = "alice"
		}
		run := runTupleFilterCase(t, frame, WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 1}, request)
		if run.reads != 0 || run.factReads != 0 || run.graph.resolveCalls != 0 {
			t.Errorf("%s: performed I/O: membership=%d facts=%d resolve=%d", tc.name, run.reads, run.factReads, run.graph.resolveCalls)
		}
		if run.result.Cohort != nil && len(run.result.Cohort.Members) != 0 {
			t.Errorf("%s: served members", tc.name)
		}
		if len(run.admissions) == 1 && run.admissions[0].Admitted {
			t.Errorf("%s: admitted", tc.name)
		}
		if tc.name != "active needs history" && limitationsContain(run.result.Limitations, memberTimeRoleClarificationLimitation()) {
			t.Errorf("%s: a refusal that is not about the verb offered the three readings", tc.name)
		}
		want := WorkItemMemberFilterWindowNotServed
		if tc.name == "active needs history" {
			// "active" is not a registry verb, so it is never read as updated_at;
			// the caller is offered the three readings and chooses one.
			want = WorkItemMemberFilterWindowRoleUnresolved
			if !limitationsContain(run.result.Limitations, memberTimeRoleClarificationLimitation()) {
				t.Errorf("%s: the readings were not offered", tc.name)
			}
		}
		if len(run.admissions) == 1 && run.admissions[0].MemberFilter != want {
			t.Errorf("%s: member_filter=%q, want %q", tc.name, run.admissions[0].MemberFilter, want)
		}
	}
}

func TestWorkItemWindowReadNeverDisclosesTheDeniedPartition(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	census := WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 0, DeniedPopulation: 7, CappedPopulation: 7}
	run := runTupleFilterCase(t, periodTupleFrame(), census, statedPeriodRequest("Which work items in Project Alpha were created in the last 30 days?"))
	if run.invokedErr != nil {
		t.Fatal(run.invokedErr)
	}
	if hasWorkItemAuthorizationGapLimitation(run.result.Limitations) || run.result.Status == InvestigationDegraded {
		t.Fatalf("a windowed read disclosed or degraded on the denied partition: %v", run.result.Limitations)
	}
	if !limitationsContain(run.result.Limitations, workItemStatusDeniedExclusion) {
		t.Fatalf("fixed exclusion missing: %v", run.result.Limitations)
	}
}

func TestWorkItemPeriodStoredAnswerIsATupleButNeverReused(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	frame := periodTupleFrame()
	state := &PersistedSemanticState{Family: QuestionFamilyScopedCohortStatus, FramePresent: true, Frame: &frame, ScopeAnchor: SemanticScopeAnchor{Kind: SubjectProject}}
	if !workItemTupleSemanticState(state) {
		t.Fatal("a period tuple is not classified as a work-item tuple, so a stored answer would be served without its census")
	}
	telemetry := &reuseDecisionTelemetry{recordingTelemetry: &recordingTelemetry{}}
	engine := &Engine{telemetry: telemetry}
	_, hit, err := engine.tryReuseWorkItemTuple(context.Background(), storage.Principal{OrgID: "org-1"}, InvestigationRequest{}, ResolvedGraphBinding{}, StoredInvestigationResult{SemanticState: state, SemanticStateRead: SemanticStateReadAvailable}, WorkItemTupleClassification{Disposition: WorkItemTupleEligible})
	if hit || err != nil || len(telemetry.decisions) != 1 || telemetry.decisions[0] != "member_filter_not_reusable" {
		t.Fatalf("hit=%v err=%v decisions=%v", hit, err, telemetry.decisions)
	}
}

func TestWorkItemRememberedWindowIsNotCommitted(t *testing.T) {
	explicit := requestWindowCanonicalization{Effective: validEffectiveWindowForTest(t)}
	if b := deriveWorkItemTupleWindowBasis("Which items were closed?", explicit, true); b.Committed || b.Role != "" {
		t.Errorf("a remembered window satisfied the gate: %+v", b)
	}
}

func TestWorkItemWindowFilterBasisNamesComparisonAndSeriesAsNotServed(t *testing.T) {
	policy := workItemTupleFamilyPolicyForTest(QuestionFamilyScopedCohortStatus)
	committed := workItemTupleWindowBasis{Committed: true, Role: MemberTimeRoleCreated, RoleReason: MemberTimeRoleBound}
	for _, intent := range []TemporalIntent{TemporalIntentPeriodComparison, TemporalIntentTimeSeries} {
		frame := prospectiveTupleFrame(GoalAssessState)
		frame.Temporal = intent
		if token := workItemTupleMemberFilterToken(&frame, policy, TimeContext{Axis: TemporalCurrent}, committed); token != WorkItemMemberFilterWindowNotServed {
			t.Errorf("%s frame token = %q, want %q", intent, token, WorkItemMemberFilterWindowNotServed)
		}
	}
	current := prospectiveTupleFrame(GoalAssessState)
	if token := workItemTupleMemberFilterToken(&current, policy, TimeContext{Axis: TemporalCurrent}, committed); token != WorkItemMemberFilterNone {
		t.Errorf("a current frame token = %q", token)
	}
}

func TestWorkItemWindowDisclosuresSurviveAFullLimitationList(t *testing.T) {
	filter := workItemMemberFilter{Status: "blocked", TimeRole: MemberTimeRoleCreated, Start: time.Date(2026, 9, 4, 0, 0, 0, 123456000, time.UTC), End: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)}
	for _, withStatus := range []bool{false, true} {
		f := filter
		if !withStatus {
			f.Status = ""
		}
		var model []string
		for i := 0; i < contractsv1.ContextFabricLimitationsMaxCount; i++ {
			model = append(model, fmt.Sprintf("model caveat %03d", i))
		}
		census := &WorkItemTupleCensus{State: WorkItemMembershipCensusExact, Value: 0}
		got := withWorkItemMemberFilterLimitations(InvestigationResult{Limitations: model}, f, census)
		wants := []string{workItemWindowFilterDisclosure(f), workItemStatusDeniedExclusion, workItemMemberFilterNoMatchDisclosure(f)}
		if withStatus {
			wants = append(wants, workItemStatusFilterDisclosure("blocked"))
		}
		for _, want := range wants {
			if !contractsv1.IsContextFabricServiceAuthoredLimitation(want) {
				t.Errorf("%q is not recognised as service-authored, so a full list would displace it", want)
			}
			if !limitationsContain(got.Limitations, want) {
				t.Errorf("a disclosure was displaced from a full list: %q", want)
			}
		}
		if len(got.Limitations) > contractsv1.ContextFabricLimitationsMaxCount {
			t.Errorf("limitations over the cap: %d", len(got.Limitations))
		}
	}
	for _, status := range WorkItemStatusVocabulary() {
		if !contractsv1.IsContextFabricServiceAuthoredLimitation(workItemStatusNoMatchDisclosure(status)) || !contractsv1.IsContextFabricServiceAuthoredLimitation(workItemStatusFilterDisclosure(status)) {
			t.Errorf("status %q disclosures are not recognised", status)
		}
	}
	if contractsv1.IsContextFabricServiceAuthoredLimitation("Some ordinary model caveat.") {
		t.Error("an ordinary caveat was recognised as a member-filter disclosure")
	}
}

func TestWorkItemWindowReadsAndDisclosesTheWindowToTheMicrosecond(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	start := time.Date(2026, 9, 4, 1, 2, 3, 123456789, time.UTC)
	end := start.Add(24 * time.Hour)
	request := statedPeriodRequest("Which work items in Project Alpha were created in the period?")
	request.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{Start: &start, End: &end}
	run := runTupleFilterCase(t, periodTupleFrame(), WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 1}, request)
	if run.invokedErr != nil {
		t.Fatal(run.invokedErr)
	}
	wantStart := start.Truncate(time.Microsecond)
	if !run.request.TimeStart.Equal(wantStart) || !run.request.TimeEnd.Equal(end.Truncate(time.Microsecond)) {
		t.Fatalf("read window %v..%v, want the effective window to the microsecond", run.request.TimeStart, run.request.TimeEnd)
	}
	if !limitationsContain(run.result.Limitations, "2026-09-04T01:02:03.123456Z") {
		t.Fatalf("the disclosure does not state the microsecond bound: %v", run.result.Limitations)
	}
}
