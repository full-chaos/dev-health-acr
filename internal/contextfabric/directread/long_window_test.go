package directread

import (
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
