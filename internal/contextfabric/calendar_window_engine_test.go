package contextfabric

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// (trial rev20 proof j, acr 163629d2): five identical calls of "which
// repo carried the most operational/support work last month" on the MCP
// surface committed the calendar window on 2 and clarified on 3, with the same
// decoding seed and the same binder grammar. The window for a bare calendar
// phrase was taken from the interpreter's SAMPLED axis (a range committed, a
// current axis did not). The binder decides it now: engine time, UTC calendar.

// alternatingInterpreter returns the given interpretations in turn, so a test
// controls exactly what the sampled interpreter said on each identical call.
type alternatingInterpreter struct {
	interpretations []InterpretedQuestion
	calls           atomic.Int64
}

func (a *alternatingInterpreter) Interpret(_ context.Context, _ storage.Principal, _ InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	n := int(a.calls.Add(1)) - 1
	return a.interpretations[n%len(a.interpretations)], QuestionFamilyOutcome{}, nil
}

type calendarRun struct {
	explicitWindowRun
	telemetry *recordingTelemetry
}

func runCalendarCase(t *testing.T, surface, question string, field *contractsv1.ContextFabricRequestedEvidenceWindow, reqAxis TemporalAxis, interpretation InterpretedQuestion) calendarRun {
	t.Helper()
	project := acceptanceProject()
	var run calendarRun
	run.telemetry = &recordingTelemetry{}
	facts := factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
		run.factRead = true
		run.factWindow = request.Question.TimeContext.EvidenceWindow
		return bootstrapFactBundle(project), nil
	})
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	engine := buildAcceptanceEngineWithTelemetry(t, graph, facts, interpretation, bootstrapDraft(project), newMapResultStore(), run.telemetry)
	request := validInvestigationRequest()
	request.Question = question
	request.Consumer.Surface = surface
	request.TimeContext.EvidenceWindow = field
	if reqAxis != "" && reqAxis != TemporalCurrent {
		asOf := historicalAsOf()
		request.TimeContext = TimeContext{Axis: reqAxis, AsOf: &asOf}
	}
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	run.result = result
	for _, resolved := range graph.resolveInterpretations {
		run.resolvedAxes = append(run.resolvedAxes, resolved.TimeContext.Axis)
	}
	return run
}

// sampledInterpretations are the two things the trial interpreter said on
// identical calls: a 31-day range (2026-07-12..2026-08-12) and a current axis.
func sampledInterpretations() []InterpretedQuestion {
	start := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	ranged := bootstrapInterpretation()
	ranged.TimeContext = TimeContext{Axis: TemporalRange, Start: &start, End: &end}
	return []InterpretedQuestion{ranged, bootstrapInterpretation()}
}

func runFiveIdenticalCalls(t *testing.T, surface, question string) []InvestigationResult {
	t.Helper()
	project := acceptanceProject()
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bootstrapFactBundle(project), nil
	})
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	runtime := fakeModelRuntime{interpreted: bootstrapInterpretation(), draft: bootstrapDraft(project), receipt: acceptanceReceipt()}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: &alternatingInterpreter{interpretations: sampledInterpretations()},
		Graph:       graph,
		Facts:       facts,
		Synthesizer: RuntimeAnswerSynthesizer{Runtime: runtime, Options: RuntimeAnswerSynthesizerOptions{ServiceVersion: "acceptance-test", Backend: "graph"}},
		Results:     newMapResultStore(),
	}, EngineOptions{
		ServiceVersion: "acceptance-test",
		Now:            func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_acceptance01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	var results []InvestigationResult
	for i := 0; i < 5; i++ {
		request := validInvestigationRequest()
		request.Question = question
		request.Consumer.Surface = surface
		result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
		if err != nil {
			t.Fatalf("call %d: Investigate() error = %v", i, err)
		}
		results = append(results, result)
	}
	return results
}

func assertCalendarWindow(t *testing.T, label string, window *contractsv1.ContextFabricEffectiveEvidenceWindow, start, end time.Time) {
	t.Helper()
	if window == nil || window.Provenance != WindowQuestionStated || window.RelativeID != "" || window.Start == nil || window.End == nil ||
		!window.Start.Equal(start) || !window.End.Equal(end) {
		t.Fatalf("%s: EffectiveEvidenceWindow = %#v, want question_stated over the calendar %v..%v", label, window, start, end)
	}
}

// The defect: identical calls, sampled interpreter alternating range/current.
// Every call must commit the SAME previous-calendar-month window.
func TestCalendarWindow_BareLastMonthCommitsTheSameWindowOnEveryInterpreterSample(t *testing.T) {
	t.Parallel()
	july := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	august := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for _, question := range []string{
		"which repo carried the most operational/support work last month",
		"Which repository carried the most operational/support work last month and why?",
	} {
		for i, result := range runFiveIdenticalCalls(t, "mcp", question) {
			if result.Status == InvestigationClarificationRequired || result.WindowClarification != nil {
				t.Fatalf("%q call %d: status=%q window_clarification=%v, want the committed calendar window on every sample", question, i, result.Status, result.WindowClarification != nil)
			}
			assertCalendarWindow(t, question, result.EffectiveEvidenceWindow, july, august)
		}
	}
}

