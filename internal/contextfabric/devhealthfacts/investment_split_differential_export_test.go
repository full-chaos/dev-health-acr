package devhealthfacts

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// legacySplitCore is the split as it stood before the latest-row tuple and
// the requested-repository unit filter: the differential oracle's old side.
func legacySplitCore(memberships []string, unitCols, unitAgg string, spanSentinel bool, scope membershipScope, perRef bool) string {
	refCol, refGroup := "", ""
	if perRef {
		refCol = "\n\t\t\t\tparsed.pr_number AS pr_number,"
		refGroup = ", pr_number"
	}
	wins := "arrayFilter(w -> w >= 0, [" + strings.Join(memberships, ", ") + "])"
	if spanSentinel {
		wins = "arrayConcat([" + spanSentinelWindow + "], " + wins + ")"
	}
	return `			SELECT *, unit_size_weight AS effort_value
			FROM ` + sizedUnitsForOrg(`SELECT parsed.win AS win, parsed.work_unit_id AS work_unit_id,
				if(parsed.uuid_direct != '', parsed.uuid_direct, ifNull(rl.repo_uuid, '')) AS repo_uuid,`+refCol+`
				uniqExact(if(repo_uuid = '', parsed.ref_text, concat(repo_uuid, '#', parsed.pr_number))) AS c,
				any(parsed.theme_distribution_json) AS theme_distribution_json,
				any(parsed.bugfix_share) AS bugfix_share,
				any(parsed.span_from) AS span_from`+unitAgg+`
			FROM (
				SELECT work_unit_id, theme_distribution_json, bugfix_share, span_from, win,`+unitCols+`
					ref.1 AS uuid_direct, ref.2 AS lookup_provider, ref.3 AS lookup_repo, ref.4 AS pr_number, ref.5 AS ref_text
				FROM (
					SELECT work_unit_id, repo_id, theme_distribution_json, from_ts AS span_from,`+unitCols+`
						ifNull(subcategory_distribution_json['`+readers.BugfixSubcategoryKey+`'], 0.0) AS bugfix_share,
						`+wins+` AS wins,
						arrayConcat(
							arrayMap(r -> (splitByString('#pr', r)[1], '', '', splitByString('#pr', r)[2], r),
								arrayFilter(r -> match(r, '^[0-9a-fA-F-]{36}#pr[0-9]+$'), JSONExtract(structural_evidence_json, 'prs', 'Array(String)'))),
							arrayMap(r -> ('', 'github', substring(splitByChar('#', r)[1], 6), splitByChar('#', r)[2], r),
								arrayFilter(r -> match(r, '^ghpr:[^#]+#[0-9]+$'), JSONExtract(structural_evidence_json, 'issues', 'Array(String)'))),
							arrayMap(r -> ('', 'gitlab', substring(splitByChar('!', r)[1], 8), splitByChar('!', r)[2], r),
								arrayFilter(r -> match(r, '^gitlab:[^!]+![0-9]+$'), JSONExtract(structural_evidence_json, 'issues', 'Array(String)')))
						) AS pr_refs,
						`+repoRefsExpression+` AS refs
					FROM (
						SELECT work_unit_id,
							(argMax(tuple(repo_id), computed_at)).1 AS repo_id,
							argMax(from_ts, computed_at) AS from_ts,
							argMax(to_ts, computed_at) AS to_ts,
							argMax(theme_distribution_json, computed_at) AS theme_distribution_json,
							argMax(subcategory_distribution_json, computed_at) AS subcategory_distribution_json,
							argMax(structural_evidence_json, computed_at) AS structural_evidence_json
						FROM work_unit_investments
						WHERE org_id = {org_id:String}`+supersededWorkUnitIDsFilter()+scope.filter()+`
						GROUP BY work_unit_id
					)
				)
				ARRAY JOIN wins AS win
				ARRAY JOIN refs AS ref
			) AS parsed
			LEFT JOIN (
				SELECT toString(id) AS repo_uuid, argMax(repo, last_synced) AS repo,
					if(uniqExact(provider) = 1, argMax(provider, last_synced), '') AS provider
				FROM repos
				WHERE org_id = {org_id:String}
				GROUP BY id
			) AS rl ON rl.provider = parsed.lookup_provider AND rl.repo = parsed.lookup_repo
			GROUP BY win, work_unit_id, repo_uuid`+refGroup) + `
`
}

