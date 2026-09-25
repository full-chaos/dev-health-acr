package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-6557 (prod rev15, acr 7705e36c, 2026-09-25 01:22Z, raw-prod-rev14
// 15-g1.json): {"question":"Which teams need attention over the last 30
// days?","evidence_window":{"relative_id":"trailing_30d"}} on the MCP
// surface returned clarification_required with effective_evidence_window
// trailing_30d / inferred_default. The window was named twice by the caller
// and still gated: windowExplicitProvenance (window.go) downgraded an MCP
// explicit field to inferred_default (DP12(b)), canonicalizeEvidenceWindow
// turned that into ExplicitUnconfirmed, and Investigate's gate 1
// (engine.go) intercepted it before Interpret. The question-phrase channel
// gated the same way: composeEffectiveWindow kept a binder-routed span at
// inferred_default, so gate 2 fired.
//
// Ruling (chris, 2026-09-25): a window the caller SUPPLIED -- the
// evidence_window field or an explicit phrase in the question -- is a
// COMMITTED window: no clarification, the answer reads that window and
// reports it. The confirmation turn stays for the INFERRED case (class-table
// default with no stated period).

type explicitWindowRun struct {
	result     InvestigationResult
	factRead   bool
	factWindow *contractsv1.ContextFabricRequestedEvidenceWindow
	// resolvedAxes is the axis each ResolveSubjects call received.
	resolvedAxes []contractsv1.ContextFabricTemporalAxis
}

func runExplicitWindowCase(t *testing.T, surface, question string, field *contractsv1.ContextFabricRequestedEvidenceWindow) explicitWindowRun {
	t.Helper()
	return runExplicitWindowCaseWith(t, surface, question, field, bootstrapInterpretation())
}

func runExplicitWindowCaseWith(t *testing.T, surface, question string, field *contractsv1.ContextFabricRequestedEvidenceWindow, interpretation InterpretedQuestion) explicitWindowRun {
	t.Helper()
	project := acceptanceProject()
	var run explicitWindowRun
	facts := factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
		run.factRead = true
		run.factWindow = request.Question.TimeContext.EvidenceWindow
		return bootstrapFactBundle(project), nil
	})
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	engine := buildAcceptanceEngine(t, graph, facts, interpretation, bootstrapDraft(project), newMapResultStore())

	request := validInvestigationRequest()
	request.Question = question
	request.Consumer.Surface = surface
	request.TimeContext.EvidenceWindow = field

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

func assertCommittedWindow(t *testing.T, run explicitWindowRun, want RelativeWindowID) {
	t.Helper()
	result := run.result
	if result.Status == InvestigationClarificationRequired {
		t.Fatalf("Status = clarification_required (window_clarification=%v), want an answer: a supplied window is committed", result.WindowClarification != nil)
	}
	if result.WindowClarification != nil {
		t.Fatalf("WindowClarification = %#v, want nil: nothing to confirm on a supplied window", result.WindowClarification)
	}
	if result.StructureNeeds != nil {
		for _, missing := range result.StructureNeeds.Missing {
			if missing == contractsv1.ContextFabricStructureNeedWindow {
				t.Fatalf("StructureNeeds.Missing = %#v, want no window need on a supplied window", result.StructureNeeds.Missing)
			}
		}
	}
	window := result.EffectiveEvidenceWindow
	if window == nil {
		t.Fatal("EffectiveEvidenceWindow = nil, want the supplied window reported")
	}
	if window.RelativeID != want {
		t.Fatalf("EffectiveEvidenceWindow.RelativeID = %q, want %q", window.RelativeID, want)
	}
	if window.Provenance != WindowQuestionStated {
		t.Fatalf("EffectiveEvidenceWindow.Provenance = %q, want %q", window.Provenance, WindowQuestionStated)
	}
	if !run.factRead {
		t.Fatal("the canonical fact read never ran: the answer did not serve the supplied window")
	}
	if run.factWindow == nil || run.factWindow.RelativeID != want {
		t.Fatalf("fact-read window = %#v, want relative_id %q: the answer must read the window it reports", run.factWindow, want)
	}
}

