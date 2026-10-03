package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"testing"
	"time"
)

func TestCHAOS6746_BinderNamesTheCalendarPeriod(t *testing.T) {
	t.Parallel()
	cases := []struct {
		question string
		want     CalendarPeriod
	}{
		{"Which repository carried the most operational/support work last month?", CalendarPeriodMonth},
		{"Which repository carried the most operational/support work last month and why?", CalendarPeriodMonth},
		{"which repo carried the most work last month, and why?", CalendarPeriodMonth},
		{"Last month, which repository carried the most work?", CalendarPeriodMonth},
		{"What did the team ship for last month?", CalendarPeriodMonth},
		{"What did the team ship over last quarter?", CalendarPeriodQuarter},
		{"Which repository carried the most work last year?", CalendarPeriodYear},
		// Trailing forms are not calendar periods.
		{"Which repository carried the most work in the last month?", CalendarPeriodNone},
		{"Which repository carried the most work over the past month?", CalendarPeriodNone},
		{"Which repository carried the most work over the last 30 days?", CalendarPeriodNone},
		{"What shipped since last month?", CalendarPeriodNone},
		// Point in time.
		{"What was the team's state as of the end of last month?", CalendarPeriodNone},
		{"What was the team's state as of last month?", CalendarPeriodNone},
		// Two spans refuse.
		{"Compare last month with last quarter for the team.", CalendarPeriodNone},
		// A name, or a mid-sentence position with no conjunction.
		{"How is the Last Year and Beyond project doing?", CalendarPeriodNone},
		{"How did last month treat the operational work of each repository?", CalendarPeriodNone},
		{"Which repository carried the most work this month?", CalendarPeriodNone},
		{"Which repository carried the most work?", CalendarPeriodNone},
	}
	for _, tc := range cases {
		if got := ProposeWindowFromSpans(tc.question).Calendar; got != tc.want {
			t.Errorf("%q: Calendar = %q, want %q", tc.question, got, tc.want)
		}
	}
}

// The calendar reading never changes the binder's own routing: off the MCP
// surface a bare calendar phrase is still the same non-trailing proposal (or
// unbound span) it was.
func TestCHAOS6746_CalendarReadingLeavesTheProposalRoutingUntouched(t *testing.T) {
	t.Parallel()
	routed := ProposeWindowFromSpans("Which repository carried the most work last month?")
	if routed.Reason != WindowBindRoutedInferred || routed.Trailing || routed.RelativeID != RelativeWindowTrailing30D {
		t.Fatalf("outcome = %#v, want the unchanged non-trailing inferred proposal", routed)
	}
	unbound := ProposeWindowFromSpans("Which repository carried the most work last month and why?")
	if unbound.Reason != WindowBindSpanUnbound || unbound.SpansBound != 1 {
		t.Fatalf("outcome = %#v, want the unchanged unbound span", unbound)
	}
}

func TestCHAOS6746_CalendarWindowBounds(t *testing.T) {
	t.Parallel()
	utc := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	plus2 := time.FixedZone("plus2", 2*3600)
	minus7 := time.FixedZone("minus7", -7*3600)
	cases := []struct {
		name       string
		period     CalendarPeriod
		now        time.Time
		start, end time.Time
	}{
		{"month mid", CalendarPeriodMonth, time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC), utc(2026, 8, 1), utc(2026, 9, 1)},
		{"month january rolls the year back", CalendarPeriodMonth, time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC), utc(2026, 12, 1), utc(2027, 1, 1)},
		{"month first instant", CalendarPeriodMonth, utc(2026, 9, 1), utc(2026, 8, 1), utc(2026, 9, 1)},
		{"month last instant", CalendarPeriodMonth, time.Date(2026, 8, 31, 23, 59, 59, 0, time.UTC), utc(2026, 7, 1), utc(2026, 8, 1)},
		{"month is UTC not local (ahead of UTC)", CalendarPeriodMonth, time.Date(2026, 9, 1, 0, 30, 0, 0, plus2), utc(2026, 7, 1), utc(2026, 8, 1)},
		{"month is UTC not local (behind UTC)", CalendarPeriodMonth, time.Date(2026, 8, 31, 20, 0, 0, 0, minus7), utc(2026, 8, 1), utc(2026, 9, 1)},
		{"month leap february", CalendarPeriodMonth, utc(2028, 3, 10), utc(2028, 2, 1), utc(2028, 3, 1)},
		{"quarter q3", CalendarPeriodQuarter, time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC), utc(2026, 4, 1), utc(2026, 7, 1)},
		{"quarter q1 rolls the year back", CalendarPeriodQuarter, utc(2027, 2, 3), utc(2026, 10, 1), utc(2027, 1, 1)},
		{"quarter first instant of q4", CalendarPeriodQuarter, utc(2026, 10, 1), utc(2026, 7, 1), utc(2026, 10, 1)},
		{"quarter last instant of q2", CalendarPeriodQuarter, time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC), utc(2026, 1, 1), utc(2026, 4, 1)},
		{"year", CalendarPeriodYear, time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC), utc(2025, 1, 1), utc(2026, 1, 1)},
		{"year first instant", CalendarPeriodYear, utc(2026, 1, 1), utc(2025, 1, 1), utc(2026, 1, 1)},
	}
	for _, tc := range cases {
		start, end, ok := calendarWindowBounds(tc.period, tc.now)
		if !ok || !start.Equal(tc.start) || !end.Equal(tc.end) || start.Location() != time.UTC || end.Location() != time.UTC {
			t.Errorf("%s: calendarWindowBounds(%q, %v) = %v..%v ok=%v, want %v..%v (UTC)", tc.name, tc.period, tc.now, start, end, ok, tc.start, tc.end)
		}
	}
	if _, _, ok := calendarWindowBounds(CalendarPeriodNone, time.Now()); ok {
		t.Error("calendarWindowBounds(none) ok, want false")
	}
}