// legacyRepoMixStatement is the repository mix as it stood before the
// requested split, over the frozen split.
func legacyRepoMixStatement(bounds []factTimeBound, scope membershipScope, limit int) string {
	memberships := make([]string, 0, len(bounds))
	for i, b := range bounds {
		memberships = append(memberships, fmt.Sprintf("if(%s, %d, -1)", mixWindowPredicate(i, b), i))
	}
	return withRowLimitOf(`SELECT toUInt8(if(win < 0, 255, win)) AS window, repo_uuid AS repo_id,
	sumMap(mapApply((k, v) -> (k, v * effort), theme_distribution_json)) AS theme_effort,
	sum(bugfix_share * effort) AS bugfix_effort,
	uniqExact(work_unit_id) AS work_units,
	arraySort(groupUniqArrayIf(cityHash64(work_unit_id), win >= 0)) AS unit_hashes,
	arraySort(groupUniqArrayIf(cityHash64(work_unit_id), win >= 0 AND unit_issue_size > 0)) AS sized_unit_hashes,
	min(span_from) AS span_from
FROM (
	SELECT win, repo_uuid, work_unit_id, c / n * effort_value AS effort, unit_issue_size, theme_distribution_json, bugfix_share, span_from
	FROM (
		SELECT win, repo_uuid, work_unit_id, c,
			sum(c) OVER (PARTITION BY win, work_unit_id) AS n,
			effort_value, unit_issue_size, theme_distribution_json, bugfix_share, span_from
		FROM (
`+legacySplitCore(memberships, "", "", true, scope, false)+`		)
	)
	WHERE repo_uuid != '' AND repo_uuid IN {ids:Array(String)}
)
GROUP BY win, repo_uuid
ORDER BY win, repo_uuid`, limit)
}

// legacyRepositoryUnitsStatement is the repository unit listing (first page)
// as it stood before the requested split, over the frozen split.
func legacyRepositoryUnitsStatement(b factTimeBound, scope membershipScope) string {
	memberships := []string{fmt.Sprintf("if(%s, %d, -1)", mixWindowPredicate(0, b), 0)}
	core := legacySplitCore(memberships, " from_ts, to_ts,", unitsCore, false, scope, false)
	return `SELECT work_unit_id, repo_uuid, share, effort_value, unit_issue_size, theme_distribution_json, from_ts, to_ts, prs, unresolved_n, unresolved_refs, scope_total, scope_units, scope_unattributed_total, scope_unattributed_rows, scope_distinct_units, scope_sized_units
FROM (
	SELECT work_unit_id, repo_uuid, c / n * effort_value AS share, effort_value, unit_issue_size, theme_distribution_json, from_ts, to_ts, prs, unresolved_n, unresolved_refs,
		sum(c / n * effort_value) OVER () AS scope_total, count() OVER () AS scope_units,
		uniqExact(work_unit_id) OVER () AS scope_distinct_units, uniqExactIf(work_unit_id, unit_issue_size > 0) OVER () AS scope_sized_units,
		sumIf(c / n * effort_value, repo_uuid = '') OVER () AS scope_unattributed_total, countIf(repo_uuid = '') OVER () AS scope_unattributed_rows
	FROM (
		SELECT win, repo_uuid, work_unit_id, c,
			sum(c) OVER (PARTITION BY win, work_unit_id) AS n,
			sum(if(repo_uuid = '', c, 0)) OVER (PARTITION BY win, work_unit_id) AS unresolved_n,
			arrayFlatten(groupArray(if(repo_uuid = '', ref_texts, [])) OVER (PARTITION BY win, work_unit_id)) AS unresolved_refs,
			effort_value, unit_issue_size, theme_distribution_json, from_ts, to_ts, prs
		FROM (
` + core + `		)
	)
	WHERE repo_uuid != '' AND repo_uuid IN {ids:Array(String)}
)
ORDER BY share DESC, work_unit_id ASC, repo_uuid ASC
LIMIT {` + unitLimitParam + `:UInt32}`
}

