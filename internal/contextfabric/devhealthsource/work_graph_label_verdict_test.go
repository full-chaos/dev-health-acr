package devhealthsource_test

import "sort"

// The pure half of TestWorkGraphLabelStatementCostIgnoresOtherWorkItemsAndOtherEdges:
// what it decides from the per-run peak memory of each stage. It needs no
// ClickHouse, so work_graph_label_verdict_unit_test.go can prove it.

const (
	workGraphSlopeFactor = 1.15
	workGraphMemoryFloor = float64(2 << 20)
)

// lowerQuartile is the nearest-rank 25th percentile: the ceil(n/4)-th smallest
// run. With the three runs the test measured before this is the smallest run;
// with eight it is the second smallest.
func lowerQuartile(samples []uint64) uint64 {
	if len(samples) == 0 {
		panic("lowerQuartile of no samples")
	}
	sorted := append([]uint64(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[(len(sorted)+3)/4-1]
}

// slopeOver reports whether the stage's plateau exceeds the 1.15x slope of the
// base's plateau plus the 2 MiB floor, with both levels and the bound.
func slopeOver(stage, base []uint64) (over bool, stageLevel uint64, bound float64) {
	stageLevel = lowerQuartile(stage)
	bound = workGraphSlopeFactor*float64(lowerQuartile(base)) + workGraphMemoryFloor
	return float64(stageLevel) > bound, stageLevel, bound
}

// highestRunOver reports the highest run and whether it reaches the ceiling.
func highestRunOver(runs []uint64, ceiling uint64) (highest uint64, over bool) {
	for _, run := range runs {
		if run > highest {
			highest = run
		}
	}
	return highest, highest >= ceiling
}

const (
	statementMedianCeiling  = uint64(12 << 20)
	statementHighestCeiling = uint64(16 << 20)
)

// medianRun is the upper median: the (n/2+1)-th smallest run.
func medianRun(runs []uint64) uint64 {
	if len(runs) == 0 {
		panic("medianRun of no runs")
	}
	sorted := append([]uint64(nil), runs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// statementOver is the verdict on the work_graph.v1 statement's per-run peaks.
// The median must stay under 12 MiB, which catches a moved plateau; the
// highest run must stay under 16 MiB, which tolerates the rare outlier (the
// highest of 7 main runs was 12,290,871 bytes, ci run 45118451445; 16 MiB is
// that figure x1.30 rounded up).
func statementOver(runs []uint64) (over bool, median, highest uint64) {
	median = medianRun(runs)
	highest, highestOver := highestRunOver(runs, statementHighestCeiling)
	return median >= statementMedianCeiling || highestOver, median, highest
}
