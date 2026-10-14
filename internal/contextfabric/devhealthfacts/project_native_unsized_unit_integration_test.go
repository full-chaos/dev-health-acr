package devhealthfacts_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// A work unit with no stored size weighs one in the native project mix: it is
// counted, its theme reaches the mix, and it is never left out. The unit is
// stored with effort 0 (insertVersionAtMilli), the shape the old
// effort_value-weighted read dropped from every sum and from effort_units.
func TestProjectNativeMixKeepsAUnitWithNoStoredEffortAgainstRealClickHouse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	query, direct := newScopedCHAOS7257Client(t, nil)
	createCHAOS7257Tables(t, ctx, direct)
	const orgID = "org-unsized-native-unit"
	at := seedCHAOS7257Native(t, ctx, direct, orgID)
	ids := []string{"linear:n-p1", "linear:n-p2", "linear:n-p3"}
	window := devhealthfacts.ProjectMixWindow{}
	byKey := func(rows []nativeRow, key string) nativeRow {
		for _, r := range rows {
			if r.Key == key {
				return r
			}
		}
		t.Fatalf("no row for %s in %+v", key, rows)
		return nativeRow{}
	}
	before := byKey(runNativePhased(t, ctx, query, orgID, ids, window, 201), "linear:n-p1")

	insertNativeVersion(t, ctx, direct, orgID, "nu-unsized", "feature_delivery", at.Add(-time.Hour))
	insertMembership(t, ctx, direct, orgID, "nu-unsized", at)
	after := byKey(runNativePhased(t, ctx, query, orgID, ids, window, 201), "linear:n-p1")

	if after.WorkUnits != before.WorkUnits+1 {
		t.Errorf("work units %d, want %d: the unsized unit is not counted", after.WorkUnits, before.WorkUnits+1)
	}
	if after.EffortUnits != before.EffortUnits+1 {
		t.Errorf("counted units %d, want %d: the unsized unit is left out of the count", after.EffortUnits, before.EffortUnits+1)
	}
	if got, want := after.Themes[0], before.Themes[0]+1; math.Abs(got-want) > 1e-9 {
		t.Errorf("feature_delivery %v, want %v: the unsized unit weighs one", got, want)
	}
}
