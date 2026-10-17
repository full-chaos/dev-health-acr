package devhealthsource_test

import (
	"fmt"
	"testing"
)

// The slope rule compares two sizes of one statement. Its margin is derived
// from what the two sizes measured, not fixed: the typical growth that allocator
// noise alone makes is the spread of the warm reads at each size, so the margin
// is at least that spread. It is never less than slopeRatioLimit of the small
// size or slopeFloorMiB, and the spread counts for at most slopeNoiseCap of the
// small size, so a statement that really grows with its input fails however
// noisy the reads are.
const (
	slopeRatioLimit = 0.15
	slopeFloorMiB   = 2.0
	slopeNoiseCap   = 0.5
)

type slopeMargin struct {
	limit, margin, spread float64
}

func slopeMarginOf(small, large fragmentPeak) slopeMargin {
	spread := (small.maxMiB - small.mib) + (large.maxMiB - large.mib)
	counted := spread
	if cap := slopeNoiseCap * small.medianMiB; counted > cap {
		counted = cap
	}
	margin := max(slopeRatioLimit*small.medianMiB, slopeFloorMiB, counted)
	return slopeMargin{limit: small.medianMiB + margin, margin: margin, spread: spread}
}

func slopeBreach(small, large fragmentPeak) (bool, string) {
	m := slopeMarginOf(small, large)
	line := fmt.Sprintf("median %.2f MiB at the small size (lowest %.2f, highest %.2f), %.2f MiB at the large (lowest %.2f, highest %.2f); read spread %.2f MiB, margin %.2f MiB, limit %.2f MiB",
		small.medianMiB, small.mib, small.maxMiB, large.medianMiB, large.mib, large.maxMiB, m.spread, m.margin, m.limit)
	return large.medianMiB > m.limit, line
}

func slopePeak(lowest, median, highest float64) fragmentPeak {
	return fragmentPeak{mib: lowest, medianMiB: median, maxMiB: highest}
}

func TestSlopeMarginIsDerivedFromTheMeasuredSpread(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		small, large fragmentPeak
		breach       bool
	}{
		// CHAOS-9701 sample: 12.45 -> 14.67 MiB, reads moving in 1.5 MiB steps.
		{"allocator noise alone, as measured in CI", slopePeak(11.0, 12.45, 13.5), slopePeak(13.2, 14.67, 15.9), false},
		{"a quiet pair inside the floor", slopePeak(14.8, 14.81, 14.82), slopePeak(14.9, 14.96, 15.0), false},
		{"a quiet pair above the floor and the ratio", slopePeak(14.8, 14.81, 14.82), slopePeak(17.5, 17.6, 17.7), true},
		{"growth with the input", slopePeak(13.9, 14.0, 14.1), slopePeak(27.0, 28.0, 29.0), true},
		{"growth hidden under a very wide spread is still caught by the cap", slopePeak(5, 14.0, 30), slopePeak(5, 22.0, 30), true},
	} {
		if got, line := slopeBreach(tc.small, tc.large); got != tc.breach {
			t.Errorf("%s: breach = %v, want %v (%s)", tc.name, got, tc.breach, line)
		}
	}
}

func TestSlopeMarginNeverFallsBelowTheFixedFloors(t *testing.T) {
	t.Parallel()
	small := slopePeak(10, 10, 10)
	if m := slopeMarginOf(small, slopePeak(10, 10, 10)); m.margin != slopeFloorMiB {
		t.Errorf("margin with no spread at 10 MiB = %.2f, want the %.2f MiB floor", m.margin, slopeFloorMiB)
	}
	big := slopePeak(40, 40, 40)
	if m := slopeMarginOf(big, slopePeak(40, 40, 40)); m.margin != slopeRatioLimit*40 {
		t.Errorf("margin with no spread at 40 MiB = %.2f, want %.2f (the ratio)", m.margin, slopeRatioLimit*40)
	}
}
