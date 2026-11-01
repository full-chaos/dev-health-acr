package devhealthfacts

import (
	"context"
	"fmt"
	"log/slog"
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

// teamLinkCandidatesSent is the candidate set read back from its external table.
const teamLinkCandidatesSent = `(SELECT work_item_id FROM ` + teamLinkCandidatesTable + `)`

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

// teamLinkCandidatesStatement returns the distinct candidates as one array
// row: one entry per issue however many unmerged versions or repositories it
// has, and one row stays within the client's result row cap. The ids are
// grouped on one thread: a DISTINCT keeps a set per stream and a final set
// (94 MB at 96k all-inside links, at 1 thread as at 4), a DISTINCT on the sort
// key opens every overlapping part at once (117 MB at 20 versions of 24k), and
// four aggregating threads each hold every id when every part holds them all;
// one grouping thread holds 48 MB and 16 MB there.
func teamLinkCandidatesStatement(bound factTimeBound) string {
	return readers.WithSettings(`SELECT groupArray(work_item_id) FROM (SELECT cp.work_item_id AS work_item_id FROM work_items AS cp WHERE `+teamLinkCandidatesPredicate(bound)+` GROUP BY cp.work_item_id)`, readers.Settings{MaxThreads: teamLinkCandidatesMaxThreads})
}

// teamLinkCandidatesMaxThreads is the candidate statement's thread count.
const teamLinkCandidatesMaxThreads = uint64(1)

// teamAmbiguousLinkHashesStatement is the hashes of the sent candidates with
// several work item rows, in any project: a link row names no issue repository,
// so nothing says which row its link means, and such an id is no candidate
// (issueLinkRowsAmongSQL states the rule). The link rows of the team reads join
// no work item, so every team read takes the rule here. The rows of an id differ
// by repo_id and the versions of one row do not, so it needs no FINAL, and it
// groups by a fixed-width hash: a state per id, and almost no id is ambiguous.
//
// These are statements of their own: the rows of an id in another project are
// outside the owned-project prune of the candidate statement, and inside that
// statement the second set of ids peaked at 105 MB at 96k issues. Their read
// follows the granules the candidates spread over, a real cost that grows with
// the organization (team_project_reach_memory_integration_test.go holds its
// absolute bound).
var teamAmbiguousLinkHashesStatement = readers.WithSettings(`SELECT groupArray(toString(h)) FROM (SELECT cityHash64(work_item_id) AS h FROM work_items WHERE org_id = {org_id:String} AND work_item_id IN `+teamLinkCandidatesSent+` GROUP BY h HAVING min(repo_id) != max(repo_id))`, readers.Settings{MaxThreads: teamLinkCandidatesMaxThreads})

// teamAmbiguousLinkCandidatesStatement is the ids behind {hashes}, the exact
// decision: two ids can share a hash. It runs only when there is a hash.
var teamAmbiguousLinkCandidatesStatement = readers.WithSettings(`SELECT groupArray(work_item_id) FROM (SELECT work_item_id FROM work_items WHERE org_id = {org_id:String} AND work_item_id IN `+teamLinkCandidatesSent+` AND toString(cityHash64(work_item_id)) IN {hashes:Array(String)} GROUP BY work_item_id HAVING min(repo_id) != max(repo_id))`, readers.Settings{MaxThreads: teamLinkCandidatesMaxThreads})

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
	sent := clickhousedriver.Context(ctx, clickhousedriver.WithExternalTable(table))
	if len(ids) == 0 {
		return sent, true, nil
	}
	ambiguous, err := readAmbiguousLinkCandidates(sent, client, orgID, teamIDs)
	if err != nil {
		return nil, false, err
	}
	if len(ambiguous) == 0 {
		return sent, true, nil
	}
	kept := slices.DeleteFunc(ids, func(id string) bool {
		_, found := slices.BinarySearch(ambiguous, id)
		return found
	})
	slog.Default().InfoContext(ctx, "context_fabric: team link candidates dropped", "ambiguous_links", len(ambiguous), "kept", len(kept))
	if len(kept) == 0 {
		return ctx, false, nil
	}
	if table, err = teamLinkCandidatesExternalTable(kept); err != nil {
		return nil, false, err
	}
	return clickhousedriver.Context(ctx, clickhousedriver.WithExternalTable(table)), true, nil
}

// readAmbiguousLinkCandidates reads the sorted ids of the sent candidates with
// several work item rows; ctx carries the candidates.
func readAmbiguousLinkCandidates(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string) ([]string, error) {
	var hashes []string
	err := readers.QueryOrgScopedNamed(ctx, client, "ReadTeamAmbiguousLinkHashes", teamAmbiguousLinkHashesStatement, orgID, teamIDs, func(row contextpacket.ClickHouseRowScanner) error {
		return row.Scan(&hashes)
	})
	if err != nil || len(hashes) == 0 {
		return nil, err
	}
	var ids []string
	err = readers.QueryOrgScopedNamed(ctx, client, "ReadTeamAmbiguousLinkCandidates", teamAmbiguousLinkCandidatesStatement, orgID, teamIDs, func(row contextpacket.ClickHouseRowScanner) error {
		return row.Scan(&ids)
	}, readers.Binding{Name: "hashes", Value: hashes})
	if err != nil {
		return nil, err
	}
	slices.Sort(ids)
	return ids, nil
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
