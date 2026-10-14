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
// requestedRepositories expands each folded unit into its resolved
// repositories that are in {ids}. The set test is an IN over the bound array
// (one hash set); the bound array itself in arrayIntersect or has() is copied
// per row.
const requestedRepositories = `ARRAY JOIN arrayFilter(x -> x != '' AND x IN {ids:Array(String)}, arrayDistinct(arrayMap(p -> p.1, pairs))) AS repo_uuid
ARRAY JOIN wins AS win`

// requestedRepositoryLookups are the (provider, name) lookup rows of the
// repositories in {ids}.
const requestedRepositoryLookups = `(
						SELECT groupArray((provider, repo)) FROM (
							SELECT argMax(repo, last_synced) AS repo,
								if(uniqExact(provider) = 1, argMax(provider, last_synced), '') AS provider
							FROM repos
							WHERE org_id = {org_id:String} AND toString(id) IN {ids:Array(String)}
							GROUP BY id
						))`

// referenceKeys are, per work-unit row, the parts of its references the
// reach tests read, without building the full reference tuples: the direct
// (repository id, PR number) pairs and the (provider, name, PR number)
// lookups. They are the parts prRefsExpression parses, by the same patterns.
const referenceKeys = `arrayMap(r -> (substring(r, 1, 36), substring(r, 40)),
								arrayFilter(r -> match(r, '^[0-9a-fA-F-]{36}#pr[0-9]+$'), JSONExtract(structural_evidence_json, 'prs', 'Array(String)'))) AS direct_refs,
							arrayConcat(
								arrayMap(r -> ('github', substring(r, 6, position(r, '#') - 6), substring(r, position(r, '#') + 1)),
									arrayFilter(r -> match(r, '^ghpr:[^#]+#[0-9]+$'), JSONExtract(structural_evidence_json, 'issues', 'Array(String)'))),
								arrayMap(r -> ('gitlab', substring(r, 8, position(r, '!') - 8), substring(r, position(r, '!') + 1)),
									arrayFilter(r -> match(r, '^gitlab:[^!]+![0-9]+$'), JSONExtract(structural_evidence_json, 'issues', 'Array(String)')))
							) AS lookup_refs`

// requestedRepositoryReach is true when a row's references reach a
// repository in {ids} by the rule repoLookupJoin applies: a direct repository
// id, a (provider, name) lookup equal to a requested repository's own lookup
// row, or, for a row with no reference, its own repository.
const requestedRepositoryReach = `(hasAny(arrayMap(x -> x.1, direct_refs), {ids:Array(String)})
								OR hasAny(arrayMap(x -> (x.1, x.2), lookup_refs), ` + requestedRepositoryLookups + `)
								OR (empty(direct_refs) AND empty(lookup_refs) AND repo_id IS NOT NULL AND toString(repo_id) IN {ids:Array(String)}))`

// requestedSplitRows returns, per window, kept work unit and requested
// repository: win, work_unit_id, repo_uuid, c, n, effort_value,
// theme_distribution_json, bugfix_share, span_from and, with unitColumns,
// from_ts, to_ts, prs (the unit's PR numbers in that repository),
// unresolved_n and unresolved_refs (its references that resolved to no
// repository). The values are those of repoSplitCore's rows for the same
// (window, unit, repository), with n and the unresolved columns taken over
// the unit's whole reference set.
func requestedSplitRows(memberships []string, spanSentinel bool, scope membershipScope, unitColumns bool) string {
	return foldedSplitRows(memberships, spanSentinel, scope, unitColumns, "")
}

// linkedPullRequestSplitRows returns, per window, kept work unit and
// resolved (repository, pull request) in linked: win, work_unit_id, repo_uuid,
// pr_number, c, n, effort_value, theme_distribution_json, bugfix_share,
// span_from. They are the whole-organization split's rows grouped per
// reference as well, for those pull requests. linked is an Array(Tuple(String, String, String)) expression
// of (repository id, PR number, team id).
func linkedPullRequestSplitRows(memberships []string, scope membershipScope, linked string) string {
	return foldedSplitRows(memberships, false, scope, false, linked)
}

