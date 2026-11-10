package devhealthsource_test

import (
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// The ownership page seeks the stored group log on its sort key, and the
// seek's since bound is the cursor's microsecond value bound as an Int64:
// a bound time value travels at whole seconds or milliseconds, and against
// the DateTime64(6) stored_at a truncated bound re-reads or skips the rows of
// that second.
func TestOwnershipPageSeeksTheGroupLogOnAMicrosecondBound(t *testing.T) {
	since := time.Date(2026, 11, 9, 12, 0, 0, 123456000, time.UTC)
	statement, bindings := devhealthsource.OwnershipPageStatementForTest("org-1", since, "github:P:T:native", 200)
	for _, want := range []string{
		"FROM project_team_ownership_groups_log",
		"stored_at > fromUnixTimestamp64Micro({since_us:Int64}, 'UTC')",
		"stored_at = fromUnixTimestamp64Micro({since_us:Int64}, 'UTC')",
		"row_key > {after:String}",
	} {
		if !strings.Contains(statement, want) {
			t.Errorf("the ownership page does not contain %q:\n%s", want, statement)
		}
	}
	for _, table := range []string{"team_project_ownership", "FROM projects", "FROM teams"} {
		if strings.Contains(statement, table) {
			t.Errorf("the ownership page still reads %s: it must seek the stored groups, not rebuild them", table)
		}
	}
	if strings.Contains(statement, "?") {
		t.Errorf("the ownership page carries a positional parameter")
	}
	var sinceUS any
	for _, b := range bindings {
		if _, isTime := b.Value.(time.Time); isTime {
			t.Errorf("binding %s is a time value; a stamp is bound as Int64 microseconds", b.Name)
		}
		if b.Name == "since_us" {
			sinceUS = b.Value
		}
	}
	if got, ok := sinceUS.(int64); !ok || got != since.UnixMicro() {
		t.Errorf("since_us = %#v, want int64 %d", sinceUS, since.UnixMicro())
	}
}

// The seek's ORDER BY is its whole keyset, (stored_at, row_key). One refresh
// stamps all its rows with one stored_at; ordered on stored_at alone, a LIMIT
// over more rows than a page takes an arbitrary subset of them, and the next
// seek (row_key > the page's last key) skips the rest. ClickHouse returns them
// in row-key order only while they sit in one part, so a live cell cannot show
// the loss reliably; the statement is pinned here.
func TestOwnershipSeekOrdersByItsWholeKeyset(t *testing.T) {
	statement, _ := devhealthsource.OwnershipPageStatementForTest("org-1", time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC), "github:P:T:native", 200)
	const order = "ORDER BY stored_at ASC, row_key ASC LIMIT {row_limit:UInt32}"
	// The seek appears twice: the page and the row keys its current lookup reads.
	if got := strings.Count(statement, order); got != 2 {
		t.Errorf("the seek orders by %q %d times, want 2 (the page and its current lookup):\n%s", order, got, statement)
	}
	if got := strings.Count(statement, "ORDER BY stored_at ASC LIMIT"); got != 0 {
		t.Errorf("a seek orders on stored_at alone:\n%s", statement)
	}
}
