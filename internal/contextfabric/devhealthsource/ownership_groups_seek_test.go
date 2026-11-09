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
