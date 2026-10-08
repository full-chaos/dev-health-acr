package devhealthfacts

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-go/readers"
)

// CHAOS-7257: the owning-team roll-up statement (readProjectThemeMix) reads
// work_unit_investments ONCE.
//
// Before this, the latest-row CTE was referenced through `windowed` at three
// places (repo_linked, evidence_resolved, excluded_no_repo_link). ClickHouse
// inlines a CTE at every reference, so one project read scanned the table three
// times: 65.83 MiB against the 64 MiB max_bytes_to_read on prod (Code 307
// TOO_MANY_BYTES), every project subject, every attempt. CHAOS-6594 fixed the
// same class for the repository and team mix (investment_repo_mix.go); CHAOS-6611
// was the design note for this statement.
//
// Shape, innermost first (each stage is referenced exactly once, so nothing
// re-reads the table):
//
//  1. latest + window: the one read of work_unit_investments (argMax per work
//     unit, scoped like ops' LatestWorkUnitInvestmentsSource), then the window
//     overlap predicate.
//  2. per-unit derivation: the five theme scalars (each the SUM of every entry
//     of its key, as the expansion it replaces summed them; the bugfix share is
//     a lookup, as before) and the bugfix share are read out of the two Map
//     columns here and the maps go no further; only a unit
//     with no repo_id keeps its evidence refs (the repo-keyed arm never needs
//     them, and the evidence arm only counts units whose repo_id IS NULL).
//  3. evidence vote: a unit with refs is expanded one row per ref (LEFT ARRAY
//     JOIN, so a unit without refs survives as one empty-ref row, which never
//     votes: its vote is the empty team whatever the attribution table holds
//     for an empty work item id), each ref is
//     resolved to a work item id, joined to its primary team attribution, and
//     the unit's vote is the team with the most attributed refs (ties: the
//     greater team id), exactly readTeamThemeMix's vote.
//  4. attribution: ONE join of the per-unit rows to project_link, a table of
//     (project, key) pairs with key 'r:<repo uuid>' for a project's owned
//     repositories and 't:<team id>' for a project's owning teams. A unit with
//     a repo_id joins by its repository; a unit without one joins by the team
//     its evidence vote chose. The two arms are disjoint (repo_id IS NOT NULL
//     vs IS NULL), so the repo arm carries the counted mix and the team arm
//     only counts units the repo-keyed join cannot reach
//     (work_units_without_repo_link).
//
// Result parity with the multi-reference statement it replaces is pinned by
// chaos7257_project_rollup_parity_integration_test.go, which keeps the old text
// as its oracle and compares rows on seeded data.
//
// The statement is wrapped in an outer `SELECT * FROM (WITH ... SELECT ...)`
// shell -- readTeamThemeMix's own statement uses the identical shape -- because
// the query CLIENT'S read-only validator (dev-health-go
// clickhouse.validateReadOnlyStatement) requires the statement's OWN first token
// to be literally SELECT; a bare `WITH ...` statement (valid ClickHouse, invalid
// by this guard) is refused as ErrUnsafeStatement before it ever reaches the
// server.
//
// It returns at most ONE row per requested project (every aggregate collapses to
// project_key), probed at maxFactRowsProbe (withRowProbeLimit) rather than a
// plain LIMIT, so a project population sitting exactly at the served cap is
// distinguishable from one that overflowed it.
// themeEntrySumSQL is the sum of EVERY entry of theme in the row's theme map.
// The column is Map(String, Float64) and ClickHouse fills it with whatever
// entries the writer sent, so a key can appear twice; the statement this one
// replaced expanded the map (ARRAY JOIN over its entries) and summed each
// matching entry. A lookup (map['theme']) reads ONE entry and moves the served
// share, so the lookup is not used for the themes here. An empty map, or one
// without the key, sums to 0.
func themeEntrySumSQL(theme string) string {
	return "arraySum(arrayMap(kv -> kv.2, arrayFilter(kv -> kv.1 = '" + theme + "', CAST(theme_distribution_json AS Array(Tuple(String, Float64))))))"
}

