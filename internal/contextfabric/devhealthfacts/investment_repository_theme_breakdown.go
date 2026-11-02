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
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-go/readers"
)

// A team's investment split by repository (CHAOS-12580): the declared detail
// table repository_theme_breakdown on the team fact, one row per (repository,
// theme) with weighted effort above 0. Its rows come from the same reader rows
// as the team's theme_breakdown: each owned repository's whole mix, and each
// repository the team reaches only through its owned projects' linked pull
// requests, counted for those pull requests alone. So per theme the rows add up
// to the team's effort in that theme (theme_weighted_effort, the value
// theme_breakdown serves), up to the order of the float additions.
//
// The table is built for a single-team read only (teamRollupTablesIncluded), as
// the other per-repository tables of the team fact are: its rows grow with the
// team's repositories, and the per-repository linked mix is one more statement.

// linkedRepoMixMaxRows bounds the (team, repository) rows of one linked mix
// read. A team reaching more repositories than this through project links gets
// no table, and the read says so in its log: a cut table is not the team's
// effort.
const linkedRepoMixMaxRows = 1000

// linkedPullRequestRepoMixStatement is linkedPullRequestMixStatement split by
// repository: per (team, repository the team does not own), the persisted
// theme effort of the pull requests linked to the issues of the team's owned
// projects. The theme sums are the same PR-ref partition, grouped one level
// finer.
func linkedPullRequestRepoMixStatement(bound factTimeBound, scope membershipScope, limit int) string {
	memberships := []string{fmt.Sprintf("if(%s, 0, -1)", mixWindowPredicate(0, bound))}
	linked := linkedPullRequestsSent
	scope = scope.sentAsTable()
	return withRowLimitOf(`SELECT lr.team_id AS team_id, m.repo_uuid AS repo_uuid,
	sumMap(mapApply((k, v) -> (k, if(m.gated, 0., v * m.effort)), m.theme_distribution_json)) AS theme_effort
FROM (
	SELECT work_unit_id, repo_uuid, pr_number, c / n * effort_value AS effort, unit_issue_size, theme_distribution_json, bugfix_share, gated
	FROM (
`+linkedPullRequestSplitRows(memberships, scope, linked)+`	)
) AS m
INNER JOIN (SELECT l.1 AS pr_repo, l.2 AS pr_num, l.3 AS team_id FROM (SELECT arrayJoin(`+linked+`) AS l)) AS lr
	ON m.repo_uuid = lr.pr_repo AND m.pr_number = lr.pr_num
WHERE (lr.team_id, m.repo_uuid) NOT IN (SELECT team_id, repo_key FROM `+ownedRepositoriesSource(` AND team_id IN {ids:Array(String)}`+ownershipValidityPredicate(bound))+`)
GROUP BY lr.team_id, m.repo_uuid
ORDER BY lr.team_id, m.repo_uuid`, limit)
}

// readLinkedPullRequestRepoMix returns, for one team, the theme effort of its
// project-linked pull requests per repository it does not own. ok is false when
// the read was cut at linkedRepoMixMaxRows.
func (p *InvestmentProvider) readLinkedPullRequestRepoMix(ctx context.Context, orgID, teamID string, bound factTimeBound) (mix map[string]map[string]float64, ok bool, err error) {
	mix = map[string]map[string]float64{}
	scope, err := p.resolveMembershipScope(ctx, orgID)
	if err != nil {
		return nil, false, err
	}
	chunk := []string{teamID}
	repos, numbers, teams, err := p.readLinkedPullRequestSet(ctx, orgID, chunk, bound)
	if err != nil {
		return nil, false, err
	}
	if len(repos) == 0 {
		return mix, true, nil
	}
	linkedTable, err := linkedPullRequestsExternalTable(repos, numbers, teams)
	if err != nil {
		return nil, false, err
	}
	tables := []*ext.Table{linkedTable}
	if scope.mode == membershipScopeIDs {
		scopeTable, scopeErr := membershipScopeExternalTable(scope.ids)
		if scopeErr != nil {
			return nil, false, scopeErr
		}
		tables = append(tables, scopeTable)
	}
	mixCtx := clickhousedriver.Context(ctx, clickhousedriver.WithExternalTable(tables...))
	got := 0
	err = readers.QueryOrgScopedNamed(mixCtx, p.facts.client, "ReadTeamLinkedPullRequestRepoMix", linkedPullRequestRepoMixStatement(bound, scope, linkedRepoMixMaxRows+1), orgID, chunk, func(row contextpacket.ClickHouseRowScanner) error {
		var team, repo string
		var theme map[string]float64
		if scanErr := row.Scan(&team, &repo, &theme); scanErr != nil {
			return scanErr
		}
		got++
		if team == teamID {
			mix[repo] = theme
		}
		return nil
	}, boundReaderBindings(bound)...)
	if err != nil {
		return nil, false, err
	}
	if got > linkedRepoMixMaxRows {
		return nil, false, nil
	}
	return mix, true, nil
}

