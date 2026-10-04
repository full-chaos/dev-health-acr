package contextfabric

import (
	"context"
	"fmt"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// periodShapeRun is one turn of a period-shape question: the served result and
// the time context of every canonical fact read the turn made.
type periodShapeRun struct {
	result    InvestigationResult
	factTimes []TimeContext
	telemetry *recordingTelemetry
}

// periodShapeInterpreted is the interpretation a client builds from the
// published interpret prompt for one named project: the prompt copies the
// request's time context, so a prompt-faithful client sends the current axis
// with no bounds; a client that edits its time context sends a range.
func periodShapeInterpreted(goals []InvestigationGoal, temporal TemporalIntent, facts []FactKind, timeContext TimeContext) (InterpretedQuestion, *QuestionFrame) {
	requirements := make([]FactRequirement, 0, len(facts))
	for _, kind := range facts {
		requirements = append(requirements, FactRequirement{Kind: kind})
	}
	frame := frameWith(goals, SubjectExpression{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{Terms: []string{"Ask Dev"}, ExpectedKind: kindPointer(SubjectProject)}}, temporal, nil)
	return InterpretedQuestion{
		Shape: ShapeSingleSubject, RequestedJudgment: "how the project's delivery over the stated period moved", SubjectTerms: []string{"Ask Dev"},
		TimeContext: timeContext, WindowClass: WindowClassTrendAssessment, WindowConfidence: WindowConfidenceHigh,
		FactRequirements: requirements,
	}, &frame
}

func runPeriodShapeTurn(t *testing.T, path interpreterPath, question string, interpreted InterpretedQuestion, frame *QuestionFrame) periodShapeRun {
	t.Helper()
	return runPeriodShapeTurnWith(t, path, question, interpreted, frame, periodShapeTurnOptions{})
}

// periodShapeTurnOptions vary one turn: model caveats the synthesis writes, a
// surface other than MCP, and an edit of the request.
type periodShapeTurnOptions struct {
	caveats int
	surface string
	request func(*InvestigationRequest)
}

func runPeriodShapeTurnWith(t *testing.T, path interpreterPath, question string, interpreted InterpretedQuestion, frame *QuestionFrame, options periodShapeTurnOptions) periodShapeRun {
	t.Helper()
	caveats := options.caveats
	run := periodShapeRun{telemetry: &recordingTelemetry{}}
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.QuestionFrame = frame
	receipt.QuestionFamily = QuestionFamilySubjectInvestigation
	receipt.RequestedSubjectKind = SubjectProject
	interpreter := RuntimeQuestionInterpreter{Sink: &fakeReceiptSink{}}
	request := validInvestigationRequest()
	switch path {
	case serverInterpretedPath:
		interpreter.Runtime = fakeModelRuntime{interpreted: interpreted, receipt: receipt}
	case suppliedInterpretedPath:
		receipt = clientInterpretReceipt()
		receipt.QuestionFrame = frame
		receipt.QuestionFamily = QuestionFamilySubjectInvestigation
		receipt.RequestedSubjectKind = SubjectProject
		interpreter.Supplied = &scriptedSuppliedRuntime{interpreted: interpreted, receipt: receipt}
		request = suppliedInvestigationRequest(request)
	}
	request.Question = question
	request.Consumer.Surface = mcpSurface
	if options.surface != "" {
		request.Consumer.Surface = options.surface
	}
	if options.request != nil {
		options.request(&request)
	}
	var events []string
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}}},
			context: GraphContext{
				Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
				EvidenceRefIDs: []string{"evidence_project_status"}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, factRequest CanonicalFactRequest) (CanonicalFactBundle, error) {
			run.factTimes = append(run.factTimes, factRequest.Question.TimeContext)
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			answer := decisiveSynthesis()
			for i := 0; i < caveats; i++ {
				answer.Limitations = append(answer.Limitations, fmt.Sprintf("Model caveat number %d about this answer.", i+1))
			}
			return answer, nil
		}),
		Interpreter: interpreter,
		Results:     &resultStoreStub{},
		Telemetry:   run.telemetry,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return InvestigationResult{}, false, nil
		}),
		ReuseSnapshotter:      orderTrackingSnapshotter{events: &events, snapshot: SourceWatermarkSnapshot{"linear": "wm-1"}},
		ReuseEpochSnapshotter: orderTrackingEpochSnapshotter{events: &events, epoch: 7},
	})
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("%q: %v", question, err)
	}
	run.result = result
	return run
}

