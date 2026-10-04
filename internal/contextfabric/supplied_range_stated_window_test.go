package contextfabric

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestQuestionStatesWindowIsTheBoundTrailingPhraseForACurrentOrPeriodFrame(t *testing.T) {
	trailing := WindowBindOutcome{Reason: WindowBindRoutedInferred, Trailing: true}
	explicit := InterpretedQuestion{Shape: ShapeOpen, TimeContext: TimeContext{Axis: TemporalRange}, WindowClass: WindowClassExplicitWindow, WindowConfidence: WindowConfidenceHigh}
	current := frameWith([]InvestigationGoal{GoalAssessState}, namedExpression(SubjectProject), TemporalIntentCurrent, nil)
	period := frameWith([]InvestigationGoal{GoalAssessState}, namedExpression(SubjectProject), TemporalIntentBoundedWindow, nil)
	series := frameWith([]InvestigationGoal{GoalAssessState}, namedExpression(SubjectProject), TemporalIntentTimeSeries, nil)
	comparison := frameWith([]InvestigationGoal{GoalAssessState}, namedExpression(SubjectProject), TemporalIntentPeriodComparison, nil)
	cases := []struct {
		name    string
		outcome WindowBindOutcome
		frame   *QuestionFrame
		want    bool
	}{
		{"trailing, no frame", trailing, nil, true},
		{"trailing, current frame", trailing, &current, true},
		{"trailing, period frame", trailing, &period, true},
		{"trailing, series frame", trailing, &series, false},
		{"trailing, comparison frame", trailing, &comparison, false},
		{"point in time", WindowBindOutcome{Reason: WindowBindRoutedInferred, Trailing: true, PointInTime: true}, &current, false},
		{"not trailing", WindowBindOutcome{Reason: WindowBindRoutedInferred}, &current, false},
		{"not routed", WindowBindOutcome{Reason: WindowBindSpanUnbound, Trailing: true}, &current, false},
	}
	for _, tc := range cases {
		if got := questionStatesWindow(explicit, tc.frame, tc.outcome); got != tc.want {
			t.Errorf("%s: questionStatesWindow = %v, want %v", tc.name, got, tc.want)
		}
	}
	// A class that carries a default keeps the rule it had for every frame.
	recent := explicit
	recent.WindowClass = WindowClassRecentActivityLookup
	recent.Shape = ShapeDiscoveredCohort
	if !questionStatesWindow(recent, &series, trailing) {
		t.Error("a class with a default stopped stating the window on a series frame")
	}
	if got := statedWindowOrigin(requestWindowCanonicalization{BinderProposal: trailing}, explicit, &current, TemporalCurrent, mcpSurface); got != StatedWindowOriginQuestionPhrase {
		t.Errorf("a trailing phrase on MCP: origin %q, want %q", got, StatedWindowOriginQuestionPhrase)
	}
}

// suppliedRangeCell is one supplied interpretation on a 30-day range axis:
// the shape and receipt signals that route it to its family, the window class
// and the frame.
type suppliedRangeCell struct {
	family   QuestionFamily
	shape    InvestigationShape
	terms    []string
	group    SubjectKind
	anchor   string
	class    WindowClass
	frame    *QuestionFrame
	question string
}

// runSuppliedRangeCell runs one cell through Engine.Investigate and returns
// the result and the family the interpreter resolved.
// suppliedRangeInterpreted is the client interpretation a cell sends.
// suppliedRangeRigNow is the clock of the supplied-interpretation rig's engine.
var suppliedRangeRigNow = time.Unix(200, 0).UTC()

func suppliedRangeInterpreted(cell suppliedRangeCell) InterpretedQuestion {
	now := suppliedRangeRigNow
	start := now.Add(-30 * 24 * time.Hour)
	interpreted := suppliedInterpretation()
	interpreted.Shape, interpreted.SubjectTerms = cell.shape, cell.terms
	interpreted.TimeContext = TimeContext{Axis: TemporalRange, Start: &start, End: &now}
	interpreted.WindowClass = cell.class
	if cell.class != "" {
		interpreted.WindowConfidence = WindowConfidenceHigh
	}
	return interpreted
}

