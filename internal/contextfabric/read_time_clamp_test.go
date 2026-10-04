package contextfabric

import (
	"testing"
	"time"
)

func TestAClampRecordsTheInstantAndTheFieldsItWrote(t *testing.T) {
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
		want        ReadTimeClamp
	}{
		"a future as-of":                     {TimeContext{Axis: TemporalValidTime, AsOf: at("2026-08-12T18:00:00Z")}, ReadTimeClamp{At: now, AsOf: true}},
		"a future range end":                 {TimeContext{Axis: TemporalRange, Start: at("2026-08-01T00:00:00Z"), End: at("2026-08-31T00:00:00Z")}, ReadTimeClamp{At: now, End: true}},
		"a range wholly in the future":       {TimeContext{Axis: TemporalRange, Start: at("2026-08-20T00:00:00Z"), End: at("2026-08-31T00:00:00Z")}, ReadTimeClamp{At: now, Start: true, End: true}},
		"a too-wide range with a future end": {TimeContext{Axis: TemporalRange, Start: at("2024-01-01T00:00:00Z"), End: at("2026-08-31T00:00:00Z")}, ReadTimeClamp{At: now, End: true}},
		"a past as-of":                       {TimeContext{Axis: TemporalValidTime, AsOf: at("2026-08-10T00:00:00Z")}, ReadTimeClamp{}},
		"a past range":                       {TimeContext{Axis: TemporalRange, Start: at("2026-08-01T00:00:00Z"), End: at("2026-08-10T00:00:00Z")}, ReadTimeClamp{}},
		"the current axis":                   {TimeContext{Axis: TemporalCurrent}, ReadTimeClamp{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			decision := resolveInterpretedTimeContext(tc.timeContext, now)
			if decision.Clamp != tc.want {
				t.Fatalf("Clamp = %+v, want %+v", decision.Clamp, tc.want)
			}
			if decision.ClampApplied != (tc.want != ReadTimeClamp{}) {
				t.Fatalf("ClampApplied = %v with Clamp %+v", decision.ClampApplied, decision.Clamp)
			}
			bound := decision.Bound
			for _, written := range []struct {
				wrote bool
				value *time.Time
			}{{tc.want.AsOf, bound.AsOf}, {tc.want.Start, bound.Start}, {tc.want.End, bound.End}} {
				if written.wrote && (written.value == nil || !written.value.Equal(now)) {
					t.Fatalf("bound %+v: a field the clamp wrote does not hold the clamp instant", bound)
				}
			}
		})
	}
}
