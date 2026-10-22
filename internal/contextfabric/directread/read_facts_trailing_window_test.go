package directread

import (
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestTrailingWindowEndsAtTodayMidnightUTC(t *testing.T) {
	today := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{time.Second, 12 * time.Hour, 24*time.Hour - time.Second} {
		now := today.Add(offset)
		reader := &FactsReader{now: func() time.Time { return now }}
		for _, days := range []int{1, 7, 30, 365} {
			window := &RequestWindow{Mode: WindowTrailing, Days: days}
			_, effective, err := reader.window(window, 365)
			if err != nil {
				t.Fatalf("offset %v days %d: %v", offset, days, err)
			}
			wantStart := today.AddDate(0, 0, -days)
			if effective.Start == nil || effective.End == nil || !effective.Start.Equal(wantStart) || !effective.End.Equal(today) {
				t.Errorf("offset %v days %d: [%v, %v), want [%v, %v)", offset, days, effective.Start, effective.End, wantStart, today)
			}
			if !strings.Contains(effective.Note, "completed UTC days") || !strings.Contains(effective.Note, "today") {
				t.Errorf("offset %v days %d: note %q does not name the rule", offset, days, effective.Note)
			}
		}
	}
}

func TestTrailingWindowStillRefusesBeyondMaxAndKeepsRangeAndAsOf(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 30, 0, 0, time.UTC)
	reader := &FactsReader{now: func() time.Time { return now }}
	if _, _, err := reader.window(&RequestWindow{Mode: WindowTrailing, Days: 366}, 365); err == nil {
		t.Fatal("366 days beyond max 365 was accepted")
	}
	start, end := time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC), time.Date(2026, 10, 5, 18, 0, 0, 0, time.UTC)
	_, effective, err := reader.window(&RequestWindow{Mode: WindowRange, Start: &start, End: &end}, 365)
	if err != nil || !effective.Start.Equal(start) || !effective.End.Equal(end) {
		t.Fatalf("range changed: %+v, %v", effective, err)
	}
	asOf := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	_, effective, err = reader.window(&RequestWindow{Mode: WindowAsOf, AsOf: &asOf}, 365)
	if err != nil || effective.AsOf == nil || !effective.AsOf.Equal(asOf) {
		t.Fatalf("as_of changed: %+v, %v", effective, err)
	}
}

func TestTrailingReadHandsProvidersAnExclusiveEndAndExplicitRangeDoesNot(t *testing.T) {
	for _, c := range []struct {
		name      string
		window    *RequestWindow
		exclusive bool
	}{
		{"trailing", &RequestWindow{Mode: WindowTrailing, Days: 7}, true},
		{"range", &RequestWindow{Mode: WindowRange, Start: ptrTime(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)), End: ptrTime(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC))}, false},
	} {
		reader := &FactsReader{now: func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }}
		plan, err := reader.validate(FactsRequest{Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "repository", CanonicalID: "repository:r"}}, Window: c.window},
			map[contextfabric.FactKind]contextfabric.FactCapability{contextfabric.FactHealth: {}})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if plan.timeEndExclusive != c.exclusive {
			t.Errorf("%s: timeEndExclusive = %v, want %v", c.name, plan.timeEndExclusive, c.exclusive)
		}
	}
}

func TestTrailingReadReachesTheProviderQueryWithAnExclusiveEnd(t *testing.T) {
	for _, c := range []struct {
		name      string
		window    *RequestWindow
		exclusive bool
	}{
		{"trailing", &RequestWindow{Mode: WindowTrailing, Days: 7}, true},
		{"range", &RequestWindow{Mode: WindowRange, Start: ptrTime(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)), End: ptrTime(time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC))}, false},
	} {
		provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
			return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
		}}
		reader := newTestFactsReader(t, graphOfOrgA(), provider)
		request := FactsRequest{Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "repository", CanonicalID: repoA.CanonicalID}}, Window: c.window}
		if _, err := reader.Read(requestContext(), unrestrictedA(), request); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(provider.queries) == 0 {
			t.Fatalf("%s: the provider was not queried", c.name)
		}
		if got := provider.queries[len(provider.queries)-1].TimeEndExclusive; got != c.exclusive {
			t.Errorf("%s: provider FactQuery.TimeEndExclusive = %v, want %v", c.name, got, c.exclusive)
		}
	}
}