func runSuppliedRangeCell(t *testing.T, cell suppliedRangeCell, surface string) (InvestigationResult, QuestionFamily) {
	t.Helper()
	receipt := clientInterpretReceipt()
	receipt.QuestionFamily = cell.family
	receipt.GroupKind = cell.group
	receipt.QuestionFrame = cell.frame
	if cell.anchor != "" {
		receipt.ScopeAnchorTerm, receipt.ScopeAnchorKind, receipt.RequestedSubjectKind = cell.anchor, SubjectProject, SubjectWorkItem
	}
	interpreted := suppliedRangeInterpreted(cell)
	spy := &familyTelemetrySpy{}
	rig := newSuppliedEngineRig(t, RuntimeQuestionInterpreter{Supplied: &scriptedSuppliedRuntime{interpreted: interpreted, receipt: receipt}, FamilyTelemetry: spy})
	request := suppliedInvestigationRequest(validInvestigationRequest())
	request.Question = cell.question
	request.Consumer.Surface = surface
	result, err := rig.engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("%s/%s %q: %v", cell.family, cell.class, cell.question, err)
	}
	family := QuestionFamily("")
	if len(spy.events) > 0 {
		family = spy.events[len(spy.events)-1].Family
	}
	return result, family
}

func suppliedRangeRow(result InvestigationResult, family QuestionFamily, cell suppliedRangeCell) string {
	window := "no window"
	if w := result.EffectiveEvidenceWindow; w != nil {
		window = string(w.Provenance) + " " + string(w.RelativeID)
	}
	temporal := "none"
	if cell.frame != nil {
		temporal = string(cell.frame.Temporal)
	}
	class := string(cell.class)
	if class == "" {
		class = "(none)"
	}
	return fmt.Sprintf("ROW|%s|%s|%s|%s|%s %s %s", family, class, temporal, cell.question, result.Status, result.Interpretation.TimeContext.Axis, window)
}

func reachableFamilyCells(class WindowClass, frame *QuestionFrame, question string) []suppliedRangeCell {
	return []suppliedRangeCell{
		{family: QuestionFamilySubjectInvestigation, shape: ShapeSingleSubject, terms: []string{"Ask Dev"}, class: class, frame: frame, question: question},
		{family: QuestionFamilyDiscoveredCohortRanking, shape: ShapeOpen, class: class, frame: frame, question: question},
		{family: QuestionFamilyScopedCohortStatus, shape: ShapeExplicitCohort, anchor: "Ask Dev", class: class, frame: frame, question: question},
		{family: QuestionFamilyGroupedCohortStatus, shape: ShapeDiscoveredCohort, group: SubjectTeam, class: class, frame: frame, question: question},
		{family: QuestionFamilyExplicitComparison, shape: ShapeExplicitCohort, terms: []string{"Ask Dev", "Payments"}, class: class, frame: frame, question: question},
		{family: QuestionFamilyUnclassified, shape: ShapeExplicitCohort, class: class, frame: frame, question: question},
	}
}

// Every reachable family, routed by its own signals: a client range for a
// stated trailing period of a current-state question runs on the current axis
// with the question_stated window; a series or a period comparison, or a
// question that states no period, keeps the client's range.
func TestSuppliedRangeWithAStatedTrailingPhraseRunsOnTheCurrentAxis(t *testing.T) {
	series := frameWith([]InvestigationGoal{GoalAssessState}, namedExpression(SubjectProject), TemporalIntentTimeSeries, nil)
	comparison := frameWith([]InvestigationGoal{GoalAssessState}, namedExpression(SubjectProject), TemporalIntentPeriodComparison, nil)
	const stated = "How is the Ask Dev project doing in the last 30 days?"
	type expectation struct {
		cells   []suppliedRangeCell
		current bool
	}
	var groups []expectation
	for _, class := range []WindowClass{WindowClassExplicitWindow, WindowClassStateSnapshot, ""} {
		groups = append(groups, expectation{reachableFamilyCells(class, nil, stated), true})
	}
	groups = append(groups,
		expectation{reachableFamilyCells(WindowClassExplicitWindow, &series, "How has the Ask Dev project's throughput changed over the last 30 days?"), false},
		expectation{reachableFamilyCells(WindowClassExplicitWindow, &comparison, "How does the Ask Dev project's last 30 days compare to the previous 30 days?"), false},
		expectation{reachableFamilyCells(WindowClassExplicitWindow, &comparison, "Compare the Ask Dev project in the last 30 days with the month before"), false},
		expectation{reachableFamilyCells(WindowClassExplicitWindow, nil, "How was the Ask Dev project doing as of the start of the last 30 days?"), false},
		expectation{reachableFamilyCells(WindowClassExplicitWindow, nil, "How is the Ask Dev project doing?"), false},
	)
	for _, group := range groups {
		for _, cell := range group.cells {
			result, family := runSuppliedRangeCell(t, cell, mcpSurface)
			t.Log(suppliedRangeRow(result, family, cell))
			if cell.frame == nil && family != cell.family {
				t.Errorf("%s/%q: resolved family %s, the cell does not route to its family", cell.family, cell.class, family)
			}
			// The rule a class default already decided is unchanged: such a
			// cell runs on the current axis on every frame, as before.
			binder := ProposeWindowFromSpans(cell.question)
			interpreted := suppliedRangeInterpreted(cell)
			_, classStates := DefaultRelativeID(ClassifyWindow(interpreted, interpreted.WindowClass, interpreted.WindowConfidence), windowDefaultPolicy)
			// A calendar or unbound period the interpreter read as a range is
			// committed by interpreterPeriodWindow, unchanged by this rule.
			period := interpreterPeriodWindow(requestWindowCanonicalization{BinderProposal: binder}, TemporalCurrent, interpreted.TimeContext, true, mcpSurface)
			want := group.current || (binder.Reason == WindowBindRoutedInferred && binder.Trailing && classStates) || period != nil
			window := result.EffectiveEvidenceWindow
			onCurrent := result.Interpretation.TimeContext.Axis == TemporalCurrent && window != nil && window.Provenance == WindowQuestionStated
			if onCurrent != want {
				t.Errorf("%s/%q %q: axis=%s window=%+v, want stated-current=%v", cell.family, cell.class, cell.question, result.Interpretation.TimeContext.Axis, window, want)
			}
			if limitationsContain(result.Limitations, "which is not the period the question states") {
				t.Errorf("%s/%q %q: a range equal to the stated period was disclosed as a conflict: %q", cell.family, cell.class, cell.question, result.Limitations)
			}
			if !want && result.Interpretation.TimeContext.Axis != TemporalRange {
				t.Errorf("%s/%q %q: axis=%s, want the client's range kept", cell.family, cell.class, cell.question, result.Interpretation.TimeContext.Axis)
			}
		}
	}
	// Another surface keeps the client's range.
	result, _ := runSuppliedRangeCell(t, reachableFamilyCells(WindowClassExplicitWindow, nil, stated)[0], "api")
	if result.Interpretation.TimeContext.Axis != TemporalRange {
		t.Errorf("api surface: axis=%s, want the client's range kept", result.Interpretation.TimeContext.Axis)
	}
}

