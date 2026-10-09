package devhealthfacts

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// investmentSpan collects the earliest persisted work unit start of each
// subject of one read. A team or repository gets its OWN earliest unit: the
// mix statement reports, per repository, the earliest unit start over every
// unit attributed to it (a sentinel window with no window predicate), and a
// team takes the earliest over the repositories it owns. A project has no
// per-subject value: the unit-to-project link is resolved after the window
// pin, so it carries the organization's earliest unit, labelled as such.
type investmentSpan struct {
	mu        sync.Mutex
	bySubject map[string]time.Time
	org       time.Time
	orgFound  bool
}

type investmentSpanKey struct{}

func withInvestmentSpan(ctx context.Context) (context.Context, *investmentSpan) {
	span := &investmentSpan{bySubject: map[string]time.Time{}}
	return context.WithValue(ctx, investmentSpanKey{}, span), span
}

func investmentSpanFrom(ctx context.Context) *investmentSpan {
	span, _ := ctx.Value(investmentSpanKey{}).(*investmentSpan)
	return span
}

// recordInvestmentSpan keeps the earliest start seen for one canonical subject id.
func recordInvestmentSpan(ctx context.Context, subjectID string, from time.Time) {
	span := investmentSpanFrom(ctx)
	if span == nil || from.IsZero() || from.Unix() <= 0 {
		return
	}
	span.mu.Lock()
	defer span.mu.Unlock()
	if prev, ok := span.bySubject[subjectID]; !ok || from.Before(prev) {
		span.bySubject[subjectID] = from.UTC()
	}
}

// recordInvestmentOrgSpan keeps the organization's earliest unit start (the
// project scope statement's column). hadRows false skips the epoch default of
// an aggregate over no rows.
func recordInvestmentOrgSpan(ctx context.Context, from time.Time, hadRows bool) {
	span := investmentSpanFrom(ctx)
	if span == nil || !hadRows || from.IsZero() || from.Unix() <= 0 {
		return
	}
	span.mu.Lock()
	defer span.mu.Unlock()
	if !span.orgFound || from.Before(span.org) {
		span.org, span.orgFound = from.UTC(), true
	}
}

const investmentSpanReasonToken = "investment_window_beyond_stored_history"

// reasonFor is the disclosure for a subject whose window starts before its
// earliest persisted work unit: the fact is served over the available span and
// the days before it are not stored, which is not the same as zero. Empty when
// the window is inside the stored history or names no start.
func (s *investmentSpan) reasonFor(subjectID string, project bool, b factTimeBound) string {
	if s == nil || !b.active || !b.hasStart {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if project {
		if !s.orgFound || !b.start.Before(s.org) {
			return ""
		}
		return fmt.Sprintf("%s: %s: the window starts %s but the earliest persisted work unit of the organization starts %s; this project's own span is not derived",
			investmentSpanReasonToken, subjectID, b.start.UTC().Format(time.RFC3339), s.org.UTC().Format(time.RFC3339))
	}
	earliest, ok := s.bySubject[subjectID]
	if !ok || !b.start.Before(earliest) {
		return ""
	}
	return fmt.Sprintf("%s: %s: the window starts %s but its earliest persisted work unit starts %s; the mix covers %s to %s only and the days before are not stored (not zero)",
		investmentSpanReasonToken, subjectID, b.start.UTC().Format(time.RFC3339), earliest.UTC().Format(time.RFC3339), earliest.UTC().Format(time.RFC3339), b.end.UTC().Format(time.RFC3339))
}