// linkedPullRequestReach is true when a row's references resolve to a linked
// (repository, pull request): a direct repository id with its number, or a
// (provider, name, number) lookup equal to a linked repository's own lookup
// row with the linked number. A row's own repository names no pull request.
func linkedPullRequestReach(linked string) string {
	return `(hasAny(direct_refs, arrayMap(x -> (x.1, x.2), ` + linked + `))
								OR hasAny(lookup_refs, (
						SELECT groupUniqArray((rl.provider, rl.repo, lx.lp_pr)) FROM (
							SELECT arrayJoin(` + linked + `) AS lp, lp.1 AS lp_repo, lp.2 AS lp_pr
						) AS lx
						INNER JOIN ` + repoLookupRows + ` AS rl ON rl.repo_uuid = lx.lp_repo)))`
}

// foldedSplitRows folds each kept unit's references once (see the top of this
// file). With linked empty a unit is kept and expanded per requested
// repository; otherwise per linked (repository, pull request).
func foldedSplitRows(memberships []string, spanSentinel bool, scope membershipScope, unitColumns bool, linked string) string {
	reaches := requestedRepositoryReach
	if linked != "" {
		reaches = linkedPullRequestReach(linked)
	}
	wins := "arrayFilter(w -> w >= 0, [" + strings.Join(memberships, ", ") + "])"
	if spanSentinel {
		wins = "arrayConcat([" + spanSentinelWindow + "], " + wins + ")"
	}
	rowCols, unitAgg := "", ""
	head := `SELECT win, work_unit_id, repo_uuid,
	countEqual(arrayMap(p -> p.1, pairs), repo_uuid) AS c, length(pairs) AS n,`
	expand := requestedRepositories
	pairs := `groupUniqArray((ref_repo, if(ref_repo = '', ref_text, concat(ref_repo, '#', pr_number)))) AS pairs`
	if linked != "" {
		// The unit's reference array must not be read above the expansion: an
		// array read after ARRAY JOIN (or captured in a lambda over repo_prs)
		// is copied onto every (unit, pull request) row, which grew with the
		// unit's references times its pull requests. Its length is taken here
		// as a number. c is 1: pairs and repo_prs are distinct sets over the
		// same reference rows, so the key of a resolved rp (rp.1 != '', the
		// only rows the WHERE keeps) is in the unit's pairs exactly once.
		head = `SELECT win, work_unit_id, rp.1 AS repo_uuid, rp.2 AS pr_number,
	2 AS c, n,`
		unitAgg = `
		groupUniqArray((ref_repo, pr_number)) AS repo_prs,`
		pairs += `,
		length(pairs) AS n`
		expand = `ARRAY JOIN repo_prs AS rp
ARRAY JOIN wins AS win
WHERE rp.1 != '' AND rp IN (SELECT (l.1, l.2) FROM (SELECT arrayJoin(` + linked + `) AS l))`
	}
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
	return head + `
	effort_value, theme_distribution_json, bugfix_share, span_from` + rowCols + `
FROM (
	SELECT work_unit_id,
		any(effort_value) AS effort_value,
		any(theme_distribution_json) AS theme_distribution_json,
		any(bugfix_share) AS bugfix_share,
		any(span_from) AS span_from,
		any(wins) AS wins,` + unitAgg + `
		` + pairs + `
	FROM (
		SELECT parsed.work_unit_id AS work_unit_id, parsed.effort_value AS effort_value,
			parsed.theme_distribution_json AS theme_distribution_json, parsed.bugfix_share AS bugfix_share,
			parsed.span_from AS span_from, parsed.from_ts AS from_ts, parsed.to_ts AS to_ts, parsed.wins AS wins,
			parsed.pr_number AS pr_number, parsed.ref_text AS ref_text,
			ifNull(if(parsed.uuid_direct != '', parsed.uuid_direct, ifNull(rl.repo_uuid, '')), '') AS ref_repo
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
							` + referenceKeys + `,
							` + reaches + ` AS reaches,
							if(reaches, ` + prRefsExpression + `, []) AS pr_refs
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
` + expand + `
`
}