// A client range that differs from the period the question states: the turn
// runs on the stated period and the answer names both periods. A range equal
// to the stated period, or within a day of it at both bounds, is not a
// conflict.
func TestSuppliedRangeThatDiffersFromTheStatedPeriodIsDisclosed(t *testing.T) {
	shorterStart, shorterEnd := suppliedRangeRigNow.Add(-7*24*time.Hour), suppliedRangeRigNow
	trailing := WindowBindOutcome{Reason: WindowBindRoutedInferred, Trailing: true, RelativeID: RelativeWindowTrailing30D}
	if conflict := detectStatedRangeConflict(trailing, TimeContext{Axis: TemporalValidTime, Start: &shorterStart, End: &shorterEnd}, suppliedRangeRigNow); conflict != nil {
		t.Errorf("a time context that is not a range was read as a conflicting range: %+v", conflict)
	}
	if conflict := detectStatedRangeConflict(trailing, TimeContext{Axis: TemporalRange, Start: &shorterStart, End: &shorterEnd}, suppliedRangeRigNow); conflict == nil {
		t.Error("a 7-day range against a stated 30 days is not a conflict")
	}
	const stated = "How is the Ask Dev project doing in the last 30 days?"
	day := 24 * time.Hour
	now := suppliedRangeRigNow
	for _, tc := range []struct {
		name       string
		start, end time.Time
		conflict   bool
	}{
		{"equal", now.Add(-30 * day), now, false},
		{"rounded to whole days", now.Add(-30 * day).Truncate(day), now.Add(day).Truncate(day), false},
		{"shorter", now.Add(-7 * day), now, true},
		{"longer", now.Add(-90 * day), now, true},
		{"shifted", now.Add(-60 * day), now.Add(-30 * day), true},
		{"ends early", now.Add(-30 * day), now.Add(-10 * day), true},
	} {
		cell := reachableFamilyCells(WindowClassExplicitWindow, nil, stated)[1]
		receipt := clientInterpretReceipt()
		interpreted := suppliedRangeInterpreted(cell)
		start, end := tc.start, tc.end
		interpreted.TimeContext = TimeContext{Axis: TemporalRange, Start: &start, End: &end}
		rig := newSuppliedEngineRig(t, RuntimeQuestionInterpreter{Supplied: &scriptedSuppliedRuntime{interpreted: interpreted, receipt: receipt}})
		request := suppliedInvestigationRequest(validInvestigationRequest())
		request.Question = stated
		request.Consumer.Surface = mcpSurface
		result, err := rig.engine.Investigate(context.Background(), acceptancePrincipal(), request)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		window := result.EffectiveEvidenceWindow
		if result.Interpretation.TimeContext.Axis != TemporalCurrent || window == nil || window.Provenance != WindowQuestionStated || window.RelativeID != RelativeWindowTrailing30D {
			t.Errorf("%s: axis=%s window=%+v, want the stated trailing_30d window on the current axis", tc.name, result.Interpretation.TimeContext.Axis, window)
		}
		want := ""
		if tc.conflict {
			want = (&statedRangeConflict{InterpretedStart: start.UTC(), InterpretedEnd: end.UTC(), StatedStart: now.Add(-30 * day), StatedEnd: now}).limitation()
		}
		got := limitationsContain(result.Limitations, "which is not the period the question states")
		if got != tc.conflict || (tc.conflict && !limitationsContain(result.Limitations, want)) {
			t.Errorf("%s: limitations %q, want conflict disclosed=%v (%q)", tc.name, result.Limitations, tc.conflict, want)
		}
	}
}

