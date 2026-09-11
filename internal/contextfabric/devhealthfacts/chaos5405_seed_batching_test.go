package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
)

// THE SEED'S SHAPE IS PART OF THE FIXTURE'S CONTRACT.
//
// seedChaos5405Fixture writes its distinct-key rows as ONE statement per
// table, and the re-synced duplicate of WI-0 as its own statement after them.
// Both halves are load-bearing, and neither is visible in the assertions of
// the tests that use the fixture:
//
//   - batched: at repoBackedCount=200 the row-at-a-time form issued 410
//     statements and wrote 410 parts. The server carries and merges those.
//   - the duplicate is NOT batched: ClickHouse applies the engine's merging
//     algorithm to a single insert block (optimize_on_insert, default on), so
//     a duplicate written in the SAME statement as its original collapses as
//     the part is written. The fixture needs an ACROSS-PARTS duplicate --
//     that is what FINAL resolves, and what the census assertions
//     distinguish. Batching it away would leave every one of those tests
//     green while the state they describe no longer existed.
//
// This test executes both properties directly, so a future tidy-up of the
// seed cannot quietly remove either.
func TestChaos5405_TheSeedBatchesItsRowsButNotTheResyncDuplicate(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	_, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC().Truncate(time.Second)

	// MERGES OFF FOR THE DURATION, or this test measures the merge scheduler
	// rather than the seed. The two WI-0 rows live in two parts, and a
	// background merge of a ReplacingMergeTree collapses them whenever it
	// happens to run -- which it did, between the insert and the count, when
	// this test ran inside the whole package rather than alone. What the
	// fixture must guarantee is the state AS WRITTEN; how long the server
	// keeps it before merging is the server's business, and every consumer of
	// the fixture reads through FINAL, which is correct either way.
	if err := direct.Exec(ctx, `SYSTEM STOP MERGES work_items`); err != nil {
		t.Fatalf("stop merges: %v", err)
	}
	t.Cleanup(func() {
		if err := direct.Exec(context.Background(), `SYSTEM START MERGES work_items`); err != nil {
			t.Errorf("start merges: %v", err)
		}
	})

	counter := &countingExec{conn: direct}
	seedChaos5405Fixture(t, ctx, counter, orgID, at, 200)
	t.Logf("seed statements for repoBackedCount=200: %d", counter.statements)

	// The row-at-a-time form issued 2 + 200*2 + 8 = 410. The bound is the
	// statement COUNT, not the row count: the rows are unchanged.
	if counter.statements > 12 {
		t.Errorf("seed issued %d statements, want at most 12 -- the distinct-key rows must go one statement per table, not one per row", counter.statements)
	}

	count := func(label, statement string) uint64 {
		t.Helper()
		var n uint64
		if err := direct.QueryRow(ctx, statement, orgID).Scan(&n); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		return n
	}
	rows := count("count without FINAL", `SELECT count() FROM work_items WHERE org_id = ? AND work_item_id = 'WI-0'`)
	final := count("count with FINAL", `SELECT count() FROM work_items FINAL WHERE org_id = ? AND work_item_id = 'WI-0'`)
	parts := count("distinct parts", `SELECT uniqExact(_part) FROM work_items WHERE org_id = ? AND work_item_id = 'WI-0'`)
	t.Logf("WI-0: rows=%d final=%d parts=%d", rows, final, parts)

	// THE DISCRIMINATING PAIR. Equal counts would mean the duplicate is gone,
	// whatever the reason, and every FINAL assertion in this package's census
	// tests would then be measuring a table that has nothing to collapse.
	if rows != 2 {
		t.Errorf("work_items rows for WI-0 = %d, want 2 -- the re-synced duplicate is not in the table", rows)
	}
	if final != 1 {
		t.Errorf("work_items FINAL rows for WI-0 = %d, want 1 -- FINAL must collapse the re-synced duplicate", final)
	}
	if rows == final {
		t.Errorf("FINAL and non-FINAL agree at %d rows, so this fixture no longer carries a duplicate for FINAL to resolve", rows)
	}
	if parts < 2 {
		t.Errorf("WI-0's rows live in %d part(s), want at least 2 -- a duplicate inside ONE part is collapsed on insert and is not the across-parts state the census tests describe", parts)
	}
}

// countingExec counts the statements the seed issues, and forwards them.
type countingExec struct {
	conn       clickhousedriver.Conn
	statements int
}

func (c *countingExec) Exec(ctx context.Context, query string, args ...any) error {
	c.statements++
	return c.conn.Exec(ctx, query, args...)
}
