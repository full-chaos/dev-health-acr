package contextfabric

import (
	"context"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	thisMonthPeriod   = CalendarPeriod("this_month")
	thisQuarterPeriod = CalendarPeriod("this_quarter")
	thisYearPeriod    = CalendarPeriod("this_year")
)

func TestCalendarWindow_BinderNamesTheCurrentCalendarPeriod(t *testing.T) {
	t.Parallel()
	cases := []struct {
		question string
		want     CalendarPeriod
	}{
		{"Which teams need attention this month?", thisMonthPeriod},
		{"Which teams need attention this month and why?", thisMonthPeriod},
		{"This month, which teams need attention?", thisMonthPeriod},
		{"What did the team ship in this month?", thisMonthPeriod},
		{"What did the team ship during this quarter?", thisQuarterPeriod},
		{"Which repository carried the most work this quarter?", thisQuarterPeriod},
		{"Which repository carried the most work this year?", thisYearPeriod},
		{"Which repository carried the most work last month?", CalendarPeriodMonth},
		{"Which repository carried the most work in the last month?", CalendarPeriodNone},
		{"What was the team's state as of the end of this month?", CalendarPeriodNone},
		{"Compare this month with last month for the team.", CalendarPeriodNone},
		{"How did this month treat the operational work of each repository?", CalendarPeriodNone},
		{"Which team owns This Month?", CalendarPeriodNone},
		{"Which repository carried the most work?", CalendarPeriodNone},
	}
	for _, tc := range cases {
		if got := ProposeWindowFromSpans(tc.question).Calendar; got != tc.want {
			t.Errorf("%q: Calendar = %q, want %q", tc.question, got, tc.want)
		}
	}
}

func TestCalendarWindow_CurrentPeriodBounds(t *testing.T) {
	t.Parallel()
	at := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }
	cases := []struct {
		name       string
		period     CalendarPeriod
		now        time.Time
		start, end time.Time
		ok         bool
	}{
		{"month mid excludes today", thisMonthPeriod, at(2026, 8, 12, 12), at(2026, 8, 1, 0), at(2026, 8, 12, 0), true},
		{"month second day", thisMonthPeriod, at(2026, 8, 2, 0), at(2026, 8, 1, 0), at(2026, 8, 2, 0), true},
		{"month first day is empty", thisMonthPeriod, at(2026, 8, 1, 12), time.Time{}, time.Time{}, false},
		{"quarter mid", thisQuarterPeriod, at(2026, 8, 12, 12), at(2026, 7, 1, 0), at(2026, 8, 12, 0), true},
		{"quarter first day is empty", thisQuarterPeriod, at(2026, 7, 1, 5), time.Time{}, time.Time{}, false},
		{"year mid", thisYearPeriod, at(2026, 8, 12, 12), at(2026, 1, 1, 0), at(2026, 8, 12, 0), true},
		{"year first day is empty", thisYearPeriod, at(2026, 1, 1, 23), time.Time{}, time.Time{}, false},
		{"previous month unchanged", CalendarPeriodMonth, at(2026, 8, 12, 12), at(2026, 7, 1, 0), at(2026, 8, 1, 0), true},
	}
	for _, tc := range cases {
		start, end, ok := calendarWindowBounds(tc.period, tc.now)
		if ok != tc.ok || !start.Equal(tc.start) || !end.Equal(tc.end) {
			t.Errorf("%s: bounds = %v..%v ok=%v, want %v..%v ok=%v", tc.name, start, end, ok, tc.start, tc.end, tc.ok)
		}
	}
}

type calendarServedRun struct {
	result   InvestigationResult
	factRead bool
}

func runServedWindowCase(t *testing.T, now time.Time, question string, allowClarification bool) calendarServedRun {
	t.Helper()
	return runServedWindowCaseWithDraft(t, now, question, allowClarification, nil)
}