// withdrawCalendarCommit: the interpretation can only WITHDRAW the binder's
// commitment, never move its bounds.
func TestCHAOS6746_WithdrawCalendarCommit(t *testing.T) {
	t.Parallel()
	committed := requestWindowCanonicalization{CalendarCommitted: true, KeyComponent: "k", KeyEncoding: windowKeyFrozen}
	start, end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	committed.Effective = &contractsv1.ContextFabricEffectiveEvidenceWindow{Start: &start, End: &end, Provenance: WindowQuestionStated}
	current := bootstrapInterpretation()
	ranged := sampledInterpretations()[0]
	asOf := driftedInterpretation(TemporalValidTime)
	snapshot := bootstrapInterpretation()
	snapshot.WindowClass = WindowClassStateSnapshot
	snapshot.WindowConfidence = WindowConfidenceHigh
	for name, tc := range map[string]struct {
		in   InterpretedQuestion
		want bool
	}{"current": {current, false}, "range": {ranged, false}, "as of": {asOf, true}, "state snapshot": {snapshot, true}} {
		got, withdrawn := withdrawCalendarCommit(committed, tc.in)
		if withdrawn != tc.want || (withdrawn && (got.Effective != nil || got.KeyComponent != "" || got.CalendarCommitted)) || (!withdrawn && got.Effective == nil) {
			t.Errorf("%s: withdrawn=%v effective=%v, want withdrawn=%v", name, withdrawn, got.Effective, tc.want)
		}
	}
	if _, withdrawn := withdrawCalendarCommit(requestWindowCanonicalization{}, asOf); withdrawn {
		t.Error("a request with no calendar commitment reported a withdrawal")
	}
}

// The committed calendar window keys answer reuse on its FROZEN bounds: an
// answer stored for one month is never served for the next one.
func TestCHAOS6746_CommittedCalendarWindowKeysReuseOnItsFrozenBounds(t *testing.T) {
	t.Parallel()
	request := validInvestigationRequest()
	request.Question = "Which repository carried the most operational/support work last month?"
	request.Consumer.Surface = mcpSurface
	keyAt := func(now time.Time) requestWindowCanonicalization {
		engine := &Engine{now: func() time.Time { return now }}
		return engine.commitBinderCalendarWindow(request, ProposeWindowFromSpans(request.Question))
	}
	august := keyAt(time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC))
	september := keyAt(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))
	wantAugust := "abs:" + formatUnixNano(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)) + ":" + formatUnixNano(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if august.KeyComponent != wantAugust || august.KeyEncoding != windowKeyFrozen || !august.CalendarCommitted {
		t.Fatalf("key component = %q encoding=%v committed=%v, want frozen %q", august.KeyComponent, august.KeyEncoding, august.CalendarCommitted, wantAugust)
	}
	if september.KeyComponent == august.KeyComponent || september.KeyComponent == "" {
		t.Fatalf("september key %q must differ from august %q", september.KeyComponent, august.KeyComponent)
	}
	// Off MCP, and with no calendar phrase, nothing is committed.
	request.Consumer.Surface = "workbench"
	if got := (&Engine{now: time.Now}).commitBinderCalendarWindow(request, ProposeWindowFromSpans(request.Question)); got.Effective != nil || got.CalendarCommitted {
		t.Fatalf("off MCP: %#v, want no commitment", got)
	}
	request.Consumer.Surface = mcpSurface
	if got := (&Engine{now: time.Now}).commitBinderCalendarWindow(request, ProposeWindowFromSpans("Which repository carried the most work?")); got.Effective != nil || got.CalendarCommitted {
		t.Fatalf("no phrase: %#v, want no commitment", got)
	}
}
