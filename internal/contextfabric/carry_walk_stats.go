package contextfabric

import (
	"context"
	"time"
)

// CarryWalkStats is what one carry walk cost: how many prior results it
// visited and how long it ran. Plain numbers, content-safe by construction.
//
// OtherEpochVisited counts the visited results saved under a graph epoch other
// than this turn's binding, or with no epoch at all. The kind and window walks
// read such a result like any other (neither value names a graph id), so this
// is the only place a carry across a lifecycle rebuild shows on the trace.
type CarryWalkStats struct {
	Visited           int
	OtherEpochVisited int
	Duration          time.Duration
}

// WalkMillis is Duration in whole milliseconds.
func (s CarryWalkStats) WalkMillis() int64 { return s.Duration.Milliseconds() }

type carryWalkRecorderKey struct{}

// carryWalkRecorder sums the stats of every walk run under one context.
type carryWalkRecorder struct {
	now   func() time.Time
	stats CarryWalkStats
}

func withCarryWalkRecorder(ctx context.Context, now func() time.Time) (context.Context, *carryWalkRecorder) {
	rec := &carryWalkRecorder{now: now}
	return context.WithValue(ctx, carryWalkRecorderKey{}, rec), rec
}

func carryWalkRecorderFrom(ctx context.Context) *carryWalkRecorder {
	rec, _ := ctx.Value(carryWalkRecorderKey{}).(*carryWalkRecorder)
	return rec
}

// begin returns the function a walk defers with its final counts.
func (r *carryWalkRecorder) begin() func(visited, otherEpochVisited int) {
	if r == nil {
		return func(int, int) {}
	}
	start := r.now()
	return func(visited, otherEpochVisited int) {
		r.stats.Visited += visited
		r.stats.OtherEpochVisited += otherEpochVisited
		r.stats.Duration += r.now().Sub(start)
	}
}

// carrierFromOtherEpoch reports whether a loaded carrier was saved under a
// graph epoch other than binding's, or under none.
func carrierFromOtherEpoch(stored StoredInvestigationResult, binding ResolvedGraphBinding) bool {
	return stored.GraphEpoch == nil || *stored.GraphEpoch != binding.Epoch
}

func (r *carryWalkRecorder) snapshot() CarryWalkStats {
	if r == nil {
		return CarryWalkStats{}
	}
	return r.stats
}
