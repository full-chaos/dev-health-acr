package devhealthfacts

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestStatedWindowBoundTakesOnlyAStatedCurrentWindow(t *testing.T) {
	now := clock().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	start, pastEnd := today.AddDate(0, 0, -90), today.AddDate(0, 0, -30)
	query := func(axis contextfabric.TemporalAxis, window *contractsv1.ContextFabricRequestedEvidenceWindow) contextfabric.FactQuery {
		return contextfabric.FactQuery{Time: contextfabric.TimeContext{Axis: axis, EvidenceWindow: window}}
	}
	given := factTimeBound{active: true, end: pastEnd}

	unchanged := []struct {
		name  string
		query contextfabric.FactQuery
	}{
		{"another axis", query(contextfabric.TemporalValidTime, &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &today})},
		{"no window", query(contextfabric.TemporalCurrent, nil)},
		{"no start", query(contextfabric.TemporalCurrent, &contractsv1.ContextFabricRequestedEvidenceWindow{End: &today})},
		{"no end", query(contextfabric.TemporalCurrent, &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start})},
		{"all_time", query(contextfabric.TemporalCurrent, &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &today, RelativeID: contractsv1.ContextFabricRelativeWindowAllTime})},
	}
	for _, c := range unchanged {
		if got := statedWindowBound(c.query, given); got != given {
			t.Errorf("%s: bound = %+v, want the given one", c.name, got)
		}
	}

	past := statedWindowBound(query(contextfabric.TemporalCurrent, &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &pastEnd}), factTimeBound{})
	if !past.active || !past.hasStart || !past.currentAxis || !past.statedWindow || !past.endExclusive || !past.end.Equal(pastEnd) || !past.start.Equal(start) {
		t.Errorf("a past window = %+v, want its two ends, exclusive", past)
	}
	upToToday := statedWindowBound(query(contextfabric.TemporalCurrent, &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &today}), factTimeBound{})
	if upToToday.endExclusive || upToToday.end.Before(today) || !upToToday.currentAxis || !upToToday.statedWindow {
		t.Errorf("a window up to today = %+v, want today's rows held", upToToday)
	}
}

func TestAStatedWindowLeavesOwnershipOnTheCurrentRule(t *testing.T) {
	today := clock().UTC()
	start := today.AddDate(0, 0, -90)
	bound := statedWindowBound(contextfabric.FactQuery{Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &today}}}, factTimeBound{})
	if got, want := ownershipValidityPredicate(bound), ownershipValidityPredicate(factTimeBound{}); got != want {
		t.Fatalf("ownership predicate = %q, want the current rule %q", got, want)
	}
	if state, reason := bound.retentionState(0); state != contextfabric.SourceNoData || reason != statedWindowEmptyReason {
		t.Fatalf("empty read = %s, %q", state, reason)
	}
}
