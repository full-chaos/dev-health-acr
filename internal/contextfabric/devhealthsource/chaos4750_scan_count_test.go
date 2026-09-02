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
// TWO arms, each embedding its shared project source exactly once. That was
// 2 * 3 = 6 when this test was written, because the project source was
// itself worth three scans.
//
// It is now 2 * 2 = 4. The follow-up this test's original comment named as
// out of scope -- restructuring the project source's OWN internal doubling,
// readers.ProjectIdentityCatalogSQL's id/key union -- landed in
// dev-health-go v0.6.3 (CHAOS-4751). That expansion now reads its row
// source ONCE and fans the two scope rows out with ARRAY JOIN, so each
// arm's project source drops from 3 scans to 2: one for the identity
// expansion (was two) plus the one the watermark join's own separate
// `FROM projects FINAL` still contributes. Two arms * 2 = 4.
//
// The upstream change was proven byte-identical against the v0.6.2
// rendering on real ClickHouse 24.8 and 26.7, so this number moves with the
// upstream shape while what the statement MEANS is unchanged. The remaining
// four are two arms * (one identity read + one watermark read); reducing
// them further is a question about the watermark join, not about identity.
//
// RED on origin/main at 12, then 6 once the arms collapsed, and 4 on the
// v0.6.3 pin -- a real reduction at each step, proved by a test that would
// catch a regression back toward per-arm embedding, not by counting arms
// (which stayed meaningful, just fewer of them).
//
// The name says Four rather than Six as of the v0.6.3 pin: a test whose
// name states a number it no longer asserts reads as coverage of something
// it does not cover.
func TestCHAOS4750_ProjectTeamsScansProjectsFourTimesNotTwelve(t *testing.T) {
	t.Parallel()
	statement := projectTeamsStatement(cursorState{})
	if got, want := strings.Count(statement, "FROM projects FINAL"), 4; got != want {
		t.Errorf("`projects FINAL` scanned %d times, want %d -- see this test's doc comment for the accounting\n%s", got, want, statement)
	}
}