// The exact raw-prod-rev14 15-g1 request shape: question phrase AND field.
func TestCHAOS6557_ExplicitFieldCommits_ProdShape(t *testing.T) {
	t.Parallel()
	run := runExplicitWindowCase(t, "mcp", "Which teams need attention over the last 30 days?",
		&contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D})
	assertCommittedWindow(t, run, RelativeWindowTrailing30D)
}

// The field alone, with no phrase to lean on, on every surface.
func TestCHAOS6557_ExplicitFieldCommits_AnySurface(t *testing.T) {
	t.Parallel()
	for _, surface := range []string{"mcp", "workbench"} {
		for _, id := range []RelativeWindowID{RelativeWindowTrailing30D, RelativeWindowTrailing90D, RelativeWindowTrailing365D, RelativeWindowAllTime} {
			t.Run(surface+"/"+string(id), func(t *testing.T) {
				t.Parallel()
				run := runExplicitWindowCase(t, surface, validInvestigationRequest().Question,
					&contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: id})
				assertCommittedWindow(t, run, id)
			})
		}
	}
}

// The field wins over a question phrase that names another width.
func TestCHAOS6557_ExplicitFieldWinsOverQuestionPhrase(t *testing.T) {
	t.Parallel()
	run := runExplicitWindowCase(t, "mcp", "Which teams need attention over the last 30 days?",
		&contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing90D})
	assertCommittedWindow(t, run, RelativeWindowTrailing90D)
}

// The exact raw-prod-rev14 02-q1 request shape: phrase only, no field.
func TestCHAOS6557_ExplicitPhraseCommits_ProdShape(t *testing.T) {
	t.Parallel()
	for _, surface := range []string{"mcp", "workbench"} {
		t.Run(surface, func(t *testing.T) {
			t.Parallel()
			run := runExplicitWindowCase(t, surface, "What is the team investment mix over the last 30 days?", nil)
			assertCommittedWindow(t, run, RelativeWindowTrailing30D)
		})
	}
}

// The other direction: no supplied period, so the window IS inferred and the
// confirmation turn (winr_ receipts) stays. An ambiguous phrase (two spans)
// names no single window and stays inferred too.
func TestCHAOS6557_InferredWindowStillClarifies(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"no period stated":       validInvestigationRequest().Question,
		"two conflicting spans":  "Compare the last 30 days with the last 90 days for the team.",
		"width outside registry": "What shipped in the last 14 days?",
	}
	for name, question := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run := runExplicitWindowCase(t, "mcp", question, nil)
			result := run.result
			if result.Status != InvestigationClarificationRequired {
				t.Fatalf("Status = %q, want clarification_required for an inferred window", result.Status)
			}
			if result.WindowClarification == nil || len(result.WindowClarification.Options) == 0 {
				t.Fatal("WindowClarification is nil or empty, want winr_ receipt-bound options")
			}
			if result.EffectiveEvidenceWindow == nil || result.EffectiveEvidenceWindow.Provenance != WindowInferredDefault {
				t.Fatalf("EffectiveEvidenceWindow = %#v, want a disclosed inferred_default window", result.EffectiveEvidenceWindow)
			}
			if run.factRead {
				t.Fatal("canonical fact read ran for an unconfirmed inferred window")
			}
		})
	}
}

// driftedInterpretation is what the interpreter returned for prod q2 (raw
// 04-q2.json temporal.effective): it read "last month" as a calendar RANGE
// axis although the caller's request is current-state.
func driftedInterpretation(axis contractsv1.ContextFabricTemporalAxis) InterpretedQuestion {
	interpretation := bootstrapInterpretation()
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	switch axis {
	case contractsv1.ContextFabricTemporalRange:
		interpretation.TimeContext = TimeContext{Axis: TemporalRange, Start: &start, End: &end}
	default:
		interpretation.TimeContext = TimeContext{Axis: axis, AsOf: &start}
	}
	return interpretation
}

