package devhealthfacts

import (
	"context"
	"fmt"
	"strings"
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

// recordInvestmentSpanText parses the toString of a DateTime64(6) min(from_ts)
// column and records it. A statement that selected no row reports the epoch
// default, which is not a stored start, so it is skipped.
func recordInvestmentSpanText(ctx context.Context, text string, hadRows bool) error {
	if !hadRows {
		return nil
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05.999999", strings.TrimSpace(text), time.UTC)
	if err != nil {
		return fmt.Errorf("parse earliest work unit start %q: %w", text, err)
	}
	recordInvestmentSpan(ctx, parsed.UTC())
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
