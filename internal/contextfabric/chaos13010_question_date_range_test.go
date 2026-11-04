package contextfabric

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-13010: a question that states two ISO dates states a range. The live
// real model read "(2026-09-07 to 2026-10-07)" as time_context {axis: current}
// (its prompt copies only the request's bounds), so the read ran on the current
// axis and the landscape quadrant was refused again for different days.

func day(year int, month time.Month, d int) time.Time {
	return time.Date(year, month, d, 0, 0, 0, 0, time.UTC)
}

func TestAQuestionThatStatesTwoDatesStatesARange(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		question   string
		start, end time.Time
	}{
		"between and":                {"How do they compare between 2026-09-07 and 2026-10-07?", day(2026, 9, 7), day(2026, 10, 7)},
		"from to":                    {"Compare churn from 2026-09-07 to 2026-10-07.", day(2026, 9, 7), day(2026, 10, 7)},
		"in brackets after words":    {"for a window that ends Oct 7, 2026 (2026-09-07 to 2026-10-07)? Use the team linear:CHAOS", day(2026, 9, 7), day(2026, 10, 7)},
		"through":                    {"2026-09-07 through 2026-10-07 please", day(2026, 9, 7), day(2026, 10, 7)},
		"en dash":                    {"churn 2026-09-07 – 2026-10-07", day(2026, 9, 7), day(2026, 10, 7)},
		"one day":                    {"between 2026-10-07 and 2026-10-07", day(2026, 10, 7), day(2026, 10, 7)},
		"the same span stated twice": {"between 2026-09-07 and 2026-10-07 (2026-09-07 to 2026-10-07)", day(2026, 9, 7), day(2026, 10, 7)},
	} {
		start, end, ok := explicitQuestionDateRange(tc.question)
		wantEnd := tc.end.Add(24*time.Hour - time.Second)
		if !ok || !start.Equal(tc.start) || !end.Equal(wantEnd) {
			t.Errorf("%s: got %v %v %v; want %v %v", name, start, end, ok, tc.start, wantEnd)
		}
	}
}

// Check 12: what is not a clean span states none, and nothing is guessed.
func TestAQuestionThatStatesNoCleanSpanStatesNone(t *testing.T) {
	t.Parallel()
	for name, question := range map[string]string{
		"no dates":                   "How do Ops Team and Fullchaos compare on churn?",
		"one date":                   "what changed since 2026-09-07?",
		"a trailing phrase":          "over the last 30 days",
		"two different spans":        "between 2026-09-07 and 2026-10-07, and between 2026-01-01 and 2026-01-31",
		"three dates":                "2026-09-07 to 2026-10-07 to 2026-11-07",
		"end before start":           "between 2026-10-07 and 2026-09-07",
		"not a date":                 "between 2026-13-45 and 2026-14-01",
		"a date that does not exist": "between 2026-02-30 and 2026-03-05",
		"a version string":           "release 2026.09.07 to 2026.10.07",
		"month names only":           "between Sept 7 and Oct 7",
		"wider than the time axis":   "between 2024-01-01 and 2026-10-07",
	} {
		if start, end, ok := explicitQuestionDateRange(question); ok {
			t.Errorf("%s: read %v %v from %q", name, start, end, question)
		}
	}
}

func investigateWithQuestion(t *testing.T, question string, interpreted TimeContext, requestTime TimeContext, now time.Time) *historicalEngineProbe {
	t.Helper()
	engine, probe := mustHistoricalEngine(t, interpreted, now)
	request := validInvestigationRequest()
	request.Question = question
	request.TimeContext = requestTime
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !probe.factsRead {
		t.Fatal("no fact read")
	}
	return probe
}

func TestTheEngineReadsTheDatesAQuestionStatesAsARange(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	const question = "How do Ops Team and Fullchaos compare on churn for a window that ends Oct 7, 2026 (2026-09-07 to 2026-10-07)?"
	probe := investigateWithQuestion(t, question, TimeContext{Axis: TemporalCurrent}, TimeContext{Axis: TemporalCurrent}, now)
	got := probe.factContext
	if got.Axis != TemporalRange || got.Start == nil || got.End == nil || !got.Start.Equal(day(2026, 9, 7)) || !got.End.Equal(day(2026, 10, 7).Add(24*time.Hour-time.Second)) {
		t.Fatalf("fact read time %+v; want the range 2026-09-07 .. 2026-10-07 end of day", got)
	}
}

// The model's own range stands, a caller's own axis stands, and a future end is
// clamped as any interpreted range is.
func TestTheEngineKeepsATimeThatIsAlreadyStated(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	const question = "compare between 2026-09-07 and 2026-10-07"
	modelStart, modelEnd := day(2026, 8, 1), day(2026, 8, 31)
	probe := investigateWithQuestion(t, question, TimeContext{Axis: TemporalRange, Start: &modelStart, End: &modelEnd}, TimeContext{Axis: TemporalCurrent}, now)
	if got := probe.factContext; got.Start == nil || !got.Start.Equal(modelStart) || got.End == nil || !got.End.Equal(modelEnd) {
		t.Errorf("the model's range was replaced: %+v", got)
	}
	asOf := day(2026, 10, 1)
	probe = investigateWithQuestion(t, question, TimeContext{Axis: TemporalCurrent}, TimeContext{Axis: TemporalValidTime, AsOf: &asOf}, now)
	if got := probe.factContext; got.Axis == TemporalRange {
		t.Errorf("a caller's as-of axis was replaced by the question's dates: %+v", got)
	}
	probe = investigateWithQuestion(t, "compare between 2026-10-01 and 2026-12-31", TimeContext{Axis: TemporalCurrent}, TimeContext{Axis: TemporalCurrent}, now)
	if got := probe.factContext; got.Axis != TemporalRange || got.End == nil || got.End.After(now) {
		t.Errorf("a stated end in the future is not clamped to now: %+v", got)
	}
}

// The decision is on a line: the question's dates, the interpreted axis, the
// executed axis, and the stated-range outcome.
func TestTheStatedWindowLineSaysTheQuestionsDatesWereRead(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	telemetry := &recordingTelemetry{}
	engine, _ := mustHistoricalEngineWithTelemetry(t, TimeContext{Axis: TemporalCurrent}, now, telemetry)
	request := validInvestigationRequest()
	request.Question = "compare between 2026-09-07 and 2026-10-07"
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	var seen []statedWindowAxisRecord
	for _, record := range telemetry.statedWindowAxes {
		if record.Origin == StatedWindowOriginQuestionDates {
			seen = append(seen, record)
		}
	}
	if len(seen) != 1 || seen[0].Interpreted != TemporalCurrent || seen[0].Executed != TemporalRange || seen[0].Outcome != StatedWindowAxisStatedRange {
		t.Fatalf("lines %+v", telemetry.statedWindowAxes)
	}
	quiet := &recordingTelemetry{}
	engine, _ = mustHistoricalEngineWithTelemetry(t, TimeContext{Axis: TemporalCurrent}, now, quiet)
	request.Question = "compare them"
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	for _, record := range quiet.statedWindowAxes {
		if record.Origin == StatedWindowOriginQuestionDates {
			t.Fatalf("a question with no dates wrote a question_dates line: %+v", record)
		}
	}
}