// repositoryThemeRow is one (repository, theme) link of a team's effort.
type repositoryThemeRow struct {
	repoID string
	word   string
	theme  string
	effort float64
}

// repositoryThemeBreakdownTable renders a team's links as the declared detail
// table: repositories in id order, themes in canonical order, a row only where
// the link carries effort above 0. themeEffort is the team's own effort per
// theme, the value theme_breakdown serves. ok is false when no link carries
// effort.
func repositoryThemeBreakdownTable(links []repositoryThemeRow, themeEffort map[string]float64, grain contextfabric.TemporalGrain) (contextfabric.FactValue, bool) {
	order := map[string]int{}
	for index, theme := range canonicalInvestmentThemes {
		order[theme] = index
	}
	sort.SliceStable(links, func(i, j int) bool {
		if links[i].repoID != links[j].repoID {
			return links[i].repoID < links[j].repoID
		}
		return order[links[i].theme] < order[links[j].theme]
	})
	rows := make([]contextfabric.FactValueRow, 0, len(links))
	for _, link := range links {
		if !(link.effort > 0) {
			continue
		}
		rows = append(rows, contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
			contractsv1.ContextFabricRenderRepositoryField:     contextfabric.StringFactValue(repositoryPrefix + link.repoID),
			contractsv1.ContextFabricRenderRepositoryWordField: contextfabric.StringFactValue(link.word),
			contractsv1.ContextFabricRenderThemeField:          contextfabric.StringFactValue(link.theme),
			contractsv1.ContextFabricRenderEffortField:         contextfabric.NumberFactValue(link.effort),
			contractsv1.ContextFabricRenderThemeEffortField:    contextfabric.NumberFactValue(themeEffort[link.theme]),
		}})
	}
	if len(rows) == 0 {
		return contextfabric.FactValue{}, false
	}
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:        contextfabric.FactTableBreakdown,
		Key:          []string{contractsv1.ContextFabricRenderRepositoryField, contractsv1.ContextFabricRenderThemeField},
		Measures:     []string{contractsv1.ContextFabricRenderEffortField, contractsv1.ContextFabricRenderThemeEffortField},
		Observations: []string{contractsv1.ContextFabricRenderRepositoryWordField},
		Grain:        grain,
		Detail:       true,
		Rows:         rows,
	}), true
}

// teamRepositoryThemeLinks gathers one team's links: each owned repository's
// whole mix and each project-reached repository's linked mix, each labelled
// with the repository's name. A repository with no name gives no table: its
// word would be an id. The reason is logged with the team and the repository,
// and the answer keeps theme_breakdown.
func teamRepositoryThemeLinks(ctx context.Context, teamID string, owned []string, ownedMix map[string]*repoThemeTotals, linked map[string]map[string]float64, names map[string]string) ([]repositoryThemeRow, bool) {
	var links []repositoryThemeRow
	wordOwner := map[string]string{}
	add := func(repoID string, themes map[string]float64) bool {
		// A repository with no effort in any theme draws no row, so its name
		// and its word decide nothing (Codex r3 on #1962).
		positive := false
		for _, theme := range canonicalInvestmentThemes {
			positive = positive || themes[theme] > 0
		}
		if !positive {
			return true
		}
		word := names[repoID]
		// Two repositories under one handle (repos is keyed by id) could not be
		// told apart by a reader, and a sankey refuses two nodes with one word.
		if owner, shared := wordOwner[word]; shared && owner != repoID && word != "" {
			slog.InfoContext(ctx, "devhealthfacts.repository_theme_breakdown_withheld", append([]any{
				"reason", "repository_name_shared", "team", contextfabric.SanitizeLogAttr(teamID), "repository", contextfabric.SanitizeLogAttr(repoID),
			}, requestIDLogAttrs(ctx)...)...)
			return false
		}
		wordOwner[word] = repoID
		if word == "" {
			slog.InfoContext(ctx, "devhealthfacts.repository_theme_breakdown_withheld", append([]any{
				"reason", "repository_without_name", "team", contextfabric.SanitizeLogAttr(teamID), "repository", contextfabric.SanitizeLogAttr(repoID),
			}, requestIDLogAttrs(ctx)...)...)
			return false
		}
		for _, theme := range canonicalInvestmentThemes {
			if themes[theme] > 0 {
				links = append(links, repositoryThemeRow{repoID: repoID, word: word, theme: theme, effort: themes[theme]})
			}
		}
		return true
	}
	seen := map[string]bool{}
	for _, repoID := range owned {
		mix, ok := ownedMix[repoID]
		if seen[repoID] || !ok {
			continue
		}
		seen[repoID] = true
		if mix.total() > 0 && !add(repoID, mix.theme) {
			return nil, false
		}
	}
	for repoID, themes := range linked {
		if seen[repoID] {
			continue
		}
		seen[repoID] = true
		if !add(repoID, themes) {
			return nil, false
		}
	}
	return links, true
}