// primaryFactTime is the time context of the turn's first canonical fact read.
func (r periodShapeRun) primaryFactTime(t *testing.T) TimeContext {
	t.Helper()
	if len(r.factTimes) == 0 {
		t.Fatalf("no canonical fact read: status=%s limitations=%q", r.result.Status, r.result.Limitations)
	}
	return r.factTimes[0]
}

// comparisonPeriodUnreadLimitation is the disclosure a comparison of the
// stated period start..end carries: the equal period just before it was not
// read.
func comparisonPeriodUnreadLimitation(start, end time.Time) string {
	before := start.Add(-end.Sub(start))
	return contractsv1.ContextFabricComparisonPeriodUnreadLimitation(start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), before.Format(time.RFC3339Nano), start.Format(time.RFC3339Nano))
}

func sameInstant(a *time.Time, b time.Time) bool {
	return a != nil && a.Equal(b)
}

const (
	seriesQuestion            = "how has project Ask Dev's throughput changed over the last 30 days?"
	comparisonQuestion        = "how does project Ask Dev's last 30 days compare to the previous 30 days?"
	measureComparisonQuestion = "how does project Ask Dev's throughput over the last 30 days compare to the previous 30 days?"
)

var burdenComposite = []FactKind{FactFlow, FactHealth, FactIncidents, FactInvestment, FactWorkload}

// A series question reads a series over the stated period: the prompt-faithful
// client (current axis, no bounds), the server interpreter (current axis) and a
// client range alike run on the range axis over the 30 days the question
// states, whatever window class came with it.
func TestSeriesFrameWithAStatedPeriodReadsTheStatedRange(t *testing.T) {
	day := 24 * time.Hour
	now := suppliedRangeRigNow
	start := now.Add(-30 * day)
	week := now.Add(-7 * day)
	for _, tc := range []struct {
		name     string
		path     interpreterPath
		time     TimeContext
		conflict bool
	}{
		{"client, current axis as the prompt copies it", suppliedInterpretedPath, TimeContext{Axis: TemporalCurrent}, false},
		{"client, a 30-day range", suppliedInterpretedPath, TimeContext{Axis: TemporalRange, Start: &start, End: &now}, false},
		{"client, a 7-day range against the stated 30 days", suppliedInterpretedPath, TimeContext{Axis: TemporalRange, Start: &week, End: &now}, true},
		{"server interpreter, current axis", serverInterpretedPath, TimeContext{Axis: TemporalCurrent}, false},
	} {
		interpreted, frame := periodShapeInterpreted([]InvestigationGoal{GoalDescribeTrend}, TemporalIntentTimeSeries, []FactKind{FactFlow}, tc.time)
		run := runPeriodShapeTurn(t, tc.path, seriesQuestion, interpreted, frame)
		read := run.primaryFactTime(t)
		if read.Axis != TemporalRange || !sameInstant(read.Start, start) || !sameInstant(read.End, now) {
			t.Errorf("%s: fact read %s %v..%v, want the range axis over the stated 30 days %s..%s", tc.name, read.Axis, read.Start, read.End, start, now)
		}
		if axis := run.result.Interpretation.TimeContext.Axis; axis != TemporalRange {
			t.Errorf("%s: served axis %s, want range", tc.name, axis)
		}
		if run.result.EffectiveEvidenceWindow != nil {
			t.Errorf("%s: a series answer carries the current-state window %+v", tc.name, run.result.EffectiveEvidenceWindow)
		}
		if got := limitationsContain(run.result.Limitations, "which is not the period the question states"); got != tc.conflict {
			t.Errorf("%s: limitations %q, want the range conflict disclosed=%v", tc.name, run.result.Limitations, tc.conflict)
		}
		if limitationsContain(run.result.Limitations, "compares two periods") {
			t.Errorf("%s: a series answer carries the comparison disclosure: %q", tc.name, run.result.Limitations)
		}
	}
}

