package devhealthsource

import (
	"strings"
	"testing"
)

// CHAOS-4750. queryProjectTeams' rendered statement scanned `projects
// FINAL` 12 times per read on origin/main -- measured directly against this
// package's own unexported projectTeamsStatement, not estimated, before any
// SQL here changed. Four arms (CHAOS-4566), each embedding its own copy of
// the project-identity source: arms A and B each embed
// projectIdentityWithWatermarkSQL() once (3 scans: 2 from
// readers.ProjectIdentityCatalogSQL()'s own internal id/key union, 1 more
// from the watermark join's own separate `FROM projects FINAL`), and arms C
// and D each embed ambiguousProjectIdentitySQL(), which itself wraps
// projectIdentityWithWatermarkSQL() once more (3 scans each too). 4 arms *
// 3 scans = 12.
//
// This is the ClickHouse-planner-accurate proxy, not a guess: EXPLAIN PLAN's
// ReadFromMergeTree node count and this substring count were cross-validated
// 1:1 against real ClickHouse 26.7 in CHAOS-4552, because the planner does
// not deduplicate or share scans across textually-independent subquery
// occurrences, even byte-identical ones (a `WITH` CTE reference is inlined
// per reference, not materialized once, so it does not help either).
//
// projectTeamsAssertingArm and projectTeamsRetractionArm collapse this to
// TWO arms, each embedding its shared project source exactly once: 2 * 3 = 6.
// Not one -- reaching one needs the project source's OWN internal doubling
// restructured (readers.ProjectIdentityCatalogSQL's id/key union), which is
// out of scope here and filed as a follow-up, the same shape CHAOS-4552
// shipped its own 4->2 (not 4->1) reduction under and filed as CHAOS-4751.
//
// RED on origin/main at 12, GREEN here at 6 -- a real reduction, proved by a
// test that would catch a regression back toward per-arm embedding, not by
// counting arms (which stayed meaningful, just fewer of them).
func TestCHAOS4750_ProjectTeamsScansProjectsSixTimesNotTwelve(t *testing.T) {
	t.Parallel()
	statement := projectTeamsStatement(cursorState{})
	if got, want := strings.Count(statement, "FROM projects FINAL"), 6; got != want {
		t.Errorf("`projects FINAL` scanned %d times, want %d -- see this test's doc comment for the accounting\n%s", got, want, statement)
	}
}