// The work-item member shape a client sent on prod: a current status frame,
// status done, a 30-day range and the role word in the question text.
func TestSuppliedStatusRangeServesWorkItemMembersOnTheFullPath(t *testing.T) {
	defer func() {
		if t.Failed() {
			return
		}
		weekStart := time.Now().UTC().Add(-7 * 24 * time.Hour)
		weekEnd := time.Now().UTC()
		frame := prodStatusPeriodFrame()
		frame.Temporal = TemporalIntentCurrent
		run := runInterpretedTupleCase(t, suppliedInterpretedPath, "which work items of project Alpha that are closed were created in the last 30 days?", frame, TimeContext{Axis: TemporalRange, Start: &weekStart, End: &weekEnd})
		if run.reads != 1 || run.request.TimeColumn != "created_at" || run.request.TimeEnd.Sub(run.request.TimeStart) < 29*24*time.Hour || !limitationsContain(run.result.Limitations, "which is not the period the question states") {
			t.Errorf("a 7-day supplied range on a 30-day question: reads=%d column=%q window=%s..%s limitations=%q, want the stated 30 days read and the conflict disclosed", run.reads, run.request.TimeColumn, run.request.TimeStart, run.request.TimeEnd, run.result.Limitations)
		}
		run = runInterpretedTupleCase(t, suppliedInterpretedPath, "which work items of project Alpha were created and closed in the last 30 days?", frame, TimeContext{Axis: TemporalRange, Start: &weekStart, End: &weekEnd})
		if run.result.Status != InvestigationNoMatch || !limitationsContain(run.result.Limitations, "which is not the period the question states") {
			t.Errorf("a 7-day supplied range on a refused 30-day question: status=%s limitations=%q, want the conflict disclosed on the refusal too", run.result.Status, run.result.Limitations)
		}
	}()
	defer reportWorkItemMutationPanic(t)
	now := time.Now().UTC()
	start := now.Add(-30 * 24 * time.Hour)
	frame := prodStatusPeriodFrame()
	frame.Temporal = TemporalIntentCurrent
	for _, tc := range []struct{ question, column string }{
		{"which work items of project Alpha that are closed were created in the last 30 days?", "created_at"},
		{"which work items of project Alpha are closed were created in the last 30 days?", "created_at"},
		{"which closed work items of project Alpha were created in the last 30 days?", "created_at"},
		{"which work items of project Alpha were closed in the last 30 days?", "completed_at"},
		{"which work items of project Alpha were created and closed in the last 30 days?", ""},
	} {
		run := runInterpretedTupleCase(t, suppliedInterpretedPath, tc.question, frame, TimeContext{Axis: TemporalRange, Start: &start, End: &now})
		if limitationsContain(run.result.Limitations, "which is not the period the question states") {
			t.Errorf("%q: a range equal to the stated period was disclosed as a conflict", tc.question)
		}
		if run.invokedErr != nil {
			t.Fatalf("%q: %v", tc.question, run.invokedErr)
		}
		if tc.column == "" {
			if run.result.Status != InvestigationNoMatch || run.reads != 0 || !limitationsContain(run.result.Limitations, memberTimeRoleClarificationLimitation(MemberTimeRoleAmbiguous)) {
				t.Errorf("%q: status=%s reads=%d limitations=%q, want the two-roles sentence and no read", tc.question, run.result.Status, run.reads, run.result.Limitations)
			}
			continue
		}
		if run.reads != 1 || run.request.Status != "done" || run.request.TimeColumn != tc.column || run.result.Cohort == nil || len(run.result.Cohort.Members) != 1 {
			t.Errorf("%q: reads=%d read status=%q column=%q cohort=%+v, want one member read on done and %s", tc.question, run.reads, run.request.Status, run.request.TimeColumn, run.result.Cohort, tc.column)
		}
		if !limitationsContain(run.result.Limitations, "(the "+tc.column+" field)") {
			t.Errorf("%q: limitations %q, want the %s period named", tc.question, run.result.Limitations, tc.column)
		}
	}
}
