package devhealthfacts_test

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// historicalReadRowsMargin is the room above a statement's measured reads per
// added row. A scan reports whole granules (index_granularity 8192 rows): the
// scoped touch statement reads the grown tables about 18 times over, so the
// rounding of one run is at most 18 x 8192 = 147k rows of the 2.8M it reads at
// the larger size, 5.3%. 10% is twice that and below the 100% one more pass
// over the same tables adds.
const historicalReadRowsMargin = 1.10

// historicalReadRowsStatements names the two scoped historical statements by a
// fragment only each carries, the table query_log lists for it, and the tables
// whose rows the fixture adds between the two sizes and the statement reads.
// devhealthschema:not-a-production-replica: these names select the guarded statements' query_log rows and the tables whose rows are counted; they declare no table.
var historicalReadRowsStatements = []struct {
	label, table, fragment string
	grownTables            []string
	// readsPerAddedRow is the measured (larger - smaller) read_rows over the
	// rows added to grownTables, 0 until it is pinned.
	readsPerAddedRow float64
}{
	{"historicalDropCount", "work_graph_issue_pr", "AS ever_matched", []string{"work_items", "project_membership_transitions", "work_graph_issue_pr"}, 0},
	{"historicalTouchCount", "project_membership_transitions", "AS malformed_touches", []string{"work_items", "project_membership_transitions"}, 0},
}

// historicalReadRowsRuns is the number of warm reads per size; the smallest
// is the reading, the spread between them is the floor.
const historicalReadRowsRuns = 3

