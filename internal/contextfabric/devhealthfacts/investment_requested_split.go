package devhealthfacts

import (
	"strings"

	"github.com/full-chaos/dev-health-go/readers"
)

// A read scoped to some repositories (the repository and team mix, the
// repository and team unit listing) computes the same per-(window, work unit,
// repository) split as repoSplitCore, without holding the organization's
// fan-out. The latest version of every unit is still read in one pass over
// work_unit_investments, but its values are kept only when that version
// reaches a requested repository. A kept unit's references are resolved once
// and folded into one (repository, key) set per unit; c and n are counted from
// that set, and rows are expanded only for the requested repositories and the
// unit's windows. repoSplitCore holds one aggregation state per window, unit
// and repository of every unit instead, which grew with the organization.

// requestedRepositoryReach is true for a reference r that reaches a
// repository in {ids} by the rule repoLookupJoin applies: a direct repository
// id, or a (provider, name) lookup equal to a requested repository's own
// lookup row.
// requestedRepositories are the rows requestedSplitRows expands: the
// resolved repositories of a unit's references that are in {ids}.
const requestedRepositories = "arrayFilter(x -> x != '' AND has({ids:Array(String)}, x), arrayDistinct(arrayMap(p -> p.1, pairs)))"

const requestedRepositoryReach = `if(r.1 != '', has({ids:Array(String)}, r.1), has((
						SELECT groupArray((provider, repo)) FROM (
							SELECT argMax(repo, last_synced) AS repo,
								if(uniqExact(provider) = 1, argMax(provider, last_synced), '') AS provider
							FROM repos
							WHERE org_id = {org_id:String} AND toString(id) IN {ids:Array(String)}
							GROUP BY id
						)), (r.2, r.3)))`

// requestedSplitRows returns, per window, kept work unit and requested
// repository: win, work_unit_id, repo_uuid, c, n, effort_value,
// theme_distribution_json, bugfix_share, span_from and, with unitColumns,
// from_ts, to_ts, prs (the unit's PR numbers in that repository),
// unresolved_n and unresolved_refs (its references that resolved to no
// repository). The values are those of repoSplitCore's rows for the same
// (window, unit, repository), with n and the unresolved columns taken over
// the unit's whole reference set.
func requestedSplitRows(memberships []string, spanSentinel bool, scope membershipScope, unitColumns bool) string {
	wins := "arrayFilter(w -> w >= 0, [" + strings.Join(memberships, ", ") + "])"
	if spanSentinel {
		wins = "arrayConcat([" + spanSentinelWindow + "], " + wins + ")"
	}
	rowCols, unitAgg := "", ""
	if unitColumns {
		rowCols = `,
	from_ts, to_ts,
	arrayMap(x -> x.2, arrayFilter(x -> x.1 = repo_uuid, repo_prs)) AS prs,
	countEqual(arrayMap(p -> p.1, pairs), '') AS unresolved_n,
	arrayMap(p -> p.2, arrayFilter(p -> p.1 = '', pairs)) AS unresolved_refs`
		unitAgg = `
		any(from_ts) AS from_ts, any(to_ts) AS to_ts,
		groupUniqArray((ref_repo, pr_number)) AS repo_prs,`
	}
	return `SELECT win, work_unit_id, repo_uuid,
	countEqual(arrayMap(p -> p.1, pairs), repo_uuid) AS c, length(pairs) AS n,
	effort_value, theme_distribution_json, bugfix_share, span_from` + rowCols + `
FROM (
	SELECT work_unit_id,
		any(effort_value) AS effort_value,
		any(theme_distribution_json) AS theme_distribution_json,
		any(bugfix_share) AS bugfix_share,
		any(span_from) AS span_from,
		any(wins) AS wins,` + unitAgg + `
		groupUniqArray((ref_repo, if(ref_repo = '', ref_text, concat(ref_repo, '#', pr_number)))) AS pairs
	FROM (
		SELECT parsed.work_unit_id AS work_unit_id, parsed.effort_value AS effort_value,
			parsed.theme_distribution_json AS theme_distribution_json, parsed.bugfix_share AS bugfix_share,
			parsed.span_from AS span_from, parsed.from_ts AS from_ts, parsed.to_ts AS to_ts, parsed.wins AS wins,
			parsed.pr_number AS pr_number, parsed.ref_text AS ref_text,
			if(parsed.uuid_direct != '', parsed.uuid_direct, ifNull(rl.repo_uuid, '')) AS ref_repo
		FROM (
			SELECT work_unit_id, effort_value, theme_distribution_json, bugfix_share, from_ts AS span_from, from_ts, to_ts, wins,
				ref.1 AS uuid_direct, ref.2 AS lookup_provider, ref.3 AS lookup_repo, ref.4 AS pr_number, ref.5 AS ref_text
			FROM (
				SELECT work_unit_id, latest.1 AS repo_id, latest.2 AS from_ts, latest.3 AS to_ts, latest.4 AS effort_value,
					latest.5 AS theme_distribution_json, latest.6 AS bugfix_share, latest.7 AS pr_refs,
					` + wins + ` AS wins
				FROM (
					SELECT work_unit_id,
						argMaxIf(tuple(repo_id, from_ts, to_ts, effort_value, theme_distribution_json, unit_bugfix_share, pr_refs), computed_at, reaches) AS latest
					FROM (
						SELECT work_unit_id, computed_at, repo_id, from_ts, to_ts, effort_value, theme_distribution_json,
							ifNull(subcategory_distribution_json['` + readers.BugfixSubcategoryKey + `'], 0.0) AS unit_bugfix_share,
							` + prRefsExpression + ` AS pr_refs,
							arrayExists(r -> ` + requestedRepositoryReach + `, ` + repoRefsExpression + `) AS reaches
						FROM work_unit_investments
						WHERE org_id = {org_id:String}` + supersededWorkUnitIDsFilter() + scope.filter() + `
					)
					GROUP BY work_unit_id
					HAVING argMax(reaches, computed_at)
				)
			)
			ARRAY JOIN ` + repoRefsExpression + ` AS ref
		) AS parsed
		LEFT JOIN ` + repoLookupJoin + `
	)
	GROUP BY work_unit_id
)
ARRAY JOIN ` + requestedRepositories + ` AS repo_uuid
ARRAY JOIN wins AS win
`
}