// repositoryNamesStatement reads each repository's handle (repos.repo, the word
// the subject reads serve) for the ids of one team's table.
const repositoryNamesStatement = `SELECT toString(id) AS repo_key, any(repo) AS name
FROM repos FINAL
WHERE org_id = {org_id:String} AND toString(id) IN {ids:Array(String)}
GROUP BY repo_key
ORDER BY repo_key`

// readRepositoryNames returns the handle of each repository id it finds. An id
// it does not find has no word, and the caller withholds the table.
func (p *InvestmentProvider) readRepositoryNames(ctx context.Context, orgID string, ids []string) (map[string]string, error) {
	names := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return names, nil
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadRepositoryThemeBreakdownNames", withRowLimitOf(repositoryNamesStatement, len(sorted)+1), orgID, sorted, func(row contextpacket.ClickHouseRowScanner) error {
		var key, name string
		if err := row.Scan(&key, &name); err != nil {
			return err
		}
		names[key] = name
		return nil
	})
	if err != nil {
		return nil, err
	}
	return names, nil
}

// teamRepositoryThemeBreakdown builds one team's repository_theme_breakdown:
// the owned repositories' effort from their whole mix, the reached
// repositories' from the per-repository linked mix, and every repository's
// word from repos. ok is false when the table is withheld (no link with effort,
// a repository with no name, or a cut linked read); the answer then keeps
// theme_breakdown alone.
func (p *InvestmentProvider) teamRepositoryThemeBreakdown(ctx context.Context, orgID, teamID string, owned []string, ownedMix map[string]*repoThemeTotals, reachesProjects bool, themeEffort map[string]float64, bound factTimeBound) (contextfabric.FactValue, bool, error) {
	linked := map[string]map[string]float64{}
	if reachesProjects {
		mix, whole, err := p.readLinkedPullRequestRepoMix(ctx, orgID, teamID, bound)
		if err != nil {
			return contextfabric.FactValue{}, false, err
		}
		if !whole {
			slog.InfoContext(ctx, "devhealthfacts.repository_theme_breakdown_withheld", append([]any{
				"reason", "linked_repositories_over_bound", "team", contextfabric.SanitizeLogAttr(teamID), "bound", linkedRepoMixMaxRows,
			}, requestIDLogAttrs(ctx)...)...)
			return contextfabric.FactValue{}, false, nil
		}
		linked = mix
	}
	ids := append([]string(nil), owned...)
	for repoID := range linked {
		ids = append(ids, repoID)
	}
	names, err := p.readRepositoryNames(ctx, orgID, ids)
	if err != nil {
		return contextfabric.FactValue{}, false, err
	}
	links, ok := teamRepositoryThemeLinks(ctx, teamID, owned, ownedMix, linked, names)
	if !ok {
		return contextfabric.FactValue{}, false, nil
	}
	table, ok := repositoryThemeBreakdownTable(links, themeEffort, bound.effectiveGrain(grainDaily))
	// Past the table's row bound (64 repositories × 5 themes) the table is
	// withheld, never cut: a cut table is not the team's effort, and a table
	// past its bound would make the whole fact invalid (Codex r1 on #1962).
	if ok && len(table.Rows) > contractsv1.ContextFabricClaimedFactRepositoryFlowMaxRows {
		slog.InfoContext(ctx, "devhealthfacts.repository_theme_breakdown_withheld", append([]any{
			"reason", "rows_over_bound", "team", contextfabric.SanitizeLogAttr(teamID), "bound", contractsv1.ContextFabricClaimedFactRepositoryFlowMaxRows,
		}, requestIDLogAttrs(ctx)...)...)
		return contextfabric.FactValue{}, false, nil
	}
	return table, ok, nil
}