// TestScopedHistoricalStatementsReadRowsFollowTheAddedRowsOnRealClickHouse
// guards the rows the two scoped historical statements read. Their memory is
// flat (the slope rows of TestScopeExpanderStatementsFollowTheRequestedScope
// OnRealClickHouse), but the rows they read grow with the unrelated rows of
// the organization: the candidate work items are found by project_id,
// to_project_id and from_project_id, none of which is in a sort key, so every
// read scans the organization's rows. That growth is the table order's and is
// not asked to vanish here. What this pins is how many rows each added
// unrelated row costs, so a change that makes the statement read the tables
// more often fails by value.
func TestScopedHistoricalStatementsReadRowsFollowTheAddedRowsOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	for _, statement := range devhealthschema.DDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	fixture := seedEveryRootFixture(t, ctx, direct, "5b8e3a17-6c24-4d09-9f71-2e0a4c6d8b53")
	since := time.Now().UTC().Add(-7 * 24 * time.Hour)
	// devhealthschema:not-a-production-replica: test seed over a table the shared DDL creates; it declares no column, type or engine.
	if err := direct.Exec(ctx, `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', 'issue-Q', 'linear', '', 'Q', '', '', '', ?, ?, 'linear:issue-Q-add')`,
		fixture.orgID, fixture.shared, since, since); err != nil {
		t.Fatalf("seed transition: %v", err)
	}
	recorder := &recordingQueryClient{inner: query}
	expander := devhealthfacts.NewScopeExpander(recorder)

	// served sends the as-of request through the expander and returns each
	// guarded statement as sent.
	served := func(t *testing.T) map[string]capturedStatement {
		recorder.reset()
		sent := false
		for _, tc := range fixture.cases() {
			if tc.name != "project repositories as of" {
				continue
			}
			sent = true
			if _, err := expander.ExpandFactScope(ctx, tc.request); err != nil {
				t.Fatalf("%s: ExpandFactScope: %v: %s", tc.name, err, readFailureCause(err))
			}
		}
		if !sent {
			t.Fatal("the as-of request is not among the fixture's cases, so no historical statement was sent")
		}
		out := map[string]capturedStatement{}
		for _, statement := range historicalReadRowsStatements {
			captured, ok := recorder.find(statement.fragment)
			if !ok {
				t.Fatalf("%s: no statement carrying %q was sent", statement.label, statement.fragment)
			}
			out[statement.label] = captured
		}
		return out
	}
	// run sends a statement and returns the rows it read from query_log.
	run := func(t *testing.T, table, fragment string, captured capturedStatement) uint64 {
		t.Helper()
		mark := serverNowMicros(t, ctx, direct)
		rows, err := query.Query(ctx, captured.statement, captured.bindings)
		if err != nil {
			t.Fatalf("run %q: %v: %s", fragment, err, readFailureCause(err))
		}
		for rows.Next() {
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read %q: %v", fragment, err)
		}
		_ = rows.Close()
		return readRowsOf(t, ctx, direct, table, fragment, mark)
	}
	type reading struct{ rows, spread uint64 }
	// sample reads a statement once unmeasured, then runs warm reads and keeps
	// the smallest and the spread.
	sample := func(t *testing.T, table, fragment string, captured capturedStatement) reading {
		t.Helper()
		run(t, table, fragment, captured)
		var rows []uint64
		for attempt := 0; attempt < historicalReadRowsRuns; attempt++ {
			rows = append(rows, run(t, table, fragment, captured))
		}
		return reading{rows: slices.Min(rows), spread: slices.Max(rows) - slices.Min(rows)}
	}
	counts := func(t *testing.T) map[string]uint64 {
		t.Helper()
		out := map[string]uint64{}
		for _, statement := range historicalReadRowsStatements {
			for _, table := range statement.grownTables {
				if _, done := out[table]; done {
					continue
				}
				var n uint64
				if err := direct.QueryRow(ctx, "SELECT count() FROM `"+table+"`").Scan(&n); err != nil {
					t.Fatalf("count %s: %v", table, err)
				}
				out[table] = n
			}
		}
		return out
	}
	measure := func(t *testing.T) (map[string]reading, map[string]uint64) {
		t.Helper()
		statements := served(t)
		readings := map[string]reading{}
		for _, statement := range historicalReadRowsStatements {
			readings[statement.label] = sample(t, statement.table, statement.fragment, statements[statement.label])
		}
		return readings, counts(t)
	}

	fillScopeOrganization(t, ctx, direct, fixture, "small", 100_000)
	mergeScopeTables(t, ctx, direct)
	small, smallCounts := measure(t)
	fillScopeOrganization(t, ctx, direct, fixture, "large", 200_000)
	mergeScopeTables(t, ctx, direct)
	large, largeCounts := measure(t)
	largeStatements := served(t)

	for _, statement := range historicalReadRowsStatements {
		label := statement.label
		s, l := small[label], large[label]
		var added uint64
		for _, table := range statement.grownTables {
			added += largeCounts[table] - smallCounts[table]
		}
		t.Logf("READROWS %s scoped small=%d large=%d (spread %d/%d) added rows %d over %v (small %v, large %v)", label, s.rows, l.rows, s.spread, l.spread, added, statement.grownTables, smallCounts, largeCounts)
		if s.rows == 0 || l.rows == 0 || added == 0 || l.rows <= s.rows {
			t.Fatalf("%s: measurement did not happen (small %+v, large %+v, added rows %d)", label, s, l, added)
		}
		floor := max(s.spread, l.spread)
		perRow := float64(l.rows-s.rows) / float64(added)
		t.Logf("READROWS %s reads per added row %.4f, floor %d rows", label, perRow, floor)
		if statement.readsPerAddedRow == 0 {
			t.Errorf("%s: reads per added row %.4f is not pinned yet (large %d rows, small %d, added %d, floor %d)", label, perRow, l.rows, s.rows, added, floor)
			continue
		}
		bound := statement.readsPerAddedRow * historicalReadRowsMargin
		allowed := uint64(math.Ceil(bound*float64(added))) + floor
		if l.rows-s.rows > allowed {
			t.Errorf("%s reads more rows per unrelated row added than measured: %d more rows for %d added rows (%.4f per row), want at most %.4f per row (%.4f measured x %.2f) and %d rows in all", label, l.rows-s.rows, added, perRow, bound, statement.readsPerAddedRow, historicalReadRowsMargin, allowed)
		}

		// The guard must be able to fail on this data: the same statement
		// sent twice in one query reads the tables twice over.
		sent := largeStatements[label]
		body := sent.statement
		if at := strings.LastIndex(body, "\nSETTINGS "); at >= 0 {
			body = body[:at]
		}
		doubled := capturedStatement{statement: fmt.Sprintf("SELECT * FROM (%s) UNION ALL SELECT * FROM (%s)", body, body), bindings: sent.bindings}
		twice := sample(t, statement.table, statement.fragment, doubled)
		t.Logf("READROWS %s sent twice in one query: %d rows at the larger size", label, twice.rows)
		if twice.rows <= l.rows+allowed-floor {
			t.Errorf("%s: the statement sent twice read %d rows, not above %d: the guard cannot fail on this data", label, twice.rows, l.rows+allowed-floor)
		}
	}
}

// readRowsOf is the most rows read by one of the statements matching table and
// fragment logged after mark.
func readRowsOf(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, table, fragment string, mark int64) uint64 {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs (query_log unavailable, measurement did not happen): %v", err)
	}
	var rows uint64
	if err := direct.QueryRow(ctx, `SELECT max(read_rows) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionBeforeStart', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.', ?)) AND position(query, ?) > 0`, mark, table, fragment).Scan(&rows); err != nil {
		t.Fatalf("read query_log read_rows: %v", err)
	}
	return rows
}