func assertResolvedOnCurrentAxis(t *testing.T, run explicitWindowRun) {
	t.Helper()
	if len(run.resolvedAxes) == 0 {
		t.Fatal("ResolveSubjects never ran")
	}
	for _, axis := range run.resolvedAxes {
		if axis != contractsv1.ContextFabricTemporalCurrent {
			t.Fatalf("ResolveSubjects received axis %q, want current: a committed window governs its own axis, the sampled interpreter axis is a diagnostic", axis)
		}
	}
}

// On the MCP surface a caller-supplied window is the caller's time (the
// published tool contract, chris 2026-09-25: a period is an evidence window
// over current state). When the interpreter samples the period as a RANGE axis
// the window still governs: the turn executes on the current axis under it,
// never vetoed as an axis conflict and never resolved on the sampled range.
func TestCHAOS6557_CommittedFieldWindowGovernsItsAxisOnMCP(t *testing.T) {
	t.Parallel()
	run := runExplicitWindowCaseWith(t, "mcp", "Which teams need attention over the last 30 days?",
		&contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D}, driftedInterpretation(contractsv1.ContextFabricTemporalRange))
	assertCommittedWindow(t, run, RelativeWindowTrailing30D)
	assertResolvedOnCurrentAxis(t, run)
}

// Control: an explicit as-of instant is a genuine historical question, and
// the fresh-axis rule (window axis design of record) still governs it on MCP:
// a supplied window plus "state as of ..." is a disclosed axis conflict, and
// with no window supplied the as-of axis is kept untouched.
func TestCHAOS6557_ExplicitAsOfStillFollowsTheFreshAxisOnMCP(t *testing.T) {
	t.Parallel()
	for _, axis := range []contractsv1.ContextFabricTemporalAxis{contractsv1.ContextFabricTemporalValidTime, contractsv1.ContextFabricTemporalObservedTime} {
		t.Run(string(axis)+"/with window", func(t *testing.T) {
			t.Parallel()
			run := runExplicitWindowCaseWith(t, "mcp", "What was the state of the team as of March 3?",
				&contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D}, driftedInterpretation(axis))
			if run.result.Status != InvestigationNoMatch || !axisConflictLimitationServed(run.result) {
				t.Fatalf("status=%q limitations=%q, want the axis-conflict veto for an explicit as-of", run.result.Status, run.result.Limitations)
			}
		})
		t.Run(string(axis)+"/no window", func(t *testing.T) {
			t.Parallel()
			run := runExplicitWindowCaseWith(t, "mcp", "What was the state of the team as of March 3?", nil, driftedInterpretation(axis))
			if len(run.resolvedAxes) == 0 || run.resolvedAxes[0] != axis {
				t.Fatalf("resolved axes = %v, want the interpreted %s axis untouched", run.resolvedAxes, axis)
			}
			if run.result.EffectiveEvidenceWindow != nil {
				t.Fatalf("EffectiveEvidenceWindow = %#v, want nil for an as-of question", run.result.EffectiveEvidenceWindow)
			}
		})
	}
}

// Other direction: every other surface keeps CHAOS-5582's rule -- a stated
// window with no receipt follows the fresh interpretation, so a drifted axis
// is named as an axis conflict rather than overridden.
func TestCHAOS6557_OtherSurfacesKeepTheAxisConflictVeto(t *testing.T) {
	t.Parallel()
	run := runExplicitWindowCaseWith(t, "workbench", "Which teams need attention over the last 30 days?",
		&contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D}, driftedInterpretation(contractsv1.ContextFabricTemporalRange))
	if run.result.Status != InvestigationNoMatch || !axisConflictLimitationServed(run.result) {
		t.Fatalf("status=%q limitations=%q, want the axis-conflict veto on a non-MCP surface", run.result.Status, run.result.Limitations)
	}
}

