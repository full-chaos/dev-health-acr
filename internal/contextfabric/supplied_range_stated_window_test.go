package contextfabric

import (
	"context"
	"testing"
	"time"
)

func TestQuestionStatesWindowIsTheBoundTrailingPhraseAlone(t *testing.T) {
	cases := []struct {
		outcome WindowBindOutcome
		want    bool
	}{
		{WindowBindOutcome{Reason: WindowBindRoutedInferred, Trailing: true}, true},
		{WindowBindOutcome{Reason: WindowBindRoutedInferred, Trailing: false}, false},
		{WindowBindOutcome{Reason: WindowBindSpanUnbound, Trailing: true}, false},
	}
	for _, tc := range cases {
		if got := questionStatesWindow(tc.outcome); got != tc.want {
			t.Errorf("questionStatesWindow(%+v) = %v, want %v", tc.outcome, got, tc.want)
		}
	}
	canon := requestWindowCanonicalization{BinderProposal: WindowBindOutcome{Reason: WindowBindRoutedInferred, Trailing: true}}
	if got := statedWindowOrigin(canon, TemporalCurrent, mcpSurface); got != StatedWindowOriginQuestionPhrase {
		t.Errorf("a trailing phrase on MCP: origin %q, want %q", got, StatedWindowOriginQuestionPhrase)
	}
}

// runSuppliedRangeCase sends a client interpretation of family on a 30-day
// range axis with the given window class.
func runSuppliedRangeCase(t *testing.T, family QuestionFamily, class WindowClass, question, surface string) InvestigationResult {
	t.Helper()
	now := time.Now().UTC()
	start := now.Add(-30 * 24 * time.Hour)
	receipt := clientInterpretReceipt()
	receipt.QuestionFamily = family
	interpreted := suppliedInterpretation()
	interpreted.TimeContext = TimeContext{Axis: TemporalRange, Start: &start, End: &now}
	interpreted.WindowClass = class
	if class != "" {
		interpreted.WindowConfidence = WindowConfidenceHigh
	}
	rig := newSuppliedEngineRig(t, RuntimeQuestionInterpreter{Supplied: &scriptedSuppliedRuntime{interpreted: interpreted, receipt: receipt}})
	request := suppliedInvestigationRequest(validInvestigationRequest())
	request.Question = question
	request.Consumer.Surface = surface
	result, err := rig.engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("%s/%s %q: %v", family, class, question, err)
	}
	return result
}

// A client that read a stated trailing period as a range runs on the current
// axis with the question_stated window, for every family and window class,
// as the server interpretation of the same phrase does.
func TestSuppliedRangeWithAStatedTrailingPhraseRunsOnTheCurrentAxis(t *testing.T) {
	for _, family := range QuestionFamilyVocabulary() {
		for _, class := range []WindowClass{WindowClassExplicitWindow, WindowClassStateSnapshot, WindowClassRecentActivityLookup, ""} {
			result := runSuppliedRangeCase(t, family, class, "How is the Ask Dev project doing in the last 30 days?", mcpSurface)
			window := result.EffectiveEvidenceWindow
			if result.Interpretation.TimeContext.Axis != TemporalCurrent || window == nil || window.Provenance != WindowQuestionStated || window.RelativeID != RelativeWindowTrailing30D {
				t.Errorf("%s/%q: axis=%s window=%+v, want current with the question_stated trailing_30d window", family, class, result.Interpretation.TimeContext.Axis, window)
			}
		}
	}
	// No period phrase, or another surface: the client's range stands.
	for _, tc := range []struct{ question, surface string }{
		{"How is the Ask Dev project doing?", mcpSurface},
		{"How is the Ask Dev project doing in the last 30 days?", "api"},
	} {
		result := runSuppliedRangeCase(t, QuestionFamilySubjectInvestigation, WindowClassExplicitWindow, tc.question, tc.surface)
		if result.Interpretation.TimeContext.Axis != TemporalRange {
			t.Errorf("%q on %s: axis=%s, want the supplied range kept", tc.question, tc.surface, result.Interpretation.TimeContext.Axis)
		}
	}
}

// The work-item member shape a client sent on prod: a current status frame,
// status done, a 30-day range and the role word in the question text.
func TestSuppliedStatusRangeServesWorkItemMembersOnTheFullPath(t *testing.T) {
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
