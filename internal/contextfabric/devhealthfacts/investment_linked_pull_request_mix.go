package devhealthfacts

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/ext"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// A repository a team reaches only through its owned projects contributes to
// the team's mix through the pull requests linked to those projects' issues,
// never through its whole mix: the rest of that repository's work belongs to
// whoever owns it. Each linked pull request carries the share of a work unit
// the PR-ref partition gives it (effort / the unit's total reference count),
// so a unit is still split exactly once and the parts never exceed it.

// linkedMixTeamChunk bounds the teams one statement reads; the statement
// returns one row per team, far under the result cap.
const linkedMixTeamChunk = 100

// linkedPullRequestMixStatement sums, per team, the persisted effort of the
// pull requests linked to the issues of the team's owned projects, in
// repositories the team does not own (the owned arm counts those whole).
// The linked set and the membership scope ids reach it as external tables:
// inlined, they put the link chain in the statement at each of its four uses
// and the scope ids in its text.
func linkedPullRequestMixStatement(bound factTimeBound, scope membershipScope, limit int) string {
	memberships := []string{fmt.Sprintf("if(%s, 0, -1)", mixWindowPredicate(0, bound))}
	linked := linkedPullRequestsSent
	scope = scope.sentAsTable()
	return withRowLimitOf(`SELECT lr.team_id AS team_id,
	uniqExact(m.repo_uuid) AS repos,
	sumMap(mapApply((k, v) -> (k, v * m.effort), m.theme_distribution_json)) AS theme_effort,
	sum(m.bugfix_share * m.effort) AS bugfix_effort,
	arraySort(groupUniqArray(cityHash64(m.work_unit_id))) AS unit_hashes,
	arraySort(groupUniqArrayIf(cityHash64(m.work_unit_id), m.unit_issue_size > 0)) AS sized_unit_hashes
FROM (
	SELECT work_unit_id, repo_uuid, pr_number, c / n * effort_value AS effort, unit_issue_size, theme_distribution_json, bugfix_share
	FROM (
`+linkedPullRequestSplitRows(memberships, scope, linked)+`	)
) AS m
INNER JOIN (SELECT l.1 AS pr_repo, l.2 AS pr_num, l.3 AS team_id FROM (SELECT arrayJoin(`+linked+`) AS l)) AS lr
	ON m.repo_uuid = lr.pr_repo AND m.pr_number = lr.pr_num
WHERE (lr.team_id, m.repo_uuid) NOT IN (SELECT team_id, repo_key FROM `+ownedRepositoriesSource(` AND team_id IN {ids:Array(String)}`+ownershipValidityPredicate(bound))+`)
GROUP BY lr.team_id
ORDER BY lr.team_id`, limit)
}

// linkedPullRequestSetStatement is the distinct (repository id, PR number,
// team id) of the pull requests linked to the issues of the {ids} teams' owned
// projects, as three parallel arrays in one row.
func linkedPullRequestSetStatement(bound factTimeBound) string {
	return `SELECT groupArray(r), groupArray(n), groupArray(t) FROM (
	SELECT DISTINCT toString(pr_repo_id) AS r, toString(pr_number) AS n, team_id AS t FROM ` + teamProjectLinkRowsSQL("ids", bound) + `
)`
}

const (
	linkedPullRequestsTable = "_team_linked_pull_requests"
	membershipScopeTable    = "_membership_scope"
)

// linkedPullRequestsSent is the linked set read back from its external table,
// in the Array(Tuple(String, String, String)) shape the statement takes.
const linkedPullRequestsSent = `(SELECT groupArray((pr_repo, pr_num, team_id)) FROM ` + linkedPullRequestsTable + `)`

func linkedPullRequestsExternalTable(repos, numbers, teams []string) (*ext.Table, error) {
	table, err := ext.NewTable(linkedPullRequestsTable, ext.Column("pr_repo", "String"), ext.Column("pr_num", "String"), ext.Column("team_id", "String"))
	if err != nil {
		return nil, err
	}
	for i := range repos {
		if err := table.Append(repos[i], numbers[i], teams[i]); err != nil {
			return nil, err
		}
	}
	return table, nil
}

func membershipScopeExternalTable(ids []string) (*ext.Table, error) {
	table, err := ext.NewTable(membershipScopeTable, ext.Column("work_unit_id", "String"))
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

func (p *InvestmentProvider) readLinkedPullRequestSet(ctx context.Context, orgID string, chunk []string, bound factTimeBound) (repos, numbers, teams []string, err error) {
	rows := 0
	err = readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadTeamLinkedPullRequestSet", linkedPullRequestSetStatement(bound), orgID, chunk, func(row contextpacket.ClickHouseRowScanner) error {
		rows++
		return row.Scan(&repos, &numbers, &teams)
	}, boundReaderBindings(bound)...)
	if err != nil {
		return nil, nil, nil, err
	}
	if rows != 1 || len(numbers) != len(repos) || len(teams) != len(repos) {
		return nil, nil, nil, fmt.Errorf("team linked pull request set returned %d rows of %d/%d/%d values", rows, len(repos), len(numbers), len(teams))
	}
	return repos, numbers, teams, nil
}