// legacyLinkedPullRequestMixStatement is the team linked pull request mix as
// it stood before the folded split, over the frozen split.
func legacyLinkedPullRequestMixStatement(bound factTimeBound, scope membershipScope, limit int) string {
	memberships := []string{fmt.Sprintf("if(%s, 0, -1)", mixWindowPredicate(0, bound))}
	return withRowLimitOf(`SELECT lr.team_id AS team_id,
	uniqExact(m.repo_uuid) AS repos,
	sumMap(mapApply((k, v) -> (k, v * m.effort), m.theme_distribution_json)) AS theme_effort,
	sum(m.bugfix_share * m.effort) AS bugfix_effort,
	arraySort(groupUniqArray(cityHash64(m.work_unit_id))) AS unit_hashes,
	arraySort(groupUniqArrayIf(cityHash64(m.work_unit_id), m.unit_issue_size > 0)) AS sized_unit_hashes
FROM (
	SELECT work_unit_id, repo_uuid, pr_number, c / n * effort_value AS effort, unit_issue_size, theme_distribution_json, bugfix_share
	FROM (
		SELECT win, repo_uuid, pr_number, work_unit_id, c,
			sum(c) OVER (PARTITION BY win, work_unit_id) AS n,
			effort_value, unit_issue_size, theme_distribution_json, bugfix_share
		FROM (
`+legacySplitCore(memberships, "", "", false, scope, true)+`		)
	)
	WHERE repo_uuid != ''
) AS m
INNER JOIN (SELECT DISTINCT team_id, pr_repo_id, pr_number FROM `+inlineTeamLinkCandidates(teamProjectLinkRowsSQL("ids", bound), bound)+`) AS lr
	ON m.repo_uuid = toString(lr.pr_repo_id) AND m.pr_number = toString(lr.pr_number)
WHERE (lr.team_id, m.repo_uuid) NOT IN (SELECT team_id, repo_key FROM `+ownedRepositoriesSource(` AND team_id IN {ids:Array(String)}`+ownershipValidityPredicate(bound))+`)
GROUP BY lr.team_id
ORDER BY lr.team_id`, limit)
}

// SplitStatementPair is one production statement built on a split, as it is
// now (New) and as it was (Old), with the bindings both take besides org_id
// and ids. Old equals New for a statement whose text did not change.
type SplitStatementPair struct {
	Name     string
	Old, New string
	Bindings []readers.Binding
}

// InvestmentSplitStatementPairs returns every statement built on a split for
// the range [start, end) (and its prior window of equal length for the
// repository mix), under the scope subqueries.
func InvestmentSplitStatementPairs(start, end time.Time, unitLimit int) []SplitStatementPair {
	current := factTimeBound{active: true, hasStart: true, start: start, end: end}
	prior := factTimeBound{active: true, hasStart: true, start: start.Add(-end.Sub(start)), end: start}
	scope := subqueryMembershipScope
	var mixBindings []readers.Binding
	for i, b := range []factTimeBound{current, prior} {
		startParam, endParam := mixWindowParams(i)
		mixBindings = append(mixBindings, readers.Binding{Name: startParam, Value: b.start}, readers.Binding{Name: endParam, Value: b.end})
	}
	unitBindings := append(boundReaderBindings(current), readers.Binding{Name: unitLimitParam, Value: strconv.Itoa(unitLimit)})
	orgUnits := investmentUnitsStatementFor(current, false, scope, true)
	orgMix := orgMixStatement(current)
	linked := inlineSentLinkedMix(linkedPullRequestMixStatement(current, scope, 1000), current)
	return []SplitStatementPair{
		{Name: "repository mix", Bindings: mixBindings,
			New: repoMixStatementScoped([]factTimeBound{current, prior}, scope, 100000),
			Old: legacyRepoMixStatement([]factTimeBound{current, prior}, scope, 100000)},
		{Name: "repository unit listing", Bindings: unitBindings,
			New: investmentUnitsStatementFor(current, false, scope, false),
			Old: legacyRepositoryUnitsStatement(current, scope)},
		{Name: "organization unit listing", Bindings: unitBindings, New: orgUnits, Old: orgUnits},
		{Name: "organization mix", Bindings: boundReaderBindings(current), New: orgMix, Old: orgMix},
		{Name: "team linked pull request mix", Bindings: boundReaderBindings(current), New: linked,
			Old: legacyLinkedPullRequestMixStatement(current, scope, 1000)},
	}
}

