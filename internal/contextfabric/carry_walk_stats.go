package contextfabric

import (
	"context"
	"time"
)

// CarryWalkStats is what one carry walk cost: how many prior results it
// visited and how long it ran. Plain numbers, content-safe by construction.
type CarryWalkStats struct {
	Visited  int
	Duration time.Duration
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

// begin returns the function a walk defers with its final visited count.
func (r *carryWalkRecorder) begin() func(visited int) {
	if r == nil {
		return func(int) {}
	}
	return func(int) {}
}

func (r *carryWalkRecorder) snapshot() CarryWalkStats {
	if r == nil {
		return CarryWalkStats{}
	}
	return r.stats
}