func TestCalendarWindow_BareLastQuarterAndYearCommitTheCalendarPeriodOnEverySample(t *testing.T) {
	t.Parallel()
	cases := []struct {
		question   string
		start, end time.Time
	}{
		{"Which repository carried the most operational/support work last quarter?", time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
		{"Which repository carried the most operational/support work last year?", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		for i, result := range runFiveIdenticalCalls(t, "mcp", tc.question) {
			if result.Status == InvestigationClarificationRequired {
				t.Fatalf("%q call %d: clarification_required, want the committed calendar window", tc.question, i)
			}
			assertCalendarWindow(t, tc.question, result.EffectiveEvidenceWindow, tc.start, tc.end)
		}
	}
}

// Control (pinned by #679): a trailing phrase commits trailing bounds on every sample.
func TestCalendarWindow_TrailingPhraseStaysTrailingOnEverySample(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	for i, result := range runFiveIdenticalCalls(t, "mcp", "Which repository carried the most operational/support work in the last month?") {
		window := result.EffectiveEvidenceWindow
		if result.Status == InvestigationClarificationRequired || window == nil || window.Provenance != WindowQuestionStated || window.RelativeID != RelativeWindowTrailing30D ||
			!window.End.Equal(now) || !window.Start.Equal(now.AddDate(0, 0, -30)) {
			t.Fatalf("call %d: status=%q window=%#v, want the trailing_30d window", i, result.Status, window)
		}
	}
}

// Control (pinned): no period in the question commits nothing. The current-axis
// sample keeps the inferred-window clarification; a sampled range is the
// interpreter's own historical axis, resolved as before with no window reported.
func TestCalendarWindow_NoPeriodCommitsNothingOnEverySample(t *testing.T) {
	t.Parallel()
	for i, result := range runFiveIdenticalCalls(t, "mcp", validInvestigationRequest().Question) {
		if w := result.EffectiveEvidenceWindow; w != nil && w.Provenance == WindowQuestionStated {
			t.Fatalf("call %d: window = %#v, want nothing committed without a period", i, w)
		}
		if i%2 == 1 && (result.Status != InvestigationClarificationRequired || result.WindowClarification == nil) {
			t.Fatalf("call %d (current axis): status=%q window_clarification=%v, want the inferred-window clarification", i, result.Status, result.WindowClarification != nil)
		}
	}
}

// Other surfaces keep the pinned rule: a bare calendar phrase there is only a
// proposal, never a committed window.
func TestCalendarWindow_OtherSurfacesDoNotCommitABareCalendarPhrase(t *testing.T) {
	t.Parallel()
	for i, result := range runFiveIdenticalCalls(t, "workbench", "Which repository carried the most operational/support work last month?") {
		if result.EffectiveEvidenceWindow != nil && result.EffectiveEvidenceWindow.Provenance == WindowQuestionStated {
			t.Fatalf("call %d: window = %#v, want no committed window off MCP", i, result.EffectiveEvidenceWindow)
		}
	}
}

// A window the caller supplied in the field wins over the phrase.
func TestCalendarWindow_FieldWindowWinsOverACalendarPhrase(t *testing.T) {
	t.Parallel()
	run := runCalendarCase(t, "mcp", "Which repository carried the most operational/support work last month?",
		&contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing90D}, TemporalCurrent, sampledInterpretations()[0])
	if w := run.result.EffectiveEvidenceWindow; w == nil || w.RelativeID != RelativeWindowTrailing90D {
		t.Fatalf("window = %#v, want the supplied trailing_90d", w)
	}
}

// An explicit as-of (the interpreter read a point in time) is never a period:
// the turn keeps the fresh as-of axis and reports no committed window, never a veto.
func TestCalendarWindow_CalendarPhraseWithAnAsOfReadingKeepsThePointInTime(t *testing.T) {
	t.Parallel()
	for _, axis := range []contractsv1.ContextFabricTemporalAxis{contractsv1.ContextFabricTemporalValidTime, contractsv1.ContextFabricTemporalObservedTime} {
		run := runCalendarCase(t, "mcp", "Which repository carried the most operational/support work last month?", nil, TemporalCurrent, driftedInterpretation(axis))
		if run.result.Status == InvestigationNoMatch || axisConflictLimitationServed(run.result) {
			t.Fatalf("%s: status=%q limitations=%q, want no axis-conflict veto for an as-of reading", axis, run.result.Status, run.result.Limitations)
		}
		if len(run.resolvedAxes) == 0 || run.resolvedAxes[0] != axis {
			t.Fatalf("%s: resolved axes = %v, want the interpreted axis untouched", axis, run.resolvedAxes)
		}
		if w := run.result.EffectiveEvidenceWindow; w != nil && w.Provenance == WindowQuestionStated {
			t.Fatalf("%s: window = %#v, want no committed window for a point-in-time reading", axis, w)
		}
	}
}

// A period the caller stated is not a guess: whatever window class the
// interpreter picked (state_snapshot included), the calendar window commits on
// every sample, as a trailing phrase does.
func TestCalendarWindow_TheWindowClassNeverWithdrawsTheCalendarCommit(t *testing.T) {
	t.Parallel()
	for _, class := range []WindowClass{WindowClassStateSnapshot, WindowClassTrendAssessment, ""} {
		for i, sample := range sampledInterpretations() {
			interpretation := sample
			interpretation.WindowClass = class
			if class != "" {
				interpretation.WindowConfidence = WindowConfidenceHigh
			}
			run := runCalendarCase(t, "mcp", "Which repository carried the most operational/support work last month?", nil, TemporalCurrent, interpretation)
			w := run.result.EffectiveEvidenceWindow
			wantStart, wantEnd := calendarPeriodOracle("last month", time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC))
			if w == nil || w.Provenance != WindowQuestionStated || w.Start == nil || w.End == nil || !w.Start.Equal(wantStart) || !w.End.Equal(wantEnd) {
				t.Fatalf("class %q sample %d: window = %#v, want the calendar month %v..%v", class, i, w, wantStart, wantEnd)
			}
		}
	}
}