// TeamLinkedMixSentForTest is the team linked pull request mix for the range
// [start, end) under the scope subqueries, in the shape the provider sends it:
// the linked set read first and sent as the external table the statement
// reads, so the statement holds no link rows. The pair's New is its inlined
// export form, for the differential oracle only.
func TeamLinkedMixSentForTest(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teams []string, start, end time.Time) (context.Context, string, []readers.Binding, error) {
	current := factTimeBound{active: true, hasStart: true, start: start, end: end}
	repos, numbers, teamIDs, err := newInvestmentProvider(client).readLinkedPullRequestSet(ctx, orgID, teams, current)
	if err != nil {
		return nil, "", nil, err
	}
	if len(repos) == 0 {
		return nil, "", nil, fmt.Errorf("the team linked pull request set of %v is empty: the provider would skip the mix", teams)
	}
	table, err := linkedPullRequestsExternalTable(repos, numbers, teamIDs)
	if err != nil {
		return nil, "", nil, err
	}
	sent := clickhousedriver.Context(ctx, clickhousedriver.WithExternalTable(table))
	return sent, linkedPullRequestMixStatement(current, subqueryMembershipScope, 1000), boundReaderBindings(current), nil
}

// ReachDefects are, per statement on the folded split, its reach with one
// clause removed; the differential must tell each apart from the old
// statement.
func ReachDefects(statement string) map[string][2]string {
	if statement == "team linked pull request mix" {
		return map[string][2]string{
			"direct references dropped": {"(hasAny(direct_refs,", "(0 AND hasAny(direct_refs,"},
			"lookup references dropped": {"OR hasAny(lookup_refs,", "OR 0 AND hasAny(lookup_refs,"},
		}
	}
	return map[string][2]string{
		"direct references dropped":       {"(hasAny(arrayMap(x -> x.1, direct_refs),", "(0 AND hasAny(arrayMap(x -> x.1, direct_refs),"},
		"lookup references dropped":       {"OR hasAny(arrayMap(x -> (x.1, x.2), lookup_refs),", "OR 0 AND hasAny(arrayMap(x -> (x.1, x.2), lookup_refs),"},
		"own repository fallback dropped": {"OR (empty(direct_refs)", "OR 0 AND (empty(direct_refs)"},
	}
}

const unitsCore = ",\n\t\t\t\tany(parsed.from_ts) AS from_ts, any(parsed.to_ts) AS to_ts,\n\t\t\t\tgroupUniqArray(parsed.pr_number) AS prs,\n\t\t\t\tgroupUniqArray(parsed.ref_text) AS ref_texts"

// legacyOrganizationMixStatement is the organization mix as main builds it,
// over the frozen split, with its named limit.
func legacyOrganizationMixStatement(bound factTimeBound) string {
	memberships := []string{fmt.Sprintf("if(%s, 0, -1)", mixWindowPredicate(0, bound))}
	statement := `SELECT
	sumMap(mapApply((k, v) -> (k, if(win >= 0 AND repo_uuid != '', v * effort, 0.)), theme_distribution_json)) AS theme_effort,
	sumIf(bugfix_share * effort, win >= 0 AND repo_uuid != '') AS bugfix_effort,
	uniqExactIf(work_unit_id, win >= 0 AND repo_uuid != '') AS work_units,
	uniqExactIf(work_unit_id, win >= 0 AND repo_uuid != '' AND unit_issue_size > 0) AS sized_units,
	uniqExactIf(repo_uuid, win >= 0 AND repo_uuid != '') AS repositories,
	sumIf(effort, win >= 0 AND repo_uuid != '') AS resolved_effort,
	sumIf(effort, win >= 0 AND repo_uuid = '') AS unattributed_effort,
	min(span_from) AS span_from
FROM (
	SELECT win, repo_uuid, work_unit_id, c / n * effort_value AS effort, unit_issue_size, theme_distribution_json, bugfix_share, span_from
	FROM (
		SELECT win, repo_uuid, work_unit_id, c,
			sum(c) OVER (PARTITION BY win, work_unit_id) AS n,
			effort_value, unit_issue_size, theme_distribution_json, bugfix_share, span_from
		FROM (
` + orgRefs(legacySplitCore(memberships, "", "", true, subqueryMembershipScope, false)) + `		)
	)
)`
	return readers.WithSettings(withRowLimit(statement), readers.Settings{MaxMemoryUsage: 512 << 20})
}