func runServedWindowCaseWithDraft(t *testing.T, now time.Time, question string, allowClarification bool, mutateDraft func(*SynthesisDraft)) calendarServedRun {
	t.Helper()
	project := acceptanceProject()
	var run calendarServedRun
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		run.factRead = true
		return bootstrapFactBundle(project), nil
	})
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	draft := bootstrapDraft(project)
	if mutateDraft != nil {
		mutateDraft(&draft)
	}
	runtime := fakeModelRuntime{interpreted: bootstrapInterpretation(), draft: draft, receipt: acceptanceReceipt()}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: &countingInterpreter{interpretation: bootstrapInterpretation()},
		Graph:       graph,
		Facts:       facts,
		Synthesizer: RuntimeAnswerSynthesizer{Runtime: runtime, Options: RuntimeAnswerSynthesizerOptions{ServiceVersion: "acceptance-test", Backend: "graph"}},
		Results:     newMapResultStore(),
	}, EngineOptions{
		ServiceVersion: "acceptance-test",
		Now:            func() time.Time { return now },
		NewResultID:    func() string { return "result_acceptance01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	request := validInvestigationRequest()
	request.Question = question
	request.Consumer.Surface = "mcp"
	request.Options.AllowClarification = allowClarification
	run.result, err = engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return run
}

func TestCalendarWindow_ThisPeriodCommitsTheCalendarPeriodToDateExcludingToday(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	today := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		question string
		start    time.Time
	}{
		{"Which teams need attention this month?", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
		{"Which teams need attention this quarter?", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
		{"Which teams need attention this year?", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, allow := range []bool{true, false} {
		for _, tc := range cases {
			run := runServedWindowCase(t, now, tc.question, allow)
			result := run.result
			if result.Status == InvestigationClarificationRequired || result.Status == InvestigationNoMatch || result.WindowClarification != nil || !run.factRead {
				t.Fatalf("%q allow=%v: status=%q window_clarification=%v fact_read=%v, want a served answer", tc.question, allow, result.Status, result.WindowClarification != nil, run.factRead)
			}
			assertCalendarWindow(t, tc.question, result.EffectiveEvidenceWindow, tc.start, today)
			if result.EffectiveEvidenceWindow.Confidence != WindowConfidenceHigh {
				t.Fatalf("%q: confidence = %q, want high", tc.question, result.EffectiveEvidenceWindow.Confidence)
			}
		}
	}
}

func TestCalendarWindow_ThisMonthOnTheFirstFallsToTheInferredPath(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	question := "Which teams need attention this month?"
	gated := runServedWindowCase(t, now, question, true)
	if gated.result.Status != InvestigationClarificationRequired || gated.result.WindowClarification == nil {
		t.Fatalf("allow=true: status=%q window_clarification=%v, want the unchanged clarification path", gated.result.Status, gated.result.WindowClarification != nil)
	}
	served := runServedWindowCase(t, now, question, false)
	window := served.result.EffectiveEvidenceWindow
	if served.result.Status == InvestigationNoMatch || !served.factRead || window == nil || window.Provenance != WindowInferredDefault || window.Confidence == WindowConfidenceHigh {
		t.Fatalf("allow=false: status=%q fact_read=%v window=%#v, want an answer over a low-confidence inferred_default window", served.result.Status, served.factRead, window)
	}
}

func TestWindowGate_AllowClarificationFalseAnswersAgainstTheInferredWindowWithOneWarning(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	run := runServedWindowCase(t, now, validInvestigationRequest().Question, false)
	result := run.result
	if result.Status == InvestigationNoMatch || result.Status == InvestigationClarificationRequired || !run.factRead {
		t.Fatalf("status=%q fact_read=%v, want an answer against the inferred window, never no_match for a window alone", result.Status, run.factRead)
	}
	window := result.EffectiveEvidenceWindow
	if window == nil || window.Provenance != WindowInferredDefault || window.Confidence != WindowConfidenceLow {
		t.Fatalf("EffectiveEvidenceWindow = %#v, want inferred_default with low confidence", window)
	}
	if result.WindowClarification != nil || result.StructureNeeds != nil {
		t.Fatalf("window_clarification=%v structure_needs=%v, want no offer when clarification is not permitted", result.WindowClarification, result.StructureNeeds)
	}
	var skipped []string
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "confirmation was skipped") {
			skipped = append(skipped, warning)
		}
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], string(window.RelativeID)) {
		t.Fatalf("warnings = %q, want exactly one skipped-confirmation warning naming %q", result.Warnings, window.RelativeID)
	}
	if strings.Contains(result.DeterministicAnswer, "clarification was not permitted") || limitationsContain(result.Limitations, "clarification was not permitted") {
		t.Fatalf("answer=%q limitations=%q, want no refusal prose", result.DeterministicAnswer, result.Limitations)
	}
	for _, row := range result.Completeness.Outcomes {
		if row.CauseCoverage == contractsv1.ContextFabricCoverageDetailAnswerTerminatedBeforeAttempt {
			t.Fatalf("outcome row %#v is answer_terminated_before_attempt, want every planned requirement attempted", row)
		}
	}
}

func TestWindowGate_AllowClarificationTrueStillGates(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	run := runServedWindowCase(t, now, validInvestigationRequest().Question, true)
	if run.result.Status != InvestigationClarificationRequired || run.result.WindowClarification == nil || run.factRead {
		t.Fatalf("status=%q window_clarification=%v fact_read=%v, want the unchanged confirmation gate", run.result.Status, run.result.WindowClarification != nil, run.factRead)
	}
}

func TestWindowGate_SkippedWarningReportsTheWindowItDescribes(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	for _, confidence := range []WindowConfidence{WindowConfidenceHigh, WindowConfidenceLow, ""} {
		window := &contractsv1.ContextFabricEffectiveEvidenceWindow{Start: &start, End: &end, RelativeID: RelativeWindowTrailing30D, Provenance: WindowInferredDefault, Confidence: confidence}
		warning := windowConfirmationSkippedWarning(window)
		if strings.Contains(warning, "No evidence window was stated") {
			t.Fatalf("confidence %q: warning %q denies a window the question may state", confidence, warning)
		}
		for _, other := range []WindowConfidence{WindowConfidenceHigh, WindowConfidenceLow} {
			if mentions := strings.Contains(warning, string(other)+" confidence"); mentions != (other == confidence) {
				t.Fatalf("confidence %q: warning %q mentions %q confidence = %v", confidence, warning, other, mentions)
			}
		}
		if !strings.Contains(warning, "confirmation was skipped") || !strings.Contains(warning, string(RelativeWindowTrailing30D)) {
			t.Fatalf("confidence %q: warning %q must name the window and the skipped confirmation", confidence, warning)
		}
	}
}

func TestWindowGate_SkippedWarningSurvivesAFullWarningList(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	run := runServedWindowCaseWithDraft(t, now, validInvestigationRequest().Question, false, func(draft *SynthesisDraft) {
		draft.Warnings = nil
		for i := 0; i < contractsv1.ContextFabricWarningsMaxCount; i++ {
			draft.Warnings = append(draft.Warnings, "model warning "+strings.Repeat("x", i%7)+time.Duration(i).String())
		}
	})
	var skipped int
	for _, warning := range run.result.Warnings {
		if strings.Contains(warning, "confirmation was skipped") {
			skipped++
		}
	}
	if skipped != 1 || len(run.result.Warnings) > contractsv1.ContextFabricWarningsMaxCount {
		t.Fatalf("skipped warnings = %d, total = %d (max %d), want the skipped-confirmation warning kept within the bound", skipped, len(run.result.Warnings), contractsv1.ContextFabricWarningsMaxCount)
	}
}

func TestWindowGate_StoredWindowClarificationIsNotReusedWhenClarificationIsDeclined(t *testing.T) {
	t.Parallel()
	for _, allow := range []bool{true, false} {
		project, candidate := reusableCandidate()
		candidate.Status = InvestigationClarificationRequired
		candidate.SubjectResolution = withheldPoolResolution()
		candidate.StructureNeeds = nil
		candidate.WindowClarification = &contractsv1.ContextFabricWindowClarification{Options: []contractsv1.ContextFabricWindowOption{{}}}
		candidate.EffectiveEvidenceWindow = &EffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing90D, Provenance: WindowInferredDefault}
		engine := mustReuseTestEngine(t, EngineDependencies{
			Graph: graphReaderStub{
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
				bases:      provenCommitBases(project),
			},
			Results: &resultStoreStub{},
			ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
				return candidate, true, nil
			}),
		})
		request := validInvestigationRequest()
		request.Options.AllowClarification = allow
		_, hit, _, _ := engine.tryReuse(context.Background(), reusePrincipal(), request, TimeContext{Axis: TemporalCurrent}, "", windowKeyRederivable, ResolvedGraphBinding{GraphKey: "some-key", Epoch: 0})
		if hit != allow {
			t.Fatalf("allow_clarification=%v: reuse hit = %v, want %v: a window clarification is served only to a caller that can be asked", allow, hit, allow)
		}
	}
}

