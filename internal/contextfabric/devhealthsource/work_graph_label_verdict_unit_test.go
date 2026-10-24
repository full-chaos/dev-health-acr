package devhealthsource_test

import (
	"math"
	"reflect"
	"testing"
)

func repeat(value uint64, n int) []uint64 {
	out := make([]uint64, n)
	for i := range out {
		out[i] = value
	}
	return out
}

func scaled(samples []uint64, factor float64) []uint64 {
	out := make([]uint64, len(samples))
	for i, sample := range samples {
		out[i] = uint64(float64(sample) * factor)
	}
	return out
}

func concat(parts ...[]uint64) []uint64 {
	var out []uint64
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

func TestLowerQuartileIsTheNearestRankOfTheRuns(t *testing.T) {
	for n, want := range map[int]uint64{1: 1, 2: 1, 3: 1, 4: 1, 5: 2, 6: 2, 7: 2, 8: 2, 9: 3} {
		runs := make([]uint64, n)
		for i := range runs {
			runs[i] = uint64(n - i) // descending: the function must sort
		}
		if got := lowerQuartile(runs); got != want {
			t.Errorf("lowerQuartile of %d runs = %d, want the %d-th smallest", n, got, want)
		}
	}
	runs := []uint64{5, 1, 4}
	_ = lowerQuartile(runs)
	if !reflect.DeepEqual(runs, []uint64{5, 1, 4}) {
		t.Fatalf("lowerQuartile reordered its input: %v", runs)
	}
}

// The two incidents as they were recorded, with the sample counts they had.
// The lower quartile of three runs is the smallest run, i.e. the rule the test
// had before: these two STILL fail, which is the point of this test. The
// remedy is the number of runs (eight, interleaved), not the statistic.
func TestIncident40065829520WithItsThreeReadsStillFailsTheSlope(t *testing.T) {
	// +commit edges read 11,625,663 bytes on all three reads; the base's
	// lowest was 8,059,298 and its highest 11,633,038 (its middle read was not
	// recorded; any value between them gives the same verdict).
	stage := repeat(11625663, 3)
	base := []uint64{8059298, 9800000, 11633038}
	over, level, bound := slopeOver(stage, base)
	if !over || level != 11625663 || uint64(math.Round(bound)) != 11365345 {
		t.Fatalf("over=%v level=%d bound=%.0f, want over, 11625663, 11365345 (the figures of the failure)", over, level, bound)
	}
}

func TestIncidentOf0808ZWithItsThreeReadsStillFailsTheSlope(t *testing.T) {
	stage := []uint64{10715216, 10850000, 10963148}
	base := repeat(6975353, 3)
	over, _, bound := slopeOver(stage, base)
	if !over || uint64(math.Round(bound)) != 10118808 {
		t.Fatalf("over=%v bound=%.0f, want over the 10,118,808 bytes of that failure", over, bound)
	}
}

// Eight runs, the stage on the high mode in every one and the base on both:
// the rule FLAGS the stage. It never reached the low plateau in eight runs
// while the base reached it four times; if the mode were a coin flip per run
// that happens in 1 of 256 stages, so the rule reads it as a higher plateau.
func TestEightRunsOnTheHighModeAgainstAMixedBaseFlagTheStage(t *testing.T) {
	stage := repeat(11625663, 8)
	base := concat(repeat(7031013, 4), repeat(11633038, 4))
	if over, _, _ := slopeOver(stage, base); !over {
		t.Fatal("a stage that never left the high plateau in eight runs against a base that did must be flagged")
	}
}

// The flake the interleaving is meant to absorb: both stages mixed over the
// same eight rounds. A stage that spent most of them on the high mode but
// reached the low one is on the same plateau as the base.
func TestEightRunsWithBothModesInTheStageAndTheBaseAreNotAFalseAlarm(t *testing.T) {
	stage := concat(repeat(7031013, 3), repeat(11625663, 5))
	base := concat(repeat(7031013, 2), []uint64{8059298}, repeat(11633038, 5))
	if over, level, bound := slopeOver(stage, base); over {
		t.Fatalf("level %d over bound %.0f for a stage on the same two modes as the base", level, bound)
	}
}

func TestPlantedSlopeRegressionsOnEveryRun(t *testing.T) {
	base := concat(repeat(7031013, 4), repeat(11633038, 4))
	for _, tc := range []struct {
		name   string
		factor float64
		want   bool
	}{
		{"+30% on every run stays inside the 2 MiB floor of a 7 MB statement", 1.30, false},
		{"+60% on every run", 1.60, true},
		{"+100% on every run", 2.00, true},
	} {
		if over, level, bound := slopeOver(scaled(base, tc.factor), base); over != tc.want {
			t.Errorf("%s: over=%v level=%d bound=%.0f, want over=%v", tc.name, over, level, bound, tc.want)
		}
	}
}

// NOT pinned by the slope: a regression that moves only the high mode. The
// lower quartile reads the low plateau, so this PASSES the slope; the
// absolute ceiling on the highest run is the only cover (next test).
func TestAHighModeOnlyRegressionPassesTheSlopeAndIsCoveredOnlyByTheCeiling(t *testing.T) {
	base := concat(repeat(7031013, 4), repeat(11633038, 4))
	stage := concat(repeat(7031013, 4), scaled(repeat(11633038, 4), 1.60))
	if over, _, _ := slopeOver(stage, base); over {
		t.Fatal("the slope now sees a high-mode-only regression: update the NOT-pinned note in the RISK-NOTES")
	}
	if _, over := highestRunOver(stage, 12<<20); !over {
		t.Fatal("the absolute ceiling must be the cover for a high-mode-only regression")
	}
}

func TestOneHighRunAmongEightIsNotAStageRegression(t *testing.T) {
	base := repeat(7031013, 8)
	stage := concat(repeat(7031013, 7), []uint64{20 << 20})
	if over, _, _ := slopeOver(stage, base); over {
		t.Fatal("a single high outlier lifted the plateau")
	}
}

func TestTheSlopeBoundIsExactToTheByte(t *testing.T) {
	base := repeat(1000000, 8) // bound = 1.15 x 1,000,000 + 2 MiB = 3,247,152
	if over, _, bound := slopeOver(repeat(3247152, 8), base); over || uint64(bound) != 3247152 {
		t.Fatalf("a stage exactly at the bound is over: bound %.0f", bound)
	}
	if over, _, _ := slopeOver(repeat(3247153, 8), base); !over {
		t.Fatal("one byte over the bound must be flagged")
	}
}

func TestTheCeilingIsReachedByTheHighestRunAtTheCeiling(t *testing.T) {
	if highest, over := highestRunOver([]uint64{5, 11 << 20, 7}, 12<<20); over || highest != 11<<20 {
		t.Fatalf("highest %d over=%v under a 12 MiB ceiling", highest, over)
	}
	if _, over := highestRunOver([]uint64{5, 12 << 20}, 12<<20); !over {
		t.Fatal("a run AT the ceiling must be over it")
	}
	if _, over := highestRunOver([]uint64{1, 2, 3, 13 << 20, 2, 1}, 12<<20); !over {
		t.Fatal("one run over the ceiling among low ones must be flagged: the ceiling reads the highest run")
	}
}
