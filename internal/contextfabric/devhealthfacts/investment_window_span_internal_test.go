package devhealthfacts

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A project's span keeps the earliest start recorded for its key, whatever the
// order the roll-up and native paths record them in; a zero start records
// nothing; another project's key is untouched.
func TestProjectSpanKeepsTheEarliestRecordedStartPerProject(t *testing.T) {
	t.Parallel()
	early := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	for name, order := range map[string][]time.Time{"late then early": {late, early}, "early then late": {early, late}, "with a zero start": {late, {}, early}} {
		ctx, span := withInvestmentSpan(context.Background())
		for _, from := range order {
			recordInvestmentProjectSpan(ctx, "linear:a", from)
		}
		recordInvestmentProjectSpan(ctx, "linear:b", late)
		if got := span.byProject["linear:a"]; !got.Equal(early) {
			t.Errorf("%s: span = %v, want %v", name, got, early)
		}
		if got := span.byProject["linear:b"]; !got.Equal(late) {
			t.Errorf("%s: another project's span = %v, want %v", name, got, late)
		}
	}
}

// The stored-history reason names the span as stored history, not as the mix:
// the span counts every persisted unit, an evidence-gated one included, and a
// gated unit is never in the mix (CHAOS-11083).
func TestStoredHistoryReasonNamesStoredHistoryNotTheMix(t *testing.T) {
	t.Parallel()
	earliest := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	for name, current := range map[string]bool{"range window": false, "current-axis window": true} {
		ctx, span := withInvestmentSpan(context.Background())
		recordInvestmentSpan(ctx, "repository:r", earliest)
		got := span.reasonFor("repository:r", "", false, factTimeBound{active: true, hasStart: true, start: start, end: end, currentAxis: current})
		if !strings.HasPrefix(got, investmentSpanReasonToken+": repository:r: ") || !strings.Contains(got, "the stored history covers 2026-01-10T00:00:00Z to ") {
			t.Errorf("%s: reason = %q, want the token and \"the stored history covers 2026-01-10T00:00:00Z to ...\"", name, got)
		}
		if strings.Contains(got, "the mix covers") {
			t.Errorf("%s: reason = %q says the mix covers the stored span", name, got)
		}
	}
}

// The reworded reason is no longer than the reason it replaces, for the same
// inputs, so a clipped request note and a max_bytes response keep what they
// kept before: the format strings below are the pre-change text, verbatim.
func TestStoredHistoryReasonIsNoLongerThanTheTextItReplaced(t *testing.T) {
	t.Parallel()
	earliest := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	const subject = "repository:0424f61b-145f-96ff-45ae-b892fd19b0cb"
	e, s, en := earliest.Format(time.RFC3339), start.Format(time.RFC3339), end.Format(time.RFC3339)
	before := map[bool]string{
		false: fmt.Sprintf("%s: %s: the window starts %s but its earliest persisted work unit starts %s; the mix covers %s to %s only and the days before are not stored (not zero)",
			investmentSpanReasonToken, subject, s, e, e, en),
		true: fmt.Sprintf("%s: %s: the window starts before its earliest persisted work unit, which starts %s; the mix covers %s to the window end only and the days before are not stored (not zero)",
			investmentSpanReasonToken, subject, e, e),
	}
	for current, old := range before {
		ctx, span := withInvestmentSpan(context.Background())
		recordInvestmentSpan(ctx, subject, earliest)
		got := span.reasonFor(subject, "", false, factTimeBound{active: true, hasStart: true, start: start, end: end, currentAxis: current})
		if got == "" || len(got) > len(old) {
			t.Errorf("current axis %v: reason is %d bytes, the text it replaced %d: %q", current, len(got), len(old), got)
		}
	}
}
