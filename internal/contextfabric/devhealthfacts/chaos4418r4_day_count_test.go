package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestCHAOS4418RepositoryDayCountIsNotCappedByThePerRepositoryRowLimit is
// codex R4 finding 3 (P2). `LIMIT 200 BY repo_id` runs inside ClickHouse,
// BEFORE Go ever groups the returned rows, so a day_count taken from
// len(rows) silently saturates at MetricsSeriesPerRepositoryRowCap: a
// 250-day window reported "250 days" as "200 days" -- an EXACT count, which
// a model may ground a claim in, that is simply wrong. The true distinct-day
// count is computed in the same query, before the cap.
func TestCHAOS4418RepositoryDayCountIsNotCappedByThePerRepositoryRowLimit(t *testing.T) {
	t.Parallel()
	const trueDays = 250
	// What the SERVER returns for a 250-day window: the cap already fired,
	// so only MetricsSeriesPerRepositoryRowCap rows come back -- each
	// carrying the pre-cap distinct-day count the query computed for its
	// own repository.
	rows := metricsRowsForOneRepoOverDays("repo-1", devhealthfacts.MetricsSeriesPerRepositoryRowCap)
	for _, row := range rows {
		row[10] = int64(trueDays)
	}
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: rows}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	got := result.Facts[0].Fields["day_count"].Integer
	if got == nil || *got != trueDays {
		t.Fatalf("day_count = %v, want the true %d distinct days -- a count taken from the returned row slice saturates at the %d-row per-repository SQL cap and grounds a false exact count", got, trueDays, devhealthfacts.MetricsSeriesPerRepositoryRowCap)
	}
	// Truncation semantics are unchanged: the 64-row per-FACT cap is what
	// Truncated reports, independent of the per-repository SQL cap above.
	if !result.Truncated {
		t.Fatalf("result.Truncated = false, want true -- %d rows still exceed the 64-row per-fact cap", devhealthfacts.MetricsSeriesPerRepositoryRowCap)
	}
	if rowCount := len(result.Facts[0].Fields["daily_metrics"].Rows); rowCount != contextfabric.MaxFactValueRows {
		t.Fatalf("daily_metrics = %d rows, want the unchanged %d-row per-fact cap", rowCount, contextfabric.MaxFactValueRows)
	}
}

// TestCHAOS4418RepositoryDayCountComesFromTheQueryNotTheRowSlice pins the
// mechanism the fixture above stands in for. The fake client does not
// execute SQL, so it can only replay a total_days column the statement must
// actually ask for -- without this pin, the test above would pass against a
// query that never computes the count, and the fixture would be fiction.
// The count window has to sit AFTER the intraday-rerun dedup (rn = 1) and
// BEFORE the per-repository LIMIT, which is the only position where it
// means "distinct days for this repository in this window".
func TestCHAOS4418RepositoryDayCountComesFromTheQueryNotTheRowSlice(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	statement := client.queries[len(client.queries)-1].statement
	if !strings.Contains(statement, "count() OVER (PARTITION BY repo_id) AS total_days") {
		t.Fatalf("statement = %q, want a per-repository distinct-day count computed in the query itself", statement)
	}
	// Nested SELECTs are written outside-in, so a query level that appears
	// EARLIER in the text is evaluated LATER. The count window must sit at
	// a level outside the row_number() dedup (so it counts days, not
	// intraday reruns) and inside the per-repository LIMIT (so the cap
	// cannot saturate it) -- textually: after the outer SELECT, before the
	// row_number(), and before the trailing LIMIT. That the dedup level it
	// wraps is actually filtered is pinned separately by `WHERE rn = 1`.
	countAt := strings.Index(statement, "count() OVER")
	dedupAt := strings.Index(statement, "row_number() OVER")
	limitAt := strings.Index(statement, "LIMIT ")
	if dedupAt < 0 || countAt > dedupAt {
		t.Fatalf("statement = %q, want the day count at a query level OUTSIDE the row_number() dedup -- counting inside it counts intraday reruns, not days", statement)
	}
	if !strings.Contains(statement, "WHERE rn = 1") {
		t.Fatalf("statement = %q, want the dedup level the count wraps to be filtered to rn = 1", statement)
	}
	if limitAt < 0 || countAt > limitAt {
		t.Fatalf("statement = %q, want the day count computed BEFORE the per-repository LIMIT -- that is the whole point of the finding", statement)
	}
	if got := strings.Count(strings.ToUpper(statement), "LIMIT"); got != 1 {
		t.Fatalf("statement = %q, want exactly 1 LIMIT clause total (unchanged from CHAOS-4418's own per-repository cap pin)", statement)
	}
}