// A comparison of two periods reads the stated period on the range axis and
// says that the period it is compared with was not read: it is never one
// current window that drops the second period without a word.
func TestPeriodComparisonFrameReadsTheStatedPeriodAndNamesTheUnreadOne(t *testing.T) {
	day := 24 * time.Hour
	now := suppliedRangeRigNow
	start := now.Add(-30 * day)
	wantUnread := comparisonPeriodUnreadLimitation(start, now)
	for _, tc := range []struct {
		name     string
		path     interpreterPath
		question string
		facts    []FactKind
		time     TimeContext
	}{
		{"client range, the period read from the client's range", suppliedInterpretedPath, comparisonQuestion, burdenComposite, TimeContext{Axis: TemporalRange, Start: &start, End: &now}},
		{"client current axis, the stated trailing phrase", suppliedInterpretedPath, measureComparisonQuestion, []FactKind{FactFlow}, TimeContext{Axis: TemporalCurrent}},
		{"client range, the stated trailing phrase", suppliedInterpretedPath, measureComparisonQuestion, []FactKind{FactFlow}, TimeContext{Axis: TemporalRange, Start: &start, End: &now}},
		{"server interpreter, the stated trailing phrase", serverInterpretedPath, measureComparisonQuestion, []FactKind{FactFlow}, TimeContext{Axis: TemporalCurrent}},
	} {
		interpreted, frame := periodShapeInterpreted([]InvestigationGoal{GoalAssessState}, TemporalIntentPeriodComparison, tc.facts, tc.time)
		run := runPeriodShapeTurn(t, tc.path, tc.question, interpreted, frame)
		read := run.primaryFactTime(t)
		if read.Axis != TemporalRange || !sameInstant(read.Start, start) || !sameInstant(read.End, now) {
			t.Errorf("%s: fact read %s %v..%v, want the range axis over the stated period %s..%s", tc.name, read.Axis, read.Start, read.End, start, now)
		}
		if run.result.EffectiveEvidenceWindow != nil {
			t.Errorf("%s: a comparison answer carries one current-state window %+v", tc.name, run.result.EffectiveEvidenceWindow)
		}
		if !limitationsContain(run.result.Limitations, wantUnread) {
			t.Errorf("%s: limitations %q, want %q", tc.name, run.result.Limitations, wantUnread)
		}
	}
}

// A comparison question whose period the binder cannot bind and whose client
// sent no range states no period this rule can read: it keeps the window gate.
func TestPeriodComparisonWithNoReadablePeriodKeepsTheWindowGate(t *testing.T) {
	interpreted, frame := periodShapeInterpreted([]InvestigationGoal{GoalAssessState}, TemporalIntentPeriodComparison, burdenComposite, TimeContext{Axis: TemporalCurrent})
	run := runPeriodShapeTurn(t, suppliedInterpretedPath, comparisonQuestion, interpreted, frame)
	if run.result.Status != InvestigationClarificationRequired || len(run.factTimes) != 0 {
		t.Errorf("status=%s fact reads=%d, want the window gate and no read", run.result.Status, len(run.factTimes))
	}
}

