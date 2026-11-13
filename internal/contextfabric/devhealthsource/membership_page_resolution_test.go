package devhealthsource

import (
	"strings"
	"testing"
	"time"
)

// The membership page reads projects ONCE, and only after the page is cut:
// the keyset, order and limit sit inside the paged arm rows, ahead of the one
// project resolution join.
func TestMembershipPageResolvesProjectsOnceAfterThePageIsCut(t *testing.T) {
	t.Parallel()
	cursors := []cursorState{{}, {After: "k"}, {Since: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), After: "k"}}
	for _, ingest := range []bool{false, true} {
		for _, pruned := range []bool{false, true} {
			for _, cursor := range cursors {
				statement := membershipStatement(cursor, ingest, pruned)
				if got := strings.Count(statement, "FROM projects FINAL"); got != 1 {
					t.Errorf("ingest=%v pruned=%v cursor=%+v: projects read %d times per page, want 1", ingest, pruned, cursor, got)
				}
				limit := strings.Index(statement, "LIMIT {row_limit:UInt32}")
				join := strings.Index(statement, "LEFT JOIN "+resolvedProjectsSubquery)
				if limit < 0 || join < 0 || limit > join {
					t.Errorf("ingest=%v pruned=%v cursor=%+v: the page limit (at %d) must come before the project resolution (at %d)", ingest, pruned, cursor, limit, join)
				}
				if strings.Count(statement, "LEFT JOIN "+resolvedProjectsSubquery) != 1 {
					t.Errorf("ingest=%v pruned=%v cursor=%+v: want exactly one project resolution join", ingest, pruned, cursor)
				}
			}
		}
	}
}
