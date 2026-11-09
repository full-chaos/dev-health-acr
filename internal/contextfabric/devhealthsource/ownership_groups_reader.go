package devhealthsource

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// The project -> team ownership groups are stored (ops migration 147): a
// refresh view builds every group of every organization once per refresh and
// appends each group that changed to project_team_ownership_groups_log, ORDER
// BY (org_id, stored_at, row_key), with a copy in
// project_team_ownership_groups_current, ORDER BY (org_id, row_key), whose
// latest row per group is the group as it stands (is_deleted = 1 once gone).
//
// A page is a seek on the log's sort key followed by point lookups in current
// for the page's row keys, so its cost is the page, not the organization.
// Every log row the seek returns is a candidate, so the page's LIMIT and its
// truncation stay exact: a row whose group has a later stored row, or whose
// group is gone, is a progress candidate. A gone group writes nothing, as a
// group that left the per-organization statement wrote nothing.
//
// stored_at is the server time at which its refresh started; the refresh's
// rows become visible when its insert commits, which can be after a page has
// moved the cursor past their stamp. The trailing overlap re-read
// (overlap.go, defaultReprojectOverlap) reads them; it must stay above the
// slowest refresh.

// ownershipGroupsColumns are the columns a stored group is scanned from, in
// scanProjectTeamGroup's order, up to and excluding row_key and live.
const ownershipGroupsColumns = "c.project_id, c.team_id, c.source_name, c.first_valid_from, c.latest_is_open, c.latest_valid_to, c.observed_at, c.provider, c.edge_suppressed, c.conflict_identities, c.conflicting_identity_present"

// ownershipGroupsSeekSQL is the log page: the rows after the cursor in the
// log's own sort order. The bound is the cursor's microseconds as an Int64
// (sinceBoundSQL); a bound time value travels at seconds or milliseconds.
func ownershipGroupsSeekSQL(cursor cursorState) string {
	keyset := ""
	if !cursor.Since.IsZero() || cursor.After != "" {
		keyset = " AND (stored_at > " + sinceBoundSQL + " OR (stored_at = " + sinceBoundSQL + " AND row_key > {after:String}))"
	}
	return `SELECT row_key, stored_at FROM project_team_ownership_groups_log
	WHERE org_id = {org_id:String}` + keyset + `
	ORDER BY stored_at ASC, row_key ASC LIMIT {row_limit:UInt32}`
}

// ownershipGroupsPageStatement is the ownership page: the log page, each row
// LEFT JOINed to its group's current row; live when the log row is that row
// and the group is not gone. stored_at is the last column: the cursor
// position (fetchIngest).
func ownershipGroupsPageStatement(cursor cursorState) string {
	page := ownershipGroupsSeekSQL(cursor)
	return `SELECT ` + ownershipGroupsColumns + `, l.row_key, toUInt8(c.row_key != '' AND c.stored_at = l.stored_at AND c.is_deleted = 0) AS live, l.stored_at
FROM (` + page + `) AS l
LEFT JOIN (
	SELECT * FROM project_team_ownership_groups_current FINAL
	WHERE org_id = {org_id:String} AND row_key IN (SELECT row_key FROM (` + page + `))
) AS c ON c.row_key = l.row_key
ORDER BY l.stored_at ASC, l.row_key ASC`
}

// ownershipGroupsReconcileStatement is the ownership reconcile page: every
// live stored group of the organization in row-key order after {after}. A
// gone group is not re-emitted.
const ownershipGroupsReconcileStatement = `SELECT ` + ownershipGroupsColumns + `, c.row_key, toUInt8(1) AS live, c.stored_at
FROM project_team_ownership_groups_current AS c FINAL
WHERE c.org_id = {org_id:String} AND c.is_deleted = 0 AND c.row_key > {after:String}
ORDER BY c.row_key ASC LIMIT {row_limit:UInt32}`

// ownershipGroupsReconcileQuery is the ownership reconcile pass's read.
func ownershipGroupsReconcileQuery(omissions *ambiguityLedger) func(context.Context, contextpacket.ClickHouseQueryClient, string, string, int) ([]candidate, bool, error) {
	return func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID, after string, limit int) ([]candidate, bool, error) {
		return fetchIngest(ctx, client, ownershipGroupsReconcileStatement, rowLimitBindings(orgID, cursorState{After: after}, limit), limit, scanProjectTeamGroup(omissions))
	}
}