// A request that asked for a historical axis keeps it: nothing is committed.
func TestCalendarWindow_HistoricalRequestAxisIsNotCommitted(t *testing.T) {
	t.Parallel()
	run := runCalendarCase(t, "mcp", "Which repository carried the most operational/support work last month?", nil, TemporalValidTime, driftedInterpretation(contractsv1.ContextFabricTemporalValidTime))
	if w := run.result.EffectiveEvidenceWindow; w != nil {
		t.Fatalf("window = %#v, want none on a historical request", w)
	}
}

// The decision is loud, origin question_phrase, one line per turn, with the
// interpreted axis as a diagnostic.
func TestCalendarWindow_CalendarCommitIsRecorded(t *testing.T) {
	t.Parallel()
	question := "Which repository carried the most operational/support work last month?"
	cases := []struct {
		name string
		in   InterpretedQuestion
		want statedWindowAxisRecord
	}{
		{"range sample", sampledInterpretations()[0], statedWindowAxisRecord{"mcp", StatedWindowOriginQuestionPhrase, TemporalRange, TemporalCurrent, StatedWindowAxisOverridden}},
		{"current sample", sampledInterpretations()[1], statedWindowAxisRecord{"mcp", StatedWindowOriginQuestionPhrase, TemporalCurrent, TemporalCurrent, StatedWindowAxisAgreed}},
		{"as-of reading", driftedInterpretation(contractsv1.ContextFabricTemporalValidTime), statedWindowAxisRecord{"mcp", StatedWindowOriginQuestionPhrase, TemporalValidTime, TemporalValidTime, StatedWindowAxisWithdrawnPointInTime}},
	}
	for _, tc := range cases {
		run := runCalendarCase(t, "mcp", question, nil, TemporalCurrent, tc.in)
		if len(run.telemetry.statedWindowAxes) != 1 || run.telemetry.statedWindowAxes[0] != tc.want {
			t.Fatalf("%s: stated window axis records = %#v, want [%#v]", tc.name, run.telemetry.statedWindowAxes, tc.want)
		}
	}
}

// A series or a comparison frame gives up the binder's calendar commitment to
// the range read of its stated period; the loud line names that decision with
// its own token, apart from the as-of withdrawal, so a trace shows which
// decision was taken.
func TestCalendarWindow_APeriodShapeFrameLogsItsOwnWithdrawalToken(t *testing.T) {
	t.Parallel()
	for _, temporal := range []TemporalIntent{TemporalIntentTimeSeries, TemporalIntentPeriodComparison} {
		cells := reachableFamilyCells(WindowClassExplicitWindow, true, temporal, periodShapeQuestion(temporal, periodQuestionNamed))
		cell := cells[0]
		cell.time = suppliedRangeEqual
		cell.question = map[TemporalIntent]string{
			TemporalIntentTimeSeries:       "How did the Ask Dev project's throughput change last month?",
			TemporalIntentPeriodComparison: "How does the Ask Dev project's throughput compare with before, last month?",
		}[temporal]
		_, _, telemetry := runSuppliedRangeCellRig(t, cell, mcpSurface)
		var tokens []StatedWindowAxisOutcome
		for _, record := range telemetry.statedWindowAxes {
			tokens = append(tokens, record.Outcome)
		}
		if len(tokens) == 0 || tokens[0] != StatedWindowAxisWithdrawnPeriodShape {
			t.Fatalf("%s: stated window axis outcomes = %v, want %q first", temporal, tokens, StatedWindowAxisWithdrawnPeriodShape)
		}
		for _, token := range tokens {
			if token == StatedWindowAxisWithdrawnPointInTime {
				t.Fatalf("%s: the as-of token was logged for a period shape: %v", temporal, tokens)
			}
		}
	}
}