// linkedMixRow is one team's row of the linked mix statement, as read.
type linkedMixRow struct {
	repos       uint64
	theme       map[string]float64
	bugfix      float64
	unitHashes  []uint64
	sizedHashes []uint64
}

// readLinkedPullRequestMix returns, per team with positive linked effort, the
// summed mix of its project-linked pull requests in repositories it does not
// own; repos counts the repositories those pull requests are in.
func (p *InvestmentProvider) readLinkedPullRequestMix(ctx context.Context, orgID string, teamIDs []string, bound factTimeBound) (map[string]*repoThemeTotals, error) {
	rows, err := p.readLinkedPullRequestMixRows(ctx, orgID, teamIDs, bound)
	if err != nil {
		return nil, err
	}
	out := map[string]*repoThemeTotals{}
	for teamID, row := range rows {
		t := &repoThemeTotals{theme: map[string]float64{}, bugfix: row.bugfix, repos: int64(row.repos), unitSet: map[uint64]struct{}{}, sizedUnitSet: map[uint64]struct{}{}}
		for _, h := range row.unitHashes {
			t.unitSet[h] = struct{}{}
		}
		for _, h := range row.sizedHashes {
			t.sizedUnitSet[h] = struct{}{}
		}
		for _, name := range canonicalInvestmentThemes {
			t.theme[name] = row.theme[name]
		}
		if t.total() > 0 {
			out[teamID] = t
		}
	}
	return out, nil
}

func (p *InvestmentProvider) readLinkedPullRequestMixRows(ctx context.Context, orgID string, teamIDs []string, bound factTimeBound) (map[string]linkedMixRow, error) {
	out := map[string]linkedMixRow{}
	if len(teamIDs) == 0 {
		return out, nil
	}
	sorted := append([]string(nil), teamIDs...)
	sort.Strings(sorted)
	scope, err := p.resolveMembershipScope(ctx, orgID)
	if err != nil {
		return nil, err
	}
	bindings := boundReaderBindings(bound)
	statement := linkedPullRequestMixStatement(bound, scope, linkedMixTeamChunk+1)
	for start := 0; start < len(sorted); start += linkedMixTeamChunk {
		chunk := sorted[start:min(start+linkedMixTeamChunk, len(sorted))]
		repos, numbers, teams, err := p.readLinkedPullRequestSet(ctx, orgID, chunk, bound)
		if err != nil {
			return nil, err
		}
		outcome := "sent"
		if len(repos) == 0 {
			outcome = "empty_set_mix_skipped"
		}
		slog.InfoContext(ctx, "devhealthfacts.team_linked_mix_set", "outcome", contextfabric.SanitizeLogAttr(outcome), "teams", len(chunk), "linked_pull_requests", len(repos), "scope_mode", contextfabric.SanitizeLogAttr(scope.modeName()))
		if len(repos) == 0 {
			continue
		}
		linkedTable, err := linkedPullRequestsExternalTable(repos, numbers, teams)
		if err != nil {
			return nil, err
		}
		tables := []*ext.Table{linkedTable}
		if scope.mode == membershipScopeIDs {
			scopeTable, err := membershipScopeExternalTable(scope.ids)
			if err != nil {
				return nil, err
			}
			tables = append(tables, scopeTable)
		}
		mixCtx := clickhousedriver.Context(ctx, clickhousedriver.WithExternalTable(tables...))
		got := 0
		err = readers.QueryOrgScopedNamed(mixCtx, p.facts.client, "ReadTeamLinkedPullRequestMix", statement, orgID, chunk, func(row contextpacket.ClickHouseRowScanner) error {
			var teamID string
			var r linkedMixRow
			if err := row.Scan(&teamID, &r.repos, &r.theme, &r.bugfix, &r.unitHashes, &r.sizedHashes); err != nil {
				return err
			}
			got++
			out[teamID] = r
			return nil
		}, bindings...)
		if err != nil {
			return nil, err
		}
		if got > linkedMixTeamChunk {
			return nil, fmt.Errorf("team linked pull request mix returned %d rows for %d teams", got, len(chunk))
		}
	}
	return out, nil
}

// addLinkedMix folds a team's linked-pull-request mix into its owned total.
// The owned repository count is untouched: linked repositories are counted
// apart (project_reached_repository_count).
func addLinkedMix(totals map[string]*repoThemeTotals, linked map[string]*repoThemeTotals) {
	for teamID, l := range linked {
		t, ok := totals[teamID]
		if !ok {
			t = &repoThemeTotals{theme: map[string]float64{}}
			totals[teamID] = t
		}
		for _, name := range canonicalInvestmentThemes {
			t.theme[name] += l.theme[name]
		}
		t.bugfix += l.bugfix
		t.unionUnits(l)
	}
}
