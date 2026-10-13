package devhealthfacts

import (
	"context"
	"fmt"
	"sort"

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
func linkedPullRequestMixStatement(bound factTimeBound, scope membershipScope, limit int) string {
	memberships := []string{fmt.Sprintf("if(%s, 0, -1)", mixWindowPredicate(0, bound))}
	return withRowLimitOf(`SELECT lr.team_id AS team_id,
	uniqExact(m.repo_uuid) AS repos,
	sumMap(mapApply((k, v) -> (k, v * m.effort), m.theme_distribution_json)) AS theme_effort,
	sum(m.bugfix_share * m.effort) AS bugfix_effort
FROM (
	SELECT repo_uuid, pr_number, c / n * effort_value AS effort, theme_distribution_json, bugfix_share
	FROM (
		SELECT win, repo_uuid, pr_number, work_unit_id, c,
			sum(c) OVER (PARTITION BY win, work_unit_id) AS n,
			effort_value, theme_distribution_json, bugfix_share
		FROM (
`+repoSplitCoreGrouped(memberships, "", "", false, scope, true)+`		)
	)
	WHERE repo_uuid != ''
) AS m
INNER JOIN (SELECT DISTINCT team_id, pr_repo_id, pr_number FROM `+teamProjectLinkRowsSQL("ids", bound)+`) AS lr
	ON m.repo_uuid = toString(lr.pr_repo_id) AND m.pr_number = toString(lr.pr_number)
WHERE (lr.team_id, m.repo_uuid) NOT IN (SELECT team_id, repo_key FROM `+ownedRepositoriesSource(` AND team_id IN {ids:Array(String)}`+ownershipValidityPredicate(bound))+`)
GROUP BY lr.team_id
ORDER BY lr.team_id`, limit)
}

// readLinkedPullRequestMix returns, per team with positive linked effort, the
// summed mix of its project-linked pull requests in repositories it does not
// own; repos counts the repositories those pull requests are in.
func (p *InvestmentProvider) readLinkedPullRequestMix(ctx context.Context, orgID string, teamIDs []string, bound factTimeBound) (map[string]*repoThemeTotals, error) {
	out := map[string]*repoThemeTotals{}
	if len(teamIDs) == 0 {
		return out, nil
	}
	sorted := append([]string(nil), teamIDs...)
	sort.Strings(sorted)
	scope, err := p.resolveMembershipScope(ctx, orgID)
	if err != nil {
		return nil, err
	}
	bindings := append(boundReaderBindings(bound), scope.bindings()...)
	statement := linkedPullRequestMixStatement(bound, scope, linkedMixTeamChunk+1)
	for start := 0; start < len(sorted); start += linkedMixTeamChunk {
		chunk := sorted[start:min(start+linkedMixTeamChunk, len(sorted))]
		got := 0
		err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadTeamLinkedPullRequestMix", statement, orgID, chunk, func(row contextpacket.ClickHouseRowScanner) error {
			var teamID string
			var repos uint64
			var theme map[string]float64
			var bugfix float64
			if err := row.Scan(&teamID, &repos, &theme, &bugfix); err != nil {
				return err
			}
			got++
			t := &repoThemeTotals{theme: map[string]float64{}, bugfix: bugfix, repos: int64(repos)}
			for _, name := range canonicalInvestmentThemes {
				t.theme[name] = theme[name]
			}
			if t.total() > 0 {
				out[teamID] = t
			}
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
	}
}