func projectRollupMixStatement(timeBound factTimeBound) string {
	ownershipPredicate := ownershipValidityPredicate(timeBound)
	rangePredicate := themeInvestmentRangePredicate(timeBound, "from_ts", "to_ts")
	return withRowProbeLimit(`SELECT * FROM (
WITH project_team AS (
	SELECT DISTINCT provider AS project_provider, id AS project_id, team_id
	FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
),
project_repo_team AS (
	SELECT DISTINCT pt.project_provider AS project_provider, pt.project_id AS project_id, toUUID(tro.repo_key) AS repo_id, pt.team_id AS team_id
	FROM project_team AS pt
	INNER JOIN ` + ownedRepositoriesSource(ownershipPredicate) + ` AS tro ON tro.team_id = pt.team_id
),
project_link AS (
	SELECT project_provider, project_id, 'repo' AS link_kind, concat('r:', toString(repo_id)) AS link_key, groupUniqArray(team_id) AS link_teams
	FROM project_repo_team
	GROUP BY project_provider, project_id, repo_id
	UNION ALL
	SELECT project_provider, project_id, 'team' AS link_kind, concat('t:', team_id) AS link_key, CAST([], 'Array(String)') AS link_teams
	FROM project_team
),
repo_lookup AS (
	SELECT toString(id) AS repo_uuid,
		argMax(repo, last_synced) AS repo,
		if(uniqExact(provider) = 1, argMax(provider, last_synced), '') AS provider
	FROM repos
	WHERE org_id = {org_id:String}
	GROUP BY id
),
wita AS (
	SELECT work_item_id, team_id
	FROM work_item_team_attributions FINAL
	WHERE org_id = {org_id:String} AND ` + devhealthschema.TeamAttributionPredicate("", devhealthschema.AttributionScopeOrg) + `
	  AND (work_item_id, computed_at) IN (
		  SELECT work_item_id, max(computed_at)
		  FROM work_item_team_attributions
		  WHERE org_id = {org_id:String}
		  GROUP BY work_item_id
	  )
)
SELECT
	concat(l.project_provider, ':', l.project_id) AS project_key,
	sumIf(u.theme_feature_delivery * u.effort_value, l.link_kind = 'repo') AS feature_delivery,
	sumIf(u.theme_operational * u.effort_value, l.link_kind = 'repo') AS operational,
	sumIf(u.theme_maintenance * u.effort_value, l.link_kind = 'repo') AS maintenance,
	sumIf(u.theme_quality * u.effort_value, l.link_kind = 'repo') AS quality,
	sumIf(u.theme_risk * u.effort_value, l.link_kind = 'repo') AS risk,
	sumIf(u.bugfix_share * u.effort_value, l.link_kind = 'repo') AS bugfix_weighted,
	uniqExactIf(u.work_unit_id, l.link_kind = 'repo') AS work_units,
	uniqExactIf(u.repo_id, l.link_kind = 'repo') AS repos,
	length(groupUniqArrayArrayIf(l.link_teams, l.link_kind = 'repo')) AS team_count,
	uniqExactIf(u.work_unit_id, l.link_kind = 'team') AS excluded_no_repo_link
FROM (
	SELECT work_unit_id, repo_id, effort_value,
		theme_feature_delivery, theme_operational, theme_maintenance, theme_quality, theme_risk, bugfix_share,
		multiIf(repo_id IS NOT NULL, concat('r:', toString(repo_id)), vote_team_id != '', concat('t:', vote_team_id), '') AS link_key
	FROM (
		SELECT voted.work_unit_id AS work_unit_id,
			any(voted.repo_id) AS repo_id,
			any(voted.effort_value) AS effort_value,
			any(voted.theme_feature_delivery) AS theme_feature_delivery,
			any(voted.theme_operational) AS theme_operational,
			any(voted.theme_maintenance) AS theme_maintenance,
			any(voted.theme_quality) AS theme_quality,
			any(voted.theme_risk) AS theme_risk,
			any(voted.bugfix_share) AS bugfix_share,
			argMax(voted.vote_team_id, (voted.cnt, voted.vote_team_id)) AS vote_team_id
		FROM (
			SELECT resolved.work_unit_id AS work_unit_id,
				if(resolved.evidence_ref = '', '', ifNull(nullIf(t.team_id, ''), '')) AS vote_team_id,
				uniqExactIf(resolved.resolved_wi_id, ` + evidenceVoteAttributedPredicate + `) AS cnt,
				any(resolved.repo_id) AS repo_id,
				any(resolved.effort_value) AS effort_value,
				any(resolved.theme_feature_delivery) AS theme_feature_delivery,
				any(resolved.theme_operational) AS theme_operational,
				any(resolved.theme_maintenance) AS theme_maintenance,
				any(resolved.theme_quality) AS theme_quality,
				any(resolved.theme_risk) AS theme_risk,
				any(resolved.bugfix_share) AS bugfix_share
			FROM (
				SELECT expanded.work_unit_id AS work_unit_id, expanded.repo_id AS repo_id, expanded.effort_value AS effort_value,
					expanded.theme_feature_delivery AS theme_feature_delivery, expanded.theme_operational AS theme_operational,
					expanded.theme_maintenance AS theme_maintenance, expanded.theme_quality AS theme_quality, expanded.theme_risk AS theme_risk,
					expanded.bugfix_share AS bugfix_share, expanded.evidence_ref AS evidence_ref,
					multiIf(
						NOT match(expanded.evidence_ref, '^[0-9a-fA-F-]{36}#pr[0-9]+$'), expanded.evidence_ref,
						evidence_repo.repo = '' OR evidence_repo.provider = '', '',
						concat(if(evidence_repo.provider = 'gitlab', 'gitlab:', 'ghpr:'), evidence_repo.repo,
							if(evidence_repo.provider = 'gitlab', '!', '#'), splitByString('#pr', expanded.evidence_ref)[2])
					) AS resolved_wi_id
				FROM (
					SELECT unit.work_unit_id AS work_unit_id, unit.repo_id AS repo_id, unit.effort_value AS effort_value,
						unit.theme_feature_delivery AS theme_feature_delivery, unit.theme_operational AS theme_operational,
						unit.theme_maintenance AS theme_maintenance, unit.theme_quality AS theme_quality, unit.theme_risk AS theme_risk,
						unit.bugfix_share AS bugfix_share, evidence_ref
					FROM (
						SELECT work_unit_id, repo_id, effort_value,
							` + themeEntrySumSQL(contextfabric.ThemeFeatureDelivery) + ` AS theme_feature_delivery,
							` + themeEntrySumSQL(contextfabric.ThemeOperational) + ` AS theme_operational,
							` + themeEntrySumSQL(contextfabric.ThemeMaintenance) + ` AS theme_maintenance,
							` + themeEntrySumSQL(contextfabric.ThemeQuality) + ` AS theme_quality,
							` + themeEntrySumSQL(contextfabric.ThemeRisk) + ` AS theme_risk,
							ifNull(subcategory_distribution_json['` + readers.BugfixSubcategoryKey + `'], 0.0) AS bugfix_share,
							if(repo_id IS NULL,
								arrayDistinct(arrayConcat(
									JSONExtract(structural_evidence_json, 'issues', 'Array(String)'),
									JSONExtract(structural_evidence_json, 'prs', 'Array(String)')
								)),
								CAST([], 'Array(String)')) AS evidence_refs
						FROM (
							SELECT work_unit_id,
								(argMax(tuple(repo_id), computed_at)).1 AS repo_id,
								argMax(from_ts, computed_at) AS from_ts,
								argMax(to_ts, computed_at) AS to_ts,
								argMax(effort_value, computed_at) AS effort_value,
								argMax(theme_distribution_json, computed_at) AS theme_distribution_json,
								argMax(subcategory_distribution_json, computed_at) AS subcategory_distribution_json,
								argMax(structural_evidence_json, computed_at) AS structural_evidence_json
							FROM work_unit_investments
							WHERE org_id = {org_id:String}` + supersededWorkUnitIDsFilter() + investmentMembershipScopeFilter() + `
							GROUP BY work_unit_id
						)
						WHERE 1` + rangePredicate + `
					) AS unit
					LEFT ARRAY JOIN unit.evidence_refs AS evidence_ref
				) AS expanded
				LEFT JOIN repo_lookup AS evidence_repo ON evidence_repo.repo_uuid = splitByString('#pr', expanded.evidence_ref)[1]
			) AS resolved
			LEFT JOIN wita AS t ON t.work_item_id = resolved.resolved_wi_id
			GROUP BY resolved.work_unit_id, vote_team_id
		) AS voted
		GROUP BY voted.work_unit_id
	)
) AS u
INNER JOIN project_link AS l ON l.link_key = u.link_key
GROUP BY l.project_provider, l.project_id
HAVING work_units > 0
)
ORDER BY project_key`)
}