func TestCalendarWindow_AComparisonKeepsTheBoundsTheBinderFroze(t *testing.T) {
	t.Parallel()
	frozenStart, frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	canon := requestWindowCanonicalization{
		BinderProposal: WindowBindOutcome{Reason: WindowBindRoutedInferred, SpansBound: 1, Calendar: CalendarPeriodThisMonth},
		CalendarStart:  frozenStart, CalendarEnd: frozenEnd,
	}
	interpreted := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	fresh := TimeContext{Axis: TemporalRange, Start: &interpreted, End: &frozenEnd}
	period := statedPeriodRead(canon, bootstrapInterpretation(), &QuestionFrame{Temporal: TemporalIntentPeriodComparison}, TemporalCurrent, fresh, true, mcpSurface, frozenEnd)
	if period == nil || !period.Start.Equal(frozenStart) || !period.End.Equal(frozenEnd) {
		t.Fatalf("period = %+v, want the frozen %v..%v, not the interpreter range", period, frozenStart, frozenEnd)
	}
}

func TestWorkItemWindowBasisCommitsThePeriodANamedWhenClarificationIsDeclined(t *testing.T) {
	t.Parallel()
	question := "Which work items in Project Alpha were created this month?"
	canon := requestWindowCanonicalization{BinderProposal: ProposeWindowFromSpans(question)}
	if b := deriveWorkItemTupleWindowBasisFor(question, canon, false, true); !b.Committed || b.Role != MemberTimeRoleCreated {
		t.Fatalf("declined clarification: basis = %+v, want committed on the created field", b)
	}
	if b := deriveWorkItemTupleWindowBasisFor(question, canon, false, false); b.Committed {
		t.Fatalf("clarification allowed: basis = %+v, want unchanged (not committed)", b)
	}
	asOf := "Which work items were created as of the end of this month?"
	if b := deriveWorkItemTupleWindowBasisFor(asOf, requestWindowCanonicalization{BinderProposal: ProposeWindowFromSpans(asOf)}, false, true); b.Committed {
		t.Fatalf("point in time: basis = %+v, want not committed", b)
	}
}

