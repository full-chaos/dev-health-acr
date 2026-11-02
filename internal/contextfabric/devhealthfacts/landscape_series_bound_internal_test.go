package devhealthfacts

import "testing"

// The production client's result-row cap is derived from
// MetricsSeriesPerRepositoryRowCap per cohort member (internal/runtime/hosted).
// A landscape series keeps landscapeSeriesDays per team, so it must not need
// more rows per member than that derivation grants, or a full cohort read fails.
func TestALandscapeSeriesFitsTheDerivedResultRowCap(t *testing.T) {
	if landscapeSeriesDays > MetricsSeriesPerRepositoryRowCap {
		t.Fatalf("a team's series keeps %d rows, over the %d per member the result-row cap is derived from", landscapeSeriesDays, MetricsSeriesPerRepositoryRowCap)
	}
}
