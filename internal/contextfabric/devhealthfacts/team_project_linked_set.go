package devhealthfacts

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/ext"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// teamLinkCandidateChunk is the candidates one link set statement reads (58.1 MB peak at 24k, CI 96k all-inside shape).
const teamLinkCandidateChunk = 24_000

// readTeamProjectLinkedSet reads the distinct link rows of the {ids} teams'
// owned projects (linkedPullRequestSetStatement) in candidate chunks of at
// most teamLinkCandidateChunk ids: each statement holds its chunk's links
// only, however many links the teams reach. A candidate is in one chunk, so
// each link row is read by one statement; the union is de-duplicated on the
// whole row, the key of the statement's own DISTINCT, so it is the set one
// statement over every candidate returns. Rows keep their first-read order.
func readTeamProjectLinkedSet(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string, bound factTimeBound) ([]linkedSetRow, error) {
	ids, ok, err := readTeamLinkCandidateIDs(ctx, client, orgID, teamIDs, bound)
	if err != nil || !ok {
		return nil, err
	}
	statement := linkedPullRequestSetStatement(bound)
	seen := map[linkedSetRow]struct{}{}
	var out []linkedSetRow
	for start := 0; start == 0 || start < len(ids); start += teamLinkCandidateChunk {
		table, err := teamLinkCandidatesExternalTable(ids[start:min(start+teamLinkCandidateChunk, len(ids))])
		if err != nil {
			return nil, err
		}
		var repos, numbers, teams, issues []string
		var ranks []uint8
		rows := 0
		err = readers.QueryOrgScopedNamed(clickhousedriver.Context(ctx, clickhousedriver.WithExternalTable(table)), client, "ReadTeamLinkedPullRequestSet", statement, orgID, teamIDs, func(row contextpacket.ClickHouseRowScanner) error {
			rows++
			return row.Scan(&repos, &numbers, &teams, &issues, &ranks)
		}, boundReaderBindings(bound)...)
		if err != nil {
			return nil, err
		}
		if rows == 0 {
			continue
		}
		if rows != 1 || len(numbers) != len(repos) || len(teams) != len(repos) || len(issues) != len(repos) || len(ranks) != len(repos) {
			return nil, fmt.Errorf("team linked pull request set returned %d rows of %d/%d/%d/%d/%d values", rows, len(repos), len(numbers), len(teams), len(issues), len(ranks))
		}
		for i := range repos {
			row := linkedSetRow{repo: repos[i], number: numbers[i], team: teams[i], issue: issues[i], rank: ranks[i]}
			if _, dup := seen[row]; dup {
				continue
			}
			seen[row] = struct{}{}
			out = append(out, row)
		}
	}
	return out, nil
}

// teamLinkedPullRequestKey is one (team, linked pull request) of a team
// project link set.
type teamLinkedPullRequestKey struct{ team, repo, number string }

// teamLinkedPullRequests is a team project link set as one row per (team,
// linked pull request) with the weakest tier rank among its link rows, the
// rows the team pull request reads count. Sorted by team, repository and
// number.
type teamLinkedPullRequests struct {
	keys  []teamLinkedPullRequestKey
	ranks []uint8
}

// The set the team pull request reads join is sent as one external table, never
// as a binding: the statement text stays the same size however large the set
// is. A set over either bound is a loud named failure, as a rollup over part of
// the set would be a wrong total.
const (
	teamLinkedPullRequestsTable    = "_team_project_linked_pull_requests"
	teamLinkedPullRequestsMaxRows  = 400_000
	teamLinkedPullRequestsMaxBytes = 64 << 20
)

// teamLinkedPullRequestsSent is the sent set with the column types of the link
// rows it came from (link_rank as the read's UInt8 tier rank).
const teamLinkedPullRequestsSent = `(SELECT team_id, toUUID(pr_repo_id) AS pr_repo_id, toUInt32(pr_number) AS pr_number, link_rank FROM ` + teamLinkedPullRequestsTable + `)`

// linkedPullRequestsOf folds a link set to one row per (team, pull request)
// with its smallest rank.
func linkedPullRequestsOf(rows []linkedSetRow) teamLinkedPullRequests {
	rank := map[teamLinkedPullRequestKey]uint8{}
	for _, row := range rows {
		key := teamLinkedPullRequestKey{team: row.team, repo: row.repo, number: row.number}
		if held, ok := rank[key]; !ok || row.rank < held {
			rank[key] = row.rank
		}
	}
	out := teamLinkedPullRequests{keys: make([]teamLinkedPullRequestKey, 0, len(rank))}
	for key := range rank {
		out.keys = append(out.keys, key)
	}
	slices.SortFunc(out.keys, func(a, b teamLinkedPullRequestKey) int {
		return cmp.Or(cmp.Compare(a.team, b.team), cmp.Compare(a.repo, b.repo), cmp.Compare(a.number, b.number))
	})
	out.ranks = make([]uint8, len(out.keys))
	for i, key := range out.keys {
		out.ranks[i] = rank[key]
	}
	return out
}

// readTeamLinkedPullRequests is readTeamProjectLinkedSet folded by
// linkedPullRequestsOf.
func readTeamLinkedPullRequests(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string, bound factTimeBound) (teamLinkedPullRequests, error) {
	rows, err := readTeamProjectLinkedSet(ctx, client, orgID, teamIDs, bound)
	if err != nil {
		return teamLinkedPullRequests{}, err
	}
	return linkedPullRequestsOf(rows), nil
}

// withTeamLinkedPullRequests returns ctx carrying the set as the external table
// teamLinkedPullRequestsSent reads.
func withTeamLinkedPullRequests(ctx context.Context, set teamLinkedPullRequests) (context.Context, error) {
	if len(set.keys) > teamLinkedPullRequestsMaxRows {
		return nil, fmt.Errorf("devhealthfacts: the team linked pull request set holds %d rows, over the %d rows one sent table carries", len(set.keys), teamLinkedPullRequestsMaxRows)
	}
	table, err := ext.NewTable(teamLinkedPullRequestsTable, ext.Column("team_id", "String"), ext.Column("pr_repo_id", "String"), ext.Column("pr_number", "String"), ext.Column("link_rank", "UInt8"))
	if err != nil {
		return nil, err
	}
	size := 0
	for i, key := range set.keys {
		size += len(key.team) + len(key.repo) + len(key.number) + 1
		if size > teamLinkedPullRequestsMaxBytes {
			return nil, fmt.Errorf("devhealthfacts: the team linked pull request set holds over %d bytes, the most one sent table carries", teamLinkedPullRequestsMaxBytes)
		}
		if err := table.Append(key.team, key.repo, key.number, set.ranks[i]); err != nil {
			return nil, err
		}
	}
	return clickhousedriver.Context(ctx, clickhousedriver.WithExternalTable(table)), nil
}
