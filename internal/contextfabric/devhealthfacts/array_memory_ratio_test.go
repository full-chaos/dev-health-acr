package devhealthfacts_test

import "testing"

func TestArraysOverShareIgnoresOneLowBaseOutlier(t *testing.T) {
	// base reads 5.82, 7.40, 7.45 MB (one low outlier), with reads 8.05, 8.2, 8.3 MB
	base := []uint64{5_821_817, 7_400_000, 7_445_253}
	with := []uint64{8_053_356, 8_200_000, 8_300_000}
	r := reading{with: 8_053_356, base: 5_821_817, withMid: medianOf(with), baseMid: medianOf(base), withSpread: 246_644, baseSpread: 1_623_436}
	if arraysOverShare(r) {
		t.Fatalf("a low outlier in the base reads failed the ratio row: %+v", r)
	}
}

func TestArraysOverShareStillCatchesRealGrowth(t *testing.T) {
	base := []uint64{20_000_000, 20_200_000, 20_300_000}
	with := []uint64{26_000_000, 26_100_000, 26_300_000}
	r := reading{withMid: medianOf(with), baseMid: medianOf(base), withSpread: 300_000, baseSpread: 300_000}
	if !arraysOverShare(r) {
		t.Fatalf("a 30%% growth in every read was not flagged: %+v", r)
	}
}