// A synthesis that fills the limitation cap with its own caveats cannot push
// the comparison disclosure or the range-conflict disclosure out: both are
// service disclosures.
func TestPeriodComparisonDisclosuresSurviveAFullLimitationList(t *testing.T) {
	day := 24 * time.Hour
	now := suppliedRangeRigNow
	start := now.Add(-30 * day)
	week := now.Add(-7 * day)
	interpreted, frame := periodShapeInterpreted([]InvestigationGoal{GoalAssessState}, TemporalIntentPeriodComparison, []FactKind{FactFlow}, TimeContext{Axis: TemporalRange, Start: &week, End: &now})
	run := runPeriodShapeTurnWith(t, suppliedInterpretedPath, measureComparisonQuestion, interpreted, frame, periodShapeTurnOptions{caveats: contractsv1.ContextFabricLimitationsMaxCount})
	if !limitationsContain(run.result.Limitations, comparisonPeriodUnreadLimitation(start, now)) || !limitationsContain(run.result.Limitations, "which is not the period the question states") {
		t.Errorf("limitations %q, want the comparison and the range-conflict disclosures kept", run.result.Limitations)
	}
	if run.result.LimitationsDisplaced == 0 {
		t.Error("a cap-full synthesis displaced nothing: the fixture did not fill the cap")
	}
}

// The rule reads a period only where the caller left the time to the
// question: on the MCP surface, on a current-axis request, with no evidence
// window committed, and never for a point-in-time phrase.
func TestStatedPeriodReadKeepsTheCallersOwnTime(t *testing.T) {
	day := 24 * time.Hour
	now := suppliedRangeRigNow
	series := func() (InterpretedQuestion, *QuestionFrame) {
		return periodShapeInterpreted([]InvestigationGoal{GoalDescribeTrend}, TemporalIntentTimeSeries, []FactKind{FactFlow}, TimeContext{Axis: TemporalCurrent})
	}
	interpreted, frame := series()
	run := runPeriodShapeTurnWith(t, suppliedInterpretedPath, seriesQuestion, interpreted, frame, periodShapeTurnOptions{surface: "api"})
	if read := run.primaryFactTime(t); read.Axis == TemporalRange {
		t.Errorf("api surface: fact read on %s %v..%v, want the caller's axis kept", read.Axis, read.Start, read.End)
	}

	interpreted, frame = series()
	run = runPeriodShapeTurnWith(t, suppliedInterpretedPath, seriesQuestion, interpreted, frame, periodShapeTurnOptions{request: func(r *InvestigationRequest) {
		r.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing90D}
	}})
	if read, w := run.primaryFactTime(t), run.result.EffectiveEvidenceWindow; read.Axis != TemporalCurrent || w == nil || w.RelativeID != RelativeWindowTrailing90D {
		t.Errorf("evidence_window field: fact read on %s, window %+v, want the committed trailing_90d window on the current axis", read.Axis, w)
	}

	asOf := now.Add(-10 * day)
	interpreted, frame = series()
	interpreted.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &asOf}
	run = runPeriodShapeTurnWith(t, suppliedInterpretedPath, seriesQuestion, interpreted, frame, periodShapeTurnOptions{request: func(r *InvestigationRequest) {
		r.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &asOf}
	}})
	if read := run.primaryFactTime(t); read.Axis != TemporalValidTime {
		t.Errorf("as-of request: fact read on %s %v..%v, want the caller's as-of axis kept", read.Axis, read.Start, read.End)
	}

	interpreted, frame = series()
	run = runPeriodShapeTurn(t, suppliedInterpretedPath, "How had the Ask Dev project's throughput changed as of the start of the last 30 days?", interpreted, frame)
	if read := run.primaryFactTime(t); read.Axis == TemporalRange {
		t.Errorf("point-in-time phrase: fact read on %s %v..%v, want no stated range", read.Axis, read.Start, read.End)
	}

	start := now.Add(-30 * day)
	binder := ProposeWindowFromSpans("How did the Ask Dev project's throughput change last month?")
	fresh := TimeContext{Axis: TemporalRange, Start: &start, End: &now}
	for _, temporal := range []TemporalIntent{TemporalIntentTimeSeries, TemporalIntentPeriodComparison} {
		frame := frameWith([]InvestigationGoal{GoalDescribeTrend}, namedExpression(SubjectProject), temporal, nil)
		if window := interpreterPeriodWindow(requestWindowCanonicalization{BinderProposal: binder}, &frame, TemporalCurrent, fresh, true, mcpSurface); window != nil {
			t.Errorf("%s frame: interpreterPeriodWindow committed one current-state window %+v", temporal, window)
		}
	}
}
