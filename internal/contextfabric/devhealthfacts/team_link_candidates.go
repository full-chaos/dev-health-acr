package devhealthfacts

import (
	"context"
	"fmt"

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

// teamLinkCandidatesSent is the candidate set read back from its external table.
const teamLinkCandidatesSent = `(SELECT work_item_id FROM ` + teamLinkCandidatesTable + `)`

// teamLinkCandidatesPredicate keeps every work item id with a link row whose
// row of any version carries a join key of a project the {ids} teams own under
// the bound: a superset of the owned projects' linked issues whose latest
// version does. cp is the unmerged work_items alias.
func teamLinkCandidatesPredicate(bound factTimeBound) string {
	return `cp.org_id = {org_id:String} AND cp.work_item_id IN ` + linkedWorkItemIDsSQL + `
      AND cp.project_id IN ` + teamOwnedProjectKeyCandidatesSQL("ids", bound)
}

func teamLinkCandidatesStatement(bound factTimeBound) string {
	return `SELECT groupUniqArray(cp.work_item_id) FROM work_items AS cp WHERE ` + teamLinkCandidatesPredicate(bound)
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
	for _, id := range ids {
		if err := table.Append(id); err != nil {
			return nil, err
		}
	}
	return table, nil
}