// chris 2026-09-25: "in the last month" is a TRAILING window (30 days back
// from now), even when the interpreter samples it as the previous calendar
// month. The committed window is the trailing one -- exact bounds asserted --
// executed on the current axis, and reported as question_stated.
func TestCHAOS6557_TrailingPhraseCommitsTheTrailingBoundsOnMCP(t *testing.T) {
	t.Parallel()
	for _, question := range []string{
		"What is the team investment mix in the last month?",
		"What is the team investment mix over the last 30 days?",
	} {
		run := runExplicitWindowCaseWith(t, "mcp", question, nil, driftedInterpretation(contractsv1.ContextFabricTemporalRange))
		assertCommittedWindow(t, run, RelativeWindowTrailing30D)
		assertResolvedOnCurrentAxis(t, run)
		now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
		window := run.result.EffectiveEvidenceWindow
		if !window.End.Equal(now) || !window.Start.Equal(now.AddDate(0, 0, -30)) {
			t.Fatalf("%q: window = %v..%v, want the trailing %v..%v", question, window.Start, window.End, now.AddDate(0, 0, -30), now)
		}
	}
}

// prod q2 (raw 04-q2.json), exact question: a bare "last month" is the
// previous CALENDAR month (chris 2026-09-25), which the closed trailing
// grammar cannot bound; the interpreter's calendar range becomes the committed
// window -- exact bounds asserted, question_stated, current axis, never the
// trailing 30 days.
func TestCHAOS6557_BareLastMonthCommitsTheCalendarBoundsOnMCP(t *testing.T) {
	t.Parallel()
	// The first is prod q2 verbatim (the binder refuses its "and why" tail); the
	// second is bound by the binder as a bare, non-trailing "last month" -- the
	// engine must still commit the interpreter's calendar bounds, not trailing_30d.
	for _, question := range []string{
		"Which repository carried the most operational/support work last month and why?",
		"Which repository carried the most operational/support work last month?",
	} {
		interpretation := driftedInterpretation(contractsv1.ContextFabricTemporalRange)
		run := runExplicitWindowCaseWith(t, "mcp", question, nil, interpretation)
		result := run.result
		if result.Status == InvestigationClarificationRequired || result.WindowClarification != nil {
			t.Fatalf("%q: status=%q window_clarification=%v, want an answer: the caller stated the period", question, result.Status, result.WindowClarification != nil)
		}
		window := result.EffectiveEvidenceWindow
		if window == nil || window.Provenance != WindowQuestionStated || window.RelativeID != "" || window.Start == nil || window.End == nil ||
			!window.Start.Equal(*interpretation.TimeContext.Start) || !window.End.Equal(*interpretation.TimeContext.End) {
			t.Fatalf("%q: EffectiveEvidenceWindow = %#v, want question_stated over the calendar %v..%v", question, window, interpretation.TimeContext.Start, interpretation.TimeContext.End)
		}
		if !run.factRead || run.factWindow == nil || run.factWindow.Start == nil || !run.factWindow.Start.Equal(*window.Start) || !run.factWindow.End.Equal(*window.End) {
			t.Fatalf("%q: fact-read window = %#v, want the reported bounds", question, run.factWindow)
		}
		assertResolvedOnCurrentAxis(t, run)
	}
}

// A period the binder cannot bind ("in July") that the interpreter reads as a
// calendar range. On MCP that range IS the evidence window: committed
// (question_stated), bounds from the interpreter, current axis, and the fact
// read receives the same bounds.
func TestCHAOS6557_InterpreterRangeBecomesTheCommittedWindowOnMCP(t *testing.T) {
	t.Parallel()
	interpretation := driftedInterpretation(contractsv1.ContextFabricTemporalRange)
	run := runExplicitWindowCaseWith(t, "mcp", "Which repository carried the most operational/support work in July?", nil, interpretation)
	result := run.result
	if result.Status == InvestigationClarificationRequired || result.WindowClarification != nil {
		t.Fatalf("status=%q window_clarification=%v, want an answer: the caller stated the period", result.Status, result.WindowClarification != nil)
	}
	window := result.EffectiveEvidenceWindow
	if window == nil || window.Provenance != WindowQuestionStated || window.Start == nil || window.End == nil ||
		!window.Start.Equal(*interpretation.TimeContext.Start) || !window.End.Equal(*interpretation.TimeContext.End) {
		t.Fatalf("EffectiveEvidenceWindow = %#v, want question_stated over the interpreter's %v..%v", window, interpretation.TimeContext.Start, interpretation.TimeContext.End)
	}
	if !run.factRead || run.factWindow == nil || run.factWindow.Start == nil || !run.factWindow.Start.Equal(*window.Start) || !run.factWindow.End.Equal(*window.End) {
		t.Fatalf("fact-read window = %#v, want the reported bounds", run.factWindow)
	}
	assertResolvedOnCurrentAxis(t, run)
}

