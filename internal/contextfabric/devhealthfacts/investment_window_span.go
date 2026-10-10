package devhealthfacts

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// investmentSpan collects the earliest persisted work unit start of each
// subject of one read. A team or repository gets its OWN earliest unit: the
// mix statement reports, per repository, the earliest unit start over every
// unit attributed to it (a sentinel window with no window predicate), and a
// team takes the earliest over the repositories it owns. A project has no
// all-history value: the unit-to-project link is resolved after the window pin,
// so a project carries the organization's earliest unit and, separately, its own
// earliest linked unit among those overlapping the window.
type investmentSpan struct {
	mu        sync.Mutex
	bySubject map[string]time.Time
	// byProject is each project's earliest linked unit overlapping the window,
	// keyed "provider:id". It is read from the phases that already join units to
	// projects, so it is not an all-history value.
	byProject map[string]time.Time
	org       time.Time
	orgFound  bool
}

type investmentSpanKey struct{}

func withInvestmentSpan(ctx context.Context) (context.Context, *investmentSpan) {
	span := &investmentSpan{bySubject: map[string]time.Time{}, byProject: map[string]time.Time{}}
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

// recordInvestmentProjectSpan keeps the earliest linked unit start seen for one
// project key ("provider:id") among the units overlapping the window.
func recordInvestmentProjectSpan(ctx context.Context, projectKey string, from time.Time) {
	span := investmentSpanFrom(ctx)
	if span == nil || from.IsZero() || from.Unix() <= 0 {
		return
	}
	span.mu.Lock()
	defer span.mu.Unlock()
	if prev, ok := span.byProject[projectKey]; !ok || from.Before(prev) {
		span.byProject[projectKey] = from.UTC()
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

// investmentProjectFirstUnitToken names the project-own disclosure: the earliest
// linked unit among those overlapping the window, never a stored-history claim.
const investmentProjectFirstUnitToken = "investment_project_window_first_unit"

// reasonFor is the disclosure for a subject whose window starts before its
// earliest persisted work unit: the fact is served over the available span and
// the days before it are not stored, which is not the same as zero. Empty when
// the window is inside the stored history or names no start.
func (s *investmentSpan) reasonFor(subjectID, projectKey string, project bool, b factTimeBound) string {
	if s == nil || !b.active || !b.hasStart {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if project {
		var reasons []string
		if s.orgFound && b.start.Before(s.org) {
			reasons = append(reasons, fmt.Sprintf("%s: %s: the window starts %s but the earliest persisted work unit of the organization starts %s",
				investmentSpanReasonToken, subjectID, b.start.UTC().Format(time.RFC3339), s.org.UTC().Format(time.RFC3339)))
		}
		if own, ok := s.byProject[projectKey]; ok && b.start.Before(own) {
			reasons = append(reasons, fmt.Sprintf("%s: %s: this project's earliest linked unit overlapping the window starts %s; the window starts %s and the history before the window was not read",
				investmentProjectFirstUnitToken, subjectID, own.UTC().Format(time.RFC3339), b.start.UTC().Format(time.RFC3339)))
		}
		return strings.Join(reasons, "; ")
	}
	earliest, ok := s.bySubject[subjectID]
	if !ok || !b.start.Before(earliest) {
		return ""
	}
	return fmt.Sprintf("%s: %s: the window starts %s but its earliest persisted work unit starts %s; the mix covers %s to %s only and the days before are not stored (not zero)",
		investmentSpanReasonToken, subjectID, b.start.UTC().Format(time.RFC3339), earliest.UTC().Format(time.RFC3339), earliest.UTC().Format(time.RFC3339), b.end.UTC().Format(time.RFC3339))
}
