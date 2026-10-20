package devhealthfacts

import (
	"context"
	"fmt"
	"slices"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/ext"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// teamLinkCandidatesTable carries the {ids} teams' link candidates to every
// statement that reads teamProjectLinkRowsSQL. Inlined, the candidate
// subquery is evaluated once per use, and each use reads the organization's
// linked ids and work items again; sent, it is read once per read.
const teamLinkCandidatesTable = "_team_link_candidates"

// teamLinkCandidateHashesSent is the candidate set's cityHash64 values, computed
// server-side: a fixed-width prune set for the link-row and pull request sites,
// whose real-id joins drop what a colliding hash lets through.
const teamLinkCandidateHashesSent = `(SELECT cityHash64(work_item_id) FROM ` + teamLinkCandidatesTable + `)`

// linkedWorkItemIDHashesSQL is the cityHash64 of every work item id with a link
// row in the organization: a fixed-width superset prune (a colliding hash
// admits an unlinked id, which the link-row joins drop).
const linkedWorkItemIDHashesSQL = `(SELECT cityHash64(work_item_id) FROM work_graph_issue_pr WHERE org_id = {org_id:String})`

// teamLinkCandidatesPredicate keeps every work item id whose hash is a link
// row's and whose row of any version carries a join key of a project the {ids}
// teams own under the bound: a superset of the owned projects' linked issues
// whose latest version does. cp is the unmerged work_items alias.
func teamLinkCandidatesPredicate(bound factTimeBound) string {
	return `cp.org_id = {org_id:String} AND cityHash64(cp.work_item_id) IN ` + linkedWorkItemIDHashesSQL + `
      AND cp.project_id IN ` + teamOwnedProjectKeyCandidatesSQL("ids", bound)
}

// teamLinkCandidatesStatement returns the candidates as one array row, one
// entry per matching row version (Go dedupes): no set of distinct ids is built
// on the server, and one row stays within the client's result row cap.
func teamLinkCandidatesStatement(bound factTimeBound) string {
	return `SELECT groupArray(cp.work_item_id) FROM work_items AS cp WHERE ` + teamLinkCandidatesPredicate(bound)
}

// withTeamLinkCandidates reads the teams' link candidates once and returns ctx
// carrying them as the external table teamProjectLinkRowsSQL reads. ok is
// false when the set is empty: no link row of the teams can be reached, and
// the caller skips its statement.
func withTeamLinkCandidates(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string, bound factTimeBound) (context.Context, bool, error) {
	var ids []string
	rows := 0
	err := readers.QueryOrgScopedNamed(ctx, client, "ReadTeamLinkCandidates", teamLinkCandidatesStatement(bound), orgID, teamIDs, func(row contextpacket.ClickHouseRowScanner) error {
		rows++
		return row.Scan(&ids)
	}, boundReaderBindings(bound)...)
	if err != nil {
		return nil, false, err
	}
	if rows > 1 {
		return nil, false, fmt.Errorf("team link candidates returned %d rows, want 1", rows)
	}
	if rows == 1 && len(ids) == 0 {
		return ctx, false, nil
	}
	table, err := teamLinkCandidatesExternalTable(ids)
	if err != nil {
		return nil, false, err
	}
	return clickhousedriver.Context(ctx, clickhousedriver.WithExternalTable(table)), true, nil
}

func teamLinkCandidatesExternalTable(ids []string) (*ext.Table, error) {
	table, err := ext.NewTable(teamLinkCandidatesTable, ext.Column("work_item_id", "String"))
	if err != nil {
		return nil, err
	}
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	for _, id := range ids {
		if err := table.Append(id); err != nil {
			return nil, err
		}
	}
	return table, nil
}