// Other direction: outside MCP the interpreter's range keeps its historical
// meaning, resolved on the range axis, with no window reported.
func TestCHAOS6557_InterpreterRangeOutsideMCPStaysHistorical(t *testing.T) {
	t.Parallel()
	run := runExplicitWindowCaseWith(t, "workbench", "Which repository carried the most operational/support work last month and why?", nil,
		driftedInterpretation(contractsv1.ContextFabricTemporalRange))
	if len(run.resolvedAxes) == 0 || run.resolvedAxes[0] != contractsv1.ContextFabricTemporalRange {
		t.Fatalf("resolved axes = %v, want the interpreted range axis untouched off MCP", run.resolvedAxes)
	}
	if run.result.EffectiveEvidenceWindow != nil {
		t.Fatalf("EffectiveEvidenceWindow = %#v, want nil", run.result.EffectiveEvidenceWindow)
	}
}

// Other direction: no window supplied off MCP, so the interpreter's historical
// axis is honoured exactly as before -- it resolves on the range axis, nothing
// is committed and nothing is overridden.
func TestCHAOS6557_NoSuppliedWindowKeepsTheInterpretedHistoricalAxis(t *testing.T) {
	t.Parallel()
	run := runExplicitWindowCaseWith(t, "workbench", validInvestigationRequest().Question, nil, driftedInterpretation(contractsv1.ContextFabricTemporalRange))
	if len(run.resolvedAxes) == 0 || run.resolvedAxes[0] != contractsv1.ContextFabricTemporalRange {
		t.Fatalf("resolved axes = %v, want the interpreted range axis untouched", run.resolvedAxes)
	}
	if run.result.EffectiveEvidenceWindow != nil {
		t.Fatalf("EffectiveEvidenceWindow = %#v, want nil on a historical axis with nothing supplied", run.result.EffectiveEvidenceWindow)
	}
}

// The decision is loud: one line per supplied-window turn naming the
// interpreted axis, the executed axis and the outcome, with a zero-override
// (agreed) control so the override rate has a denominator.
func TestCHAOS6557_StatedWindowAxisDecisionIsRecorded(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		question string
		field    *contractsv1.ContextFabricRequestedEvidenceWindow
		axis     contractsv1.ContextFabricTemporalAxis
		want     statedWindowAxisRecord
	}{
		{"field drifted", "Which teams need attention?", &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D}, contractsv1.ContextFabricTemporalRange,
			statedWindowAxisRecord{"mcp", StatedWindowOriginField, TemporalRange, TemporalCurrent, ContinuationAxisOverriddenByReceipt}},
		{"field agreed", "Which teams need attention?", &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D}, contractsv1.ContextFabricTemporalCurrent,
			statedWindowAxisRecord{"mcp", StatedWindowOriginField, TemporalCurrent, TemporalCurrent, ContinuationAxisAgreed}},
		{"interpreter range", "Which repository carried the most operational/support work in July?", nil, contractsv1.ContextFabricTemporalRange,
			statedWindowAxisRecord{"mcp", StatedWindowOriginInterpreterRange, TemporalRange, TemporalCurrent, ContinuationAxisOverriddenByReceipt}},
		{"prod q2 bare last month", "Which repository carried the most operational/support work last month and why?", nil, contractsv1.ContextFabricTemporalRange,
			statedWindowAxisRecord{"mcp", StatedWindowOriginInterpreterRange, TemporalRange, TemporalCurrent, ContinuationAxisOverriddenByReceipt}},
		{"phrase drifted", "What is the team investment mix in the last month?", nil, contractsv1.ContextFabricTemporalRange,
			statedWindowAxisRecord{"mcp", StatedWindowOriginQuestionPhrase, TemporalRange, TemporalCurrent, ContinuationAxisOverriddenByReceipt}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			telemetry := &recordingTelemetry{}
			project := acceptanceProject()
			facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
				return bootstrapFactBundle(project), nil
			})
			graph := &acceptanceGraphReader{
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
				context:    bootstrapGraphContext(project),
			}
			interpretation := driftedInterpretation(tc.axis)
			if tc.axis == contractsv1.ContextFabricTemporalCurrent {
				interpretation = bootstrapInterpretation()
			}
			engine := buildAcceptanceEngineWithTelemetry(t, graph, facts, interpretation, bootstrapDraft(project), newMapResultStore(), telemetry)
			request := validInvestigationRequest()
			request.Question = tc.question
			request.Consumer.Surface = "mcp"
			request.TimeContext.EvidenceWindow = tc.field
			if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if len(telemetry.statedWindowAxes) != 1 || telemetry.statedWindowAxes[0] != tc.want {
				t.Fatalf("stated window axis records = %#v, want [%#v]", telemetry.statedWindowAxes, tc.want)
			}
		})
	}
	// Nothing supplied: no line at all.
	telemetry := &recordingTelemetry{}
	project := acceptanceProject()
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bootstrapFactBundle(project), nil
	})
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}, context: bootstrapGraphContext(project)}
	engine := buildAcceptanceEngineWithTelemetry(t, graph, facts, bootstrapInterpretation(), bootstrapDraft(project), newMapResultStore(), telemetry)
	request := validInvestigationRequest()
	request.Consumer.Surface = "mcp"
	if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(telemetry.statedWindowAxes) != 0 {
		t.Fatalf("stated window axis records = %#v, want none when nothing was supplied", telemetry.statedWindowAxes)
	}
}