// legacyOrganizationUnitsStatement is the organization unit listing as main
// builds it, over the frozen split, with its named limit.
func legacyOrganizationUnitsStatement(b factTimeBound, withCursor bool, scope membershipScope) string {
	memberships := []string{fmt.Sprintf("if(%s, %d, -1)", mixWindowPredicate(0, b), 0)}
	core := orgRefs(legacySplitCore(memberships, " from_ts, to_ts,", unitsCore, false, scope, false))
	keyset := ""
	if withCursor {
		keyset = `
WHERE share < toFloat64({` + unitCursorShareParam + `:String})
	OR (share = toFloat64({` + unitCursorShareParam + `:String}) AND (work_unit_id > {` + unitCursorIDParam + `:String}
		OR (work_unit_id = {` + unitCursorIDParam + `:String} AND repo_uuid > {` + unitCursorRepoParam + `:String})))`
	}
	realRefs := "arrayFilter(x -> x != concat('unit:', work_unit_id), ref_texts)"
	statement := `SELECT work_unit_id, repo_uuid, share, effort_value, unit_issue_size, theme_distribution_json, from_ts, to_ts, prs, unresolved_n, unresolved_refs, scope_total, scope_units, scope_unattributed_total, scope_unattributed_rows, scope_distinct_units, scope_sized_units
FROM (
	SELECT work_unit_id, repo_uuid, c / n * effort_value AS share, effort_value, unit_issue_size, theme_distribution_json, from_ts, to_ts, prs, unresolved_n, unresolved_refs,
		sum(c / n * effort_value) OVER () AS scope_total, count() OVER () AS scope_units,
		uniqExact(work_unit_id) OVER () AS scope_distinct_units, uniqExactIf(work_unit_id, unit_issue_size > 0) OVER () AS scope_sized_units,
		sumIf(c / n * effort_value, repo_uuid = '') OVER () AS scope_unattributed_total, countIf(repo_uuid = '') OVER () AS scope_unattributed_rows
	FROM (
		SELECT win, repo_uuid, work_unit_id, c,
			sum(c) OVER (PARTITION BY win, work_unit_id) AS n,
			sum(if(repo_uuid = '', length(` + realRefs + `), 0)) OVER (PARTITION BY win, work_unit_id) AS unresolved_n,
			arrayFlatten(groupArray(if(repo_uuid = '', ` + realRefs + `, [])) OVER (PARTITION BY win, work_unit_id)) AS unresolved_refs,
			effort_value, unit_issue_size, theme_distribution_json, from_ts, to_ts, prs
		FROM (
` + core + `		)
	)
	WHERE win >= 0
)` + keyset + `
ORDER BY share DESC, work_unit_id ASC, repo_uuid ASC
LIMIT {` + unitLimitParam + `:UInt32}`
	return readers.WithSettings(statement, readers.Settings{MaxMemoryUsage: 512 << 20})
}

// orgRefs is the organization's reference list: a unit with no reference and
// no repository keeps an unattributed placeholder row.
func orgRefs(core string) string {
	return strings.Replace(core, repoRefsExpression+" AS refs",
		"if(empty("+repoRefsExpression+"), "+orgNoRefUnit+", "+repoRefsExpression+") AS refs", 1)
}
