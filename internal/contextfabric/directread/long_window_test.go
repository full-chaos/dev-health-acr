package directread

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func TestWindowMaximumIsDeclaredPerKind(t *testing.T) {
	reader := &FactsReader{now: func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }}
	end := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	investment := []contextfabric.FactKind{contextfabric.FactInvestment}
	mixed := []contextfabric.FactKind{contextfabric.FactInvestment, contextfabric.FactHealth}
	cases := []struct {
		name    string
		kinds   []contextfabric.FactKind
		window  RequestWindow
		refused bool
	}{
		{"investment trailing 61", investment, RequestWindow{Mode: WindowTrailing, Days: 61}, false},
		{"investment trailing 180", investment, RequestWindow{Mode: WindowTrailing, Days: 180}, false},
		{"investment trailing 365", investment, RequestWindow{Mode: WindowTrailing, Days: 365}, false},
		{"investment trailing 366", investment, RequestWindow{Mode: WindowTrailing, Days: 366}, true},
		{"investment range 180", investment, RequestWindow{Mode: WindowRange, Start: ptrTime(end.Add(-180 * 24 * time.Hour)), End: &end}, false},
		{"investment range 366", investment, RequestWindow{Mode: WindowRange, Start: ptrTime(end.Add(-366 * 24 * time.Hour)), End: &end}, true},
		{"health trailing 60", []contextfabric.FactKind{contextfabric.FactHealth}, RequestWindow{Mode: WindowTrailing, Days: 60}, false},
		{"health trailing 61", []contextfabric.FactKind{contextfabric.FactHealth}, RequestWindow{Mode: WindowTrailing, Days: 61}, true},
		{"mixed trailing 180", mixed, RequestWindow{Mode: WindowTrailing, Days: 180}, true},
		{"mixed trailing 60", mixed, RequestWindow{Mode: WindowTrailing, Days: 60}, false},
	}
	for _, c := range cases {
		window := c.window
		_, effective, err := reader.window(&window, MaxRangeDaysFor(c.kinds))
		if c.refused != (err != nil) {
			t.Errorf("%s: err %v, refused want %v", c.name, err, c.refused)
		}
		if err == nil && c.window.Mode == WindowTrailing && effective.Start != nil && effective.End.Sub(*effective.Start) != time.Duration(c.window.Days)*24*time.Hour {
			t.Errorf("%s: echoed span %v", c.name, effective.End.Sub(*effective.Start))
		}
	}
}

func TestWindowRefusalAdviceFollowsTheKind(t *testing.T) {
	reader := &FactsReader{now: func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }}
	_, _, err := reader.window(&RequestWindow{Mode: WindowTrailing, Days: 366}, MaxRangeDaysFor([]contextfabric.FactKind{contextfabric.FactInvestment}))
	if err == nil || !strings.Contains(err.Error(), "read a longer period as several windows") || !strings.Contains(err.Error(), "in the window that holds its end stamp") || strings.Contains(err.Error(), "do not add shorter windows") {
		t.Errorf("investment refusal: %v", err)
	}
	_, _, err = reader.window(&RequestWindow{Mode: WindowTrailing, Days: 61}, MaxRangeDaysFor([]contextfabric.FactKind{contextfabric.FactHealth}))
	if err == nil || !strings.Contains(err.Error(), "read a longer period as several windows") {
		t.Errorf("health refusal: %v", err)
	}
}

func TestWindowRefusalIsATypedTokenWithMaxDays(t *testing.T) {
	reader := &FactsReader{now: func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }}
	for _, c := range []struct {
		kind contextfabric.FactKind
		days int
		want int
	}{
		{contextfabric.FactHealth, 61, 60},
		{contextfabric.FactInvestment, 366, 365},
	} {
		kinds := []contextfabric.FactKind{c.kind}
		_, _, err := reader.window(&RequestWindow{Mode: WindowTrailing, Days: c.days}, MaxRangeDaysFor(kinds))
		var re *RequestError
		if !errors.As(err, &re) || re.Reason != "window_beyond_kind_max" || re.MaxDays != c.want {
			t.Errorf("%s %d d: %#v", c.kind, c.days, err)
		}
		end := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		_, _, err = reader.window(&RequestWindow{Mode: WindowRange, Start: &[]time.Time{end.AddDate(0, 0, -c.want-1)}[0], End: &end}, c.want)
		if !errors.As(err, &re) || re.Reason != "window_beyond_kind_max" || re.MaxDays != c.want {
			t.Errorf("%s range: %#v", c.kind, err)
		}
	}
}

func TestRangeMaximumCountsTheInclusiveEndDay(t *testing.T) {
	reader := &FactsReader{now: func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }}
	kinds := []contextfabric.FactKind{contextfabric.FactHealth}
	max := MaxRangeDaysFor(kinds)
	midnight := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	noon := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		end     time.Time
		days    int
		refused bool
	}{
		{"max days ending at midnight reads max+1 days", midnight, max, true},
		{"max-1 days ending at midnight reads max days", midnight, max - 1, false},
		{"max days not ending at midnight", noon, max, false},
		{"over max not ending at midnight", noon, max + 1, true},
	}
	for _, c := range cases {
		end := c.end
		start := end.AddDate(0, 0, -c.days)
		_, effective, err := reader.window(&RequestWindow{Mode: WindowRange, Start: &start, End: &end}, max)
		var re *RequestError
		if c.refused {
			if !errors.As(err, &re) || re.Reason != "window_beyond_kind_max" || re.MaxDays != max {
				t.Errorf("%s: %#v", c.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: refused: %v", c.name, err)
			continue
		}
		if end.Equal(midnight) {
			days := int(contextfabric.DailyWindowEnd(*effective.End, false).Sub(effective.Start.UTC().Truncate(24*time.Hour)) / (24 * time.Hour))
			if days != max {
				t.Errorf("%s: served %d days, want %d", c.name, days, max)
			}
		}
	}
}