func TestWorkItemPeriodQuestionIsAnsweredAgainstTheInferredWindowWhenClarificationIsDeclined(t *testing.T) {
	census := WorkItemMembershipCensus{State: WorkItemMembershipCensusExact, PopulationMeasured: true, AuthorizedPopulation: 1}
	declined := statedPeriodRequest("Which work items in Project Alpha were created this month?")
	declined.Options.AllowClarification = false
	run := runTupleFilterCaseWithClass(t, periodTupleFrame(), census, declined, WindowClassTrendAssessment)
	if run.invokedErr != nil || run.result.Status == InvestigationNoMatch || run.reads != 1 {
		t.Fatalf("declined: err=%v status=%q membership reads=%d lim=%q adm=%+v win=%+v", run.invokedErr, run.result.Status, run.reads, run.result.Limitations, run.admissions, run.result.EffectiveEvidenceWindow)
	}
	control := statedPeriodRequest("Which work items in Project Alpha were created in the last 30 days?")
	control.Options.AllowClarification = false
	run = runTupleFilterCaseWithClass(t, periodTupleFrame(), census, control, WindowClassTrendAssessment)
	if run.invokedErr != nil || run.result.Status == InvestigationNoMatch || run.reads != 1 {
		t.Fatalf("stated trailing control: err=%v status=%q membership reads=%d, want unchanged", run.invokedErr, run.result.Status, run.reads)
	}
}
