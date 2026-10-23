package chtlsrig

import (
	"sort"
	"time"
)

// Percentile returns the p-th percentile (0 < p <= 1) by the nearest-rank
// rule; it panics on an empty sample so a measurement that did not happen
// cannot read as zero.
func Percentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		panic("chtlsrig: percentile of an empty sample")
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(p*float64(len(sorted)) + 0.999999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
