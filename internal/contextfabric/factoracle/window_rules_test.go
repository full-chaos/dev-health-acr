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

// TestTheDefaultVenueWindowHoldsFinishedDaysOnly: ops counts no day that is
// not over, so the default window ends at the start of the current UTC day.
func TestTheDefaultVenueWindowHoldsFinishedDaysOnly(t *testing.T) {
	now := time.Date(2026, 11, 20, 16, 30, 13, 0, time.FixedZone("x", 3600))
	if got, want := venueWindowEnd(now), time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("venueWindowEnd = %s, want %s", got, want)
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
