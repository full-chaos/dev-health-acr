package factoracle

import (
	"testing"
	"time"
)

// TestTheOpsDateRangeEndsOnTheLastDayTheWindowHolds: ops reads endDate as the
// last day of the range, inclusive (D6101); the window's End is exclusive.
func TestTheOpsDateRangeEndsOnTheLastDayTheWindowHolds(t *testing.T) {
	w := Window{Start: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)}
	dateRangeOf := func(variables map[string]any) map[string]any {
		return variables["batch"].(map[string]any)["breakdowns"].([]any)[0].(map[string]any)["dateRange"].(map[string]any)
	}
	for name, variables := range map[string]map[string]any{
		"investmentVariables":   investmentVariables(w, "THEME"),
		"workItemTeamVariables": workItemTeamVariables(w),
	} {
		got := dateRangeOf(variables)
		if got["startDate"] != "2026-10-21" || got["endDate"] != "2026-11-20" {
			t.Errorf("%s dateRange = %v, want startDate 2026-10-21 and endDate 2026-11-20", name, got)
		}
	}
}

// TestEachReaderCountsTheCaptureDayByItsOwnRule: the default venue window ends
// at the next UTC midnight. The ops analytics readers leave the capture day out
// (D6101), so the analytics pairs read up to its start; capacityForecasts asks
// toDate = the capture day and counts the forecasts computed on it.
func TestEachReaderCountsTheCaptureDayByItsOwnRule(t *testing.T) {
	now := time.Date(2026, 11, 20, 16, 30, 13, 0, time.FixedZone("x", 3600))
	end := venueWindowEnd(now)
	if want := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC); !end.Equal(want) {
		t.Fatalf("venueWindowEnd = %s, want %s", end, want)
	}
	o := &Oracle{Window: Window{Start: end.Add(-MaxWindowDays * 24 * time.Hour), End: end}, Today: now.UTC().Truncate(24 * time.Hour)}
	if got := o.analyticsWindow().End; !got.Equal(o.Today) {
		t.Errorf("analytics window ends %s, want the start of the capture day %s", got, o.Today)
	}
	if got := o.Window.lastDay(); got != "2026-11-20" {
		t.Errorf("capacityForecasts toDate = %s, want the capture day 2026-11-20", got)
	}
	past := &Oracle{Window: Window{Start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}, Today: o.Today}
	if got := past.analyticsWindow(); got != past.Window {
		t.Errorf("a window of finished days is cut to %v", got)
	}
	if got := (&Oracle{Window: o.Window}).analyticsWindow(); got != o.Window {
		t.Errorf("with no Today the analytics window is cut to %v", got)
	}
}

// TestAnOpsScopeIDWithAColonKeysTheSameSubjectAsTheFact: a provider-composite
// team id holds a colon; the ops row must key the same subject as the fact's
// canonical id.
func TestAnOpsScopeIDWithAColonKeysTheSameSubjectAsTheFact(t *testing.T) {
	for _, id := range []string{"gh:identity", "gl:chaos-gl/data", "core"} {
		if got, want := opsScopeKey("team", id), bareID("team:"+id); got != want {
			t.Errorf("opsScopeKey(team, %q) = %q, the fact key is %q", id, got, want)
		}
	}
	if got := opsScopeKey("repository", "repository:abc"); got != "abc" {
		t.Errorf("opsScopeKey keeps the kind prefix: %q", got)
	}
}

// TestTheCatalogComparesOwnerNameSlugsOnly: the ops repository inventory keeps
// owner/name slugs only.
func TestTheCatalogComparesOwnerNameSlugsOnly(t *testing.T) {
	for name, want := range map[string]bool{"chaos-fixtures/shared-api": true, "acme/live-e2e": true, "chaos-gl/data/pipeline": false, "solo": false} {
		if got := ownerNameSlug.MatchString(name); got != want {
			t.Errorf("ownerNameSlug(%q) = %t, want %t", name, got, want)
		}
	}
}