// A caller who asked for a historical axis keeps it: a period phrase in the
// question is not a window commitment there (windows are representable only
// on the current axis), so no decision line is emitted and nothing is
// overridden.
func TestCHAOS6557_PhraseOnAHistoricalRequestIsNotCommitted(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	project := acceptanceProject()
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bootstrapFactBundle(project), nil
	})
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}, context: bootstrapGraphContext(project)}
	asOf := historicalAsOf()
	interpretation := bootstrapInterpretation()
	interpretation.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &asOf}
	engine := buildAcceptanceEngineWithTelemetry(t, graph, facts, interpretation, bootstrapDraft(project), newMapResultStore(), telemetry)
	request := validInvestigationRequest()
	request.Question = "What is the team investment mix over the last 30 days?"
	request.Consumer.Surface = "mcp"
	request.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &asOf}
	if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(telemetry.statedWindowAxes) != 0 {
		t.Fatalf("stated window axis records = %#v, want none on a historical request", telemetry.statedWindowAxes)
	}
	if len(graph.resolveInterpretations) == 0 || graph.resolveInterpretations[0].TimeContext.Axis != TemporalValidTime {
		t.Fatalf("resolved interpretations = %#v, want the valid_time axis kept", graph.resolveInterpretations)
	}
}

// A bare "last month" that the interpreter leaves on the current axis has no
// bounds the trailing grammar may commit (it names the previous calendar
// month): it stays an inferred proposal and the confirmation turn stands,
// while the preposition-led control commits.
func TestCHAOS6557_BareLastMonthWithoutARangeStaysAnInferredProposal(t *testing.T) {
	t.Parallel()
	bare := runExplicitWindowCase(t, "mcp", "Which repository carried the most operational/support work last month?", nil)
	if bare.result.Status != InvestigationClarificationRequired || bare.result.WindowClarification == nil {
		t.Fatalf("bare: status=%q window_clarification=%v, want the confirmation turn", bare.result.Status, bare.result.WindowClarification != nil)
	}
	if w := bare.result.EffectiveEvidenceWindow; w == nil || w.Provenance != WindowInferredDefault {
		t.Fatalf("bare: EffectiveEvidenceWindow = %#v, want an inferred_default proposal", w)
	}
	led := runExplicitWindowCase(t, "mcp", "Which repository carried the most operational/support work in the last month?", nil)
	assertCommittedWindow(t, led, RelativeWindowTrailing30D)
}
