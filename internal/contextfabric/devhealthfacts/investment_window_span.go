package devhealthfacts

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// investmentSpan collects the earliest persisted work unit start the mix
// statement reports (min(from_ts) over the organization's latest units, a
// column of the same single statement, so no second read of the table).
type investmentSpan struct {
	mu       sync.Mutex
	earliest time.Time
	found    bool
}

type investmentSpanKey struct{}

func withInvestmentSpan(ctx context.Context) (context.Context, *investmentSpan) {
	span := &investmentSpan{}
	return context.WithValue(ctx, investmentSpanKey{}, span), span
}

func recordInvestmentSpan(ctx context.Context, from time.Time) {
	span, ok := ctx.Value(investmentSpanKey{}).(*investmentSpan)
	if !ok || from.IsZero() {
		return
	}
	span.mu.Lock()
	defer span.mu.Unlock()
	if !span.found || from.Before(span.earliest) {
		span.earliest, span.found = from, true
	}
}

// recordInvestmentSpanTime records a min(from_ts) column. A statement that
// selected no row reports the epoch default, which is not a stored start, so
// it is skipped.
func recordInvestmentSpanTime(ctx context.Context, from time.Time, hadRows bool) error {
	if !hadRows || from.Unix() <= 0 {
		return nil
	}
	recordInvestmentSpan(ctx, from.UTC())
	return nil
}

// reasonFor is the disclosure for a window that starts before the earliest
// persisted work unit: the facts are served over the available span and the
// days before it are not stored, which is not the same as zero. Empty when
// the window is inside the stored history or names no start.
func (s *investmentSpan) reasonFor(b factTimeBound) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.found || !b.active || !b.hasStart || !b.start.Before(s.earliest) {
		return ""
	}
	return fmt.Sprintf("investment_window_beyond_stored_history: the window starts %s but the earliest persisted work unit starts %s; the mix covers %s to %s only and the days before are not stored (not zero)",
		b.start.UTC().Format(time.RFC3339), s.earliest.UTC().Format(time.RFC3339), s.earliest.UTC().Format(time.RFC3339), b.end.UTC().Format(time.RFC3339))
}
