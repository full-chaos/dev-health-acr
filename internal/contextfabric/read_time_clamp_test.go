package contextfabric

import (
	"testing"
	"time"
)

func TestAClampRecordsTheInstantItWrote(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 500, time.UTC)
	at := func(value string) *time.Time {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			t.Fatal(err)
		}
		return &parsed
	}
	cases := map[string]struct {
		timeContext TimeContext
		clamped     bool
		written     func(TimeContext) []*time.Time
	}{
		"a future as-of": {TimeContext{Axis: TemporalValidTime, AsOf: at("2026-08-12T18:00:00Z")}, true, func(b TimeContext) []*time.Time { return []*time.Time{b.AsOf} }},
		"a future range end": {TimeContext{Axis: TemporalRange, Start: at("2026-08-01T00:00:00Z"), End: at("2026-08-31T00:00:00Z")}, true,
			func(b TimeContext) []*time.Time { return []*time.Time{b.End} }},
		"a range wholly in the future": {TimeContext{Axis: TemporalRange, Start: at("2026-08-20T00:00:00Z"), End: at("2026-08-31T00:00:00Z")}, true,
			func(b TimeContext) []*time.Time { return []*time.Time{b.Start, b.End} }},
		"a too-wide range with a future end": {TimeContext{Axis: TemporalRange, Start: at("2024-01-01T00:00:00Z"), End: at("2026-08-31T00:00:00Z")}, true,
			func(b TimeContext) []*time.Time { return []*time.Time{b.End} }},
		"a past as-of":     {TimeContext{Axis: TemporalValidTime, AsOf: at("2026-08-10T00:00:00Z")}, false, nil},
		"a past range":     {TimeContext{Axis: TemporalRange, Start: at("2026-08-01T00:00:00Z"), End: at("2026-08-10T00:00:00Z")}, false, nil},
		"the current axis": {TimeContext{Axis: TemporalCurrent}, false, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			decision := resolveInterpretedTimeContext(tc.timeContext, now)
			if decision.ClampApplied != tc.clamped {
				t.Fatalf("ClampApplied = %v, want %v", decision.ClampApplied, tc.clamped)
			}
			if !tc.clamped {
				if !decision.ClampedTo.IsZero() {
					t.Fatalf("ClampedTo = %v on a turn with no clamp", decision.ClampedTo)
				}
				return
			}
			if !decision.ClampedTo.Equal(now) {
				t.Fatalf("ClampedTo = %v, want the clamp instant %v", decision.ClampedTo, now)
			}
			for _, written := range tc.written(decision.Bound) {
				if written == nil || !written.Equal(decision.ClampedTo) {
					t.Fatalf("bound %+v: a clamped instant is not ClampedTo", decision.Bound)
				}
			}
		})
	}
}
