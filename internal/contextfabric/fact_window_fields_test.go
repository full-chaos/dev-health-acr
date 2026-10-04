package contextfabric

import (
	"testing"
	"time"
)

func TestWindowBoundsComeFromTheClockOnlyForARederivableRelativeWindow(t *testing.T) {
	start, end := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC), time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	relative := &EffectiveEvidenceWindow{Start: &start, End: &end, RelativeID: RelativeWindowTrailing30D}
	absolute := &EffectiveEvidenceWindow{Start: &start, End: &end}
	allTime := &EffectiveEvidenceWindow{RelativeID: RelativeWindowAllTime}
	cases := map[string]struct {
		window   *EffectiveEvidenceWindow
		encoding windowKeyEncoding
		carried  bool
		want     bool
	}{
		"no window":                      {nil, windowKeyRederivable, false, false},
		"a re-derivable relative window": {relative, windowKeyRederivable, false, true},
		"a frozen relative window":       {relative, windowKeyFrozen, false, false},
		"a carried relative window":      {relative, windowKeyRederivable, true, false},
		"a stated absolute window":       {absolute, windowKeyRederivable, false, false},
		"a frozen absolute window":       {absolute, windowKeyFrozen, false, false},
		"all time":                       {allTime, windowKeyRederivable, false, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := windowBoundsFromClock(tc.window, tc.encoding, tc.carried); got != tc.want {
				t.Fatalf("windowBoundsFromClock = %v, want %v", got, tc.want)
			}
		})
	}
}
