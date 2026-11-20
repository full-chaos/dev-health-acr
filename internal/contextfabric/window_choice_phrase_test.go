package contextfabric

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestWindowPhraseNeedsChoice(t *testing.T) {
	t.Parallel()
	for question, want := range map[string]bool{
		"Which repository carried the most work for last month?":             true,
		"Which repository carried the most work over last month?":            true,
		"Which repository carried the most work during last month?":          true,
		"Which repository carried the most work within last month?":          true,
		"Which repository carried the most work last month?":                 false,
		"Which repository carried the most work in last month?":              false,
		"Which repository carried the most work for the last month?":         false,
		"Which repository carried the most work in the last month?":          false,
		"Which repository carried the most work for the past month?":         false,
		"Which repository carried the most work for last quarter?":           false,
		"Which repository carried the most work for last year?":              false,
		"What was the state as of the end of last month?":                    false,
		"Compare last month and last month for the team":                     false,
		"Which repository carried the most work for 30 days?":                false,
		"What changed for last month's release?":                             false,
		"How many incidents did we have during last month of the quarter?":   false,
		"Show deployments for last month through November 10.":               false,
		"Which repository carried the most work for last month, and why?":    false,
		"Which repository carried the most work for last month and why?":     false,
		"Which repository carried the most work for last month: top three":   false,
		"Show deployments for last month. Break them down by repository.":    true,
		"Compare deployment counts for last month and the month before.":     false,
		"Show deployment counts for last month and a half.":                  false,
		"Which repository carried the most work for last month and by repo?": false,
		"What shipped for last month? Break it down by repository.":          true,
		"What shipped for last month!":                                       true,
		"What shipped for last month.Break it down":                          false,
	} {
		if got := WindowPhraseNeedsChoice(question); got != want {
			t.Errorf("WindowPhraseNeedsChoice(%q) = %v, want %v", question, got, want)
		}
	}
	if !WindowPhraseIsCalendarMonth("Which repository carried the most work last month?") || WindowPhraseIsCalendarMonth("work in the last month") {
		t.Error("WindowPhraseIsCalendarMonth must hold for a bare last month and not for the article-led trailing form")
	}
}

func TestPreviousCalendarMonthLabel(t *testing.T) {
	t.Parallel()
	if got := PreviousCalendarMonthLabel(time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)); got != "December 2025" {
		t.Fatalf("label = %q", got)
	}
}

// Accepting trailing_30d sends it as the field window beside a question that
// says "for last month": the field wins, and no stated-range conflict is
// disclosed against the calendar month the phrase alone would have read.
func TestCalendarWindow_AcceptedTrailing30dBesideForLastMonthDisclosesNoRangeConflict(t *testing.T) {
	t.Parallel()
	for i, interpretation := range sampledInterpretations() {
		run := runCalendarCase(t, "mcp", "Which repository carried the most operational/support work for last month?",
			&contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D}, TemporalCurrent, interpretation)
		w := run.result.EffectiveEvidenceWindow
		if w == nil || w.RelativeID != RelativeWindowTrailing30D || w.Provenance != WindowQuestionStated {
			t.Fatalf("sample %d: window = %#v, want question_stated trailing_30d", i, w)
		}
		if limitationsContain(run.result.Limitations, "which is not the period the question states") {
			t.Fatalf("sample %d: a stated-range conflict was disclosed: %q", i, run.result.Limitations)
		}
		for _, l := range run.result.Limitations {
			if contractsv1.IsContextFabricStatedRangeConflictLimitation(l) {
				t.Fatalf("sample %d: stated-range conflict limitation %q", i, l)
			}
		}
	}
}
