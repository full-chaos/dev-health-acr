package devhealthfacts

import (
	"context"
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
