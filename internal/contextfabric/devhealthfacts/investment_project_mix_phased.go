package devhealthfacts

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// CHAOS-7271: the project theme-mix reads are phased so no single statement
// reads every wide column of work_unit_investments.
//
// One statement that reads structural_evidence_json (46% of the table),
// subcategory_distribution_json (27%) and the theme map read ~1.3x the table
// (CHAOS-7257) and grows with it: at twice the prod table it is over the 64 MiB
// max_bytes_to_read. The table's ORDER BY key is (org_id, work_unit_id), the
// window columns are not in it, and the window applies to each unit's LATEST
// version, so the window cannot be pushed into the raw scan; at prod's row count
// the table is a handful of granules, so a row prefilter prunes nothing. The
// bytes come down only by not reading a column a statement does not need.
//
// Phase 0 (projectMixScopeStatement) reads the narrow columns plus the
// membership scope and returns ONE row: the latest-version unit ids that pass
// the supersession filter, the membership run and the window, each with the
// computed_at of the version it selected. Every later phase reads only the
// columns it needs, restricted to exactly those (unit, version) pairs, so a
// version written after phase 0, whatever its date, is invisible to every phase
// and all phases see one version per unit. The membership scope is read once, in
// phase 0; so is the roll-up's project -> repository / team link table, handed
// to the later phases as bound data.
//
//	roll-up:  repo arm      repo_id, effort, theme map, subcategory map
//	          evidence arm  repo_id, structural_evidence_json
//	native:   placement     structural_evidence_json (issue refs), presence view
//	          unit values   effort, theme map, subcategory map
//
// The roll-up's two arms are disjoint (repo_id IS NOT NULL vs IS NULL) and the
// arm the old statement counted as excluded_no_repo_link never carried a theme
// value, so the phases merge by project key. The native mix's unit values are
// joined to the placement in Go by work unit id.
//
// The single statements these replace stay (projectRollupMixStatement,
// projectNativeMixStatement) as the parity oracle of
// chaos7271_project_mix_phased_integration_test.go.

// projectMixPinnedFilter restricts a phase to EXACTLY the (work unit, version)
// pairs phase 0 selected: the unit's computed_at, in whole milliseconds (the
// column is DateTime64(3)), must be the one phase 0 saw. A newer version, or a
// version dated between the unit's selected one and the newest one, is invisible
// to every phase.
const projectMixPinnedFilter = `
                              AND (work_unit_id, toUnixTimestamp64Milli(computed_at)) IN (
                                  SELECT pin.1, pin.2
                                  FROM (SELECT arrayJoin(JSONExtract({pin_json:String}, 'Array(Tuple(String, Int64))')) AS pin)
                              )`

// projectMixBoundLinkCTE is the project -> owned repository / owning team link
// table phase 0 captured, bound as data: a phase never re-derives ownership, so
// one answer never mixes two ownership states.
const projectMixBoundLinkCTE = `project_link AS (
	SELECT link.1 AS project_provider, link.2 AS project_id, link.3 AS link_kind, link.4 AS link_key, link.5 AS link_teams
	FROM (SELECT arrayJoin(JSONExtract({link_json:String}, 'Array(Tuple(String, String, String, String, Array(String)))')) AS link)
)`

type projectMixScope struct {
	unitIDs    []string
	versionsMs []int64 // versionsMs[i] is the computed_at, in ms, of unitIDs[i]'s selected version
}

func (s projectMixScope) bindings() []readers.Binding {
	// The pairs travel as ONE JSON string, not an Array parameter: the server
	// parses an array parameter literal element by element and, on 40,000 ids,
	// runs past the client's 10s timeout; a string parameter is constant work.
	// JSON also keeps any id that contains a separator intact.
	pins := make([][]any, 0, len(s.unitIDs))
	for i, id := range s.unitIDs {
		pins = append(pins, []any{id, s.versionsMs[i]})
	}
	encoded, err := json.Marshal(pins)
	if err != nil {
		encoded = []byte("[]")
	}
	return []readers.Binding{{Name: "pin_json", Value: string(encoded)}}
}

// projectMixBetweenPhasesKey carries a test hook run after phase 0 and before
// the next phase (chaos7257_oracle_test.go).
type projectMixBetweenPhasesKey struct{}

func projectMixBetweenPhases(ctx context.Context) {
	if hook, ok := ctx.Value(projectMixBetweenPhasesKey{}).(func()); ok {
		hook()
	}
}

func projectMixScopeStatement(timeBound factTimeBound) string {
	return `SELECT groupArray(work_unit_id) AS unit_ids, groupArray(toUnixTimestamp64Milli(latest_at)) AS version_ms FROM (
    SELECT work_unit_id, max(computed_at) AS latest_at,
        argMax(from_ts, computed_at) AS from_ts,
        argMax(to_ts, computed_at) AS to_ts
    FROM work_unit_investments
    WHERE org_id = {org_id:String}` + supersededWorkUnitIDsFilter() + investmentMembershipScopeFilter() + `
    GROUP BY work_unit_id
)
WHERE 1` + themeInvestmentRangePredicate(timeBound, "from_ts", "to_ts")
}

func readProjectMixScope(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, ids []string, timeBound factTimeBound) (projectMixScope, error) {
	var scope projectMixScope
	extra := make([]readers.Binding, 0, 2)
	for _, b := range timeBound.bindings() {
		extra = append(extra, readers.Binding{Name: b.Name, Value: b.Value})
	}
	err := readers.QueryOrgScopedNamed(ctx, client, "ReadProjectMixScope", projectMixScopeStatement(timeBound), orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		return row.Scan(&scope.unitIDs, &scope.versionsMs)
	}, extra...)
	if err == nil && len(scope.versionsMs) != len(scope.unitIDs) {
		err = fmt.Errorf("project mix scope arrays disagree: %d ids, %d versions", len(scope.unitIDs), len(scope.versionsMs))
	}
	return scope, err
}

// projectRollupMixRow is one project's roll-up mix, the columns
// projectRollupMixStatement returns.
type projectRollupMixRow struct {
	ProjectKey                                                               string
	FeatureDelivery, Operational, Maintenance, Quality, Risk, BugfixWeighted float64
	WorkUnits, Repos, Teams, ExcludedNoRepoLink                              uint64
}

func projectLinkCTEs(ownershipPredicate string) string {
	return `project_team AS (
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
)`
}

// projectMixLinkStatement returns ONE row of parallel arrays: the roll-up's
// project_link rows (project, link kind, link key, link teams) as ownership
// stands now. Phase 0 reads it once; the later phases are handed the result.
func projectMixLinkStatement(timeBound factTimeBound) string {
	return `SELECT * FROM (
WITH ` + projectLinkCTEs(ownershipValidityPredicate(timeBound)) + `
SELECT groupArray(project_provider), groupArray(project_id), groupArray(link_kind), groupArray(link_key), groupArray(link_teams)
FROM project_link
)`
}

func readProjectMixLinks(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, ids []string, timeBound factTimeBound) (string, error) {
	var provider, project, kind, key []string
	var teams [][]string
	extra := make([]readers.Binding, 0, 2)
	for _, b := range timeBound.bindings() {
		extra = append(extra, readers.Binding{Name: b.Name, Value: b.Value})
	}
	if err := readers.QueryOrgScopedNamed(ctx, client, "ReadProjectMixLinks", projectMixLinkStatement(timeBound), orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		return row.Scan(&provider, &project, &kind, &key, &teams)
	}, extra...); err != nil {
		return "", err
	}
	if len(project) != len(provider) || len(kind) != len(provider) || len(key) != len(provider) || len(teams) != len(provider) {
		return "", fmt.Errorf("project mix link arrays disagree: %d/%d/%d/%d/%d", len(provider), len(project), len(kind), len(key), len(teams))
	}
	links := make([][]any, 0, len(provider))
	for i := range provider {
		t := teams[i]
		if t == nil {
			t = []string{}
		}
		links = append(links, []any{provider[i], project[i], kind[i], key[i], t})
	}
	encoded, err := json.Marshal(links)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// projectRollupRepoThemesStatement is the roll-up's counted mix: the units with
// a repo_id, joined to the project's owned repositories. It reads the theme map
// and neither structural_evidence_json nor the subcategory map.
func projectRollupRepoThemesStatement() string {
	return withRowProbeLimit(`SELECT * FROM (
WITH ` + projectMixBoundLinkCTE + `
SELECT
	concat(l.project_provider, ':', l.project_id) AS project_key,
	sumIf(u.theme_feature_delivery * u.effort_value, l.link_kind = 'repo') AS feature_delivery,
	sumIf(u.theme_operational * u.effort_value, l.link_kind = 'repo') AS operational,
	sumIf(u.theme_maintenance * u.effort_value, l.link_kind = 'repo') AS maintenance,
	sumIf(u.theme_quality * u.effort_value, l.link_kind = 'repo') AS quality,
	sumIf(u.theme_risk * u.effort_value, l.link_kind = 'repo') AS risk,
	uniqExactIf(u.work_unit_id, l.link_kind = 'repo') AS work_units,
	uniqExactIf(u.repo_id, l.link_kind = 'repo') AS repos,
	length(groupUniqArrayArrayIf(l.link_teams, l.link_kind = 'repo')) AS team_count
FROM (
	SELECT work_unit_id, repo_id, effort_value,
		` + themeEntrySumSQL(contextfabric.ThemeFeatureDelivery) + ` AS theme_feature_delivery,
		` + themeEntrySumSQL(contextfabric.ThemeOperational) + ` AS theme_operational,
		` + themeEntrySumSQL(contextfabric.ThemeMaintenance) + ` AS theme_maintenance,
		` + themeEntrySumSQL(contextfabric.ThemeQuality) + ` AS theme_quality,
		` + themeEntrySumSQL(contextfabric.ThemeRisk) + ` AS theme_risk,
		concat('r:', toString(repo_id)) AS link_key
	FROM (
		SELECT work_unit_id,
			(argMax(tuple(repo_id), computed_at)).1 AS repo_id,
			argMax(effort_value, computed_at) AS effort_value,
			argMax(theme_distribution_json, computed_at) AS theme_distribution_json
		FROM work_unit_investments
		WHERE org_id = {org_id:String}` + projectMixPinnedFilter + `
		GROUP BY work_unit_id
	)
	WHERE repo_id IS NOT NULL
) AS u
INNER JOIN project_link AS l ON l.link_key = u.link_key
GROUP BY l.project_provider, l.project_id
HAVING work_units > 0
)
ORDER BY project_key`)
}

// projectRollupRepoBugfixStatement is the counted mix's bugfix-weighted sum. It
// is the same population and the same join as projectRollupRepoThemesStatement
// and reads the subcategory map instead of the theme map, so no statement reads
// both maps.
func projectRollupRepoBugfixStatement() string {
	return withRowProbeLimit(`SELECT * FROM (
WITH ` + projectMixBoundLinkCTE + `
SELECT
	concat(l.project_provider, ':', l.project_id) AS project_key,
	sumIf(u.bugfix_share * u.effort_value, l.link_kind = 'repo') AS bugfix_weighted
FROM (
	SELECT work_unit_id, repo_id, effort_value,
		ifNull(subcategory_distribution_json['` + readers.BugfixSubcategoryKey + `'], 0.0) AS bugfix_share,
		concat('r:', toString(repo_id)) AS link_key
	FROM (
		SELECT work_unit_id,
			(argMax(tuple(repo_id), computed_at)).1 AS repo_id,
			argMax(effort_value, computed_at) AS effort_value,
			argMax(subcategory_distribution_json, computed_at) AS subcategory_distribution_json
		FROM work_unit_investments
		WHERE org_id = {org_id:String}` + projectMixPinnedFilter + `
		GROUP BY work_unit_id
	)
	WHERE repo_id IS NOT NULL
) AS u
INNER JOIN project_link AS l ON l.link_key = u.link_key
GROUP BY l.project_provider, l.project_id
)
ORDER BY project_key`)
}

// projectRollupEvidenceArmStatement counts, per project, the units without a
// repo_id that the owning teams' evidence vote reaches
// (work_units_without_repo_link). It does not read the theme or subcategory
// maps: that arm never carried a theme value.
func projectRollupEvidenceArmStatement() string {
	return withRowProbeLimit(`SELECT * FROM (
WITH ` + projectMixBoundLinkCTE + `,
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
	WHERE org_id = {org_id:String} AND is_primary = 1
	  AND (work_item_id, computed_at) IN (
		  SELECT work_item_id, max(computed_at)
		  FROM work_item_team_attributions
		  WHERE org_id = {org_id:String}
		  GROUP BY work_item_id
	  )
)
SELECT
	concat(l.project_provider, ':', l.project_id) AS project_key,
	uniqExact(u.work_unit_id) AS excluded_no_repo_link
FROM (
	SELECT voted.work_unit_id AS work_unit_id,
		argMax(voted.vote_team_id, (voted.cnt, voted.vote_team_id)) AS vote_team_id
	FROM (
		SELECT resolved.work_unit_id AS work_unit_id,
			if(resolved.evidence_ref = '', '', ifNull(nullIf(t.team_id, ''), '')) AS vote_team_id,
			uniqExactIf(resolved.resolved_wi_id, ` + evidenceVoteAttributedPredicate + `) AS cnt
		FROM (
			SELECT expanded.work_unit_id AS work_unit_id, expanded.evidence_ref AS evidence_ref,
				multiIf(
					NOT match(expanded.evidence_ref, '^[0-9a-fA-F-]{36}#pr[0-9]+$'), expanded.evidence_ref,
					evidence_repo.repo = '' OR evidence_repo.provider = '', '',
					concat(if(evidence_repo.provider = 'gitlab', 'gitlab:', 'ghpr:'), evidence_repo.repo,
						if(evidence_repo.provider = 'gitlab', '!', '#'), splitByString('#pr', expanded.evidence_ref)[2])
				) AS resolved_wi_id
			FROM (
				SELECT unit.work_unit_id AS work_unit_id, evidence_ref
				FROM (
					SELECT work_unit_id,
						arrayDistinct(arrayConcat(
							JSONExtract(structural_evidence_json, 'issues', 'Array(String)'),
							JSONExtract(structural_evidence_json, 'prs', 'Array(String)')
						)) AS evidence_refs
					FROM (
						SELECT work_unit_id,
							(argMax(tuple(repo_id), computed_at)).1 AS repo_id,
							argMax(structural_evidence_json, computed_at) AS structural_evidence_json
						FROM work_unit_investments
						WHERE org_id = {org_id:String}` + projectMixPinnedFilter + `
						GROUP BY work_unit_id
					)
					WHERE repo_id IS NULL
				) AS unit
				LEFT ARRAY JOIN unit.evidence_refs AS evidence_ref
			) AS expanded
			LEFT JOIN repo_lookup AS evidence_repo ON evidence_repo.repo_uuid = splitByString('#pr', expanded.evidence_ref)[1]
		) AS resolved
		LEFT JOIN wita AS t ON t.work_item_id = resolved.resolved_wi_id
		GROUP BY resolved.work_unit_id, vote_team_id
	) AS voted
	GROUP BY voted.work_unit_id
) AS u
INNER JOIN project_link AS l ON l.link_key = concat('t:', u.vote_team_id)
WHERE u.vote_team_id != ''
GROUP BY l.project_provider, l.project_id
)
ORDER BY project_key`)
}

// readProjectRollupMixRows runs the roll-up mix: phase 0, then the two arms. A
// project is a row only when its repo arm counted a work unit (the old
// statement's HAVING work_units > 0); the evidence arm only adds its count.
func readProjectRollupMixRows(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, ids []string, timeBound factTimeBound) ([]projectRollupMixRow, error) {
	scope, err := readProjectMixScope(ctx, client, orgID, ids, timeBound)
	if err != nil {
		return nil, err
	}
	if len(scope.unitIDs) == 0 {
		return nil, nil
	}
	linkJSON, err := readProjectMixLinks(ctx, client, orgID, ids, timeBound)
	if err != nil {
		return nil, err
	}
	projectMixBetweenPhases(ctx)
	extra := append(scope.bindings(), readers.Binding{Name: "link_json", Value: linkJSON})
	var rows []projectRollupMixRow
	if err := readers.QueryOrgScopedNamed(ctx, client, "ReadProjectThemeMix", projectRollupRepoThemesStatement(), orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var r projectRollupMixRow
		if scanErr := row.Scan(&r.ProjectKey, &r.FeatureDelivery, &r.Operational, &r.Maintenance, &r.Quality, &r.Risk, &r.WorkUnits, &r.Repos, &r.Teams); scanErr != nil {
			return scanErr
		}
		rows = append(rows, r)
		return nil
	}, extra...); err != nil {
		return nil, err
	}
	bugfix := map[string]float64{}
	if err := readers.QueryOrgScopedNamed(ctx, client, "ReadProjectThemeMixBugfix", projectRollupRepoBugfixStatement(), orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var key string
		var weighted float64
		if scanErr := row.Scan(&key, &weighted); scanErr != nil {
			return scanErr
		}
		bugfix[key] = weighted
		return nil
	}, extra...); err != nil {
		return nil, err
	}
	excluded := map[string]uint64{}
	if err := readers.QueryOrgScopedNamed(ctx, client, "ReadProjectThemeMixEvidenceArm", projectRollupEvidenceArmStatement(), orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var key string
		var n uint64
		if scanErr := row.Scan(&key, &n); scanErr != nil {
			return scanErr
		}
		excluded[key] = n
		return nil
	}, extra...); err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].ExcludedNoRepoLink = excluded[rows[i].ProjectKey]
		rows[i].BugfixWeighted = bugfix[rows[i].ProjectKey]
	}
	return rows, nil
}

// projectNativePlacementStatement returns ONE row of parallel arrays, one
// element per (project, work unit) placement: the units' issue refs joined to
// project membership and the project catalog, before the requested projects
// are selected. It reads structural_evidence_json and no map column.
func projectNativePlacementStatement() string {
	return `SELECT groupArray(work_unit_id), groupArray(project_provider), groupArray(project_id), groupArray(multi_placed) FROM (
    SELECT p.provider AS project_provider, p.id AS project_id, up.work_unit_id AS work_unit_id,
        max(up.multi_placed) AS multi_placed
    FROM ` + readers.ProjectIdentityCatalogSQL() + `
    INNER JOIN (
        SELECT ui.work_unit_id AS work_unit_id, ip.project_id AS project_id, ip.multi_placed AS multi_placed
        FROM (
            SELECT work_unit_id, issue_ref
            FROM (
                SELECT work_unit_id,
                    JSONExtract(structural_evidence_json, 'issues', 'Array(String)') AS issue_refs
                FROM (
                    SELECT work_unit_id,
                        argMax(structural_evidence_json, computed_at) AS structural_evidence_json
                    FROM work_unit_investments
                    WHERE org_id = {org_id:String}` + projectMixPinnedFilter + `
                    GROUP BY work_unit_id
                )
            )
            ARRAY JOIN issue_refs AS issue_ref
        ) AS ui
        INNER JOIN (
            SELECT work_item_id, project_id, toUInt8(repo_count > 1) AS multi_placed
            FROM (
                SELECT subject_id AS work_item_id, project_id,
                    uniqExact(repo_id) OVER (PARTITION BY subject_id) AS repo_count
                FROM project_membership_presence
                WHERE org_id = {org_id:String} AND subject_kind = 'work_item'
            )
        ) AS ip ON ip.work_item_id = ui.issue_ref
    ) AS up ON up.project_id = p.scope
    GROUP BY p.provider, p.id, up.work_unit_id
)`
}

// projectNativeThemeValuesStatement returns ONE row of parallel arrays, one
// element per work unit: effort and the five theme scalars. It reads the theme
// map only.
func projectNativeThemeValuesStatement() string {
	return `SELECT groupArray(work_unit_id), groupArray(effort_value),
    groupArray(theme_feature_delivery), groupArray(theme_operational), groupArray(theme_maintenance),
    groupArray(theme_quality), groupArray(theme_risk)
FROM (
    SELECT work_unit_id, effort_value,
        theme_distribution_json['` + contextfabric.ThemeFeatureDelivery + `'] AS theme_feature_delivery,
        theme_distribution_json['` + contextfabric.ThemeOperational + `'] AS theme_operational,
        theme_distribution_json['` + contextfabric.ThemeMaintenance + `'] AS theme_maintenance,
        theme_distribution_json['` + contextfabric.ThemeQuality + `'] AS theme_quality,
        theme_distribution_json['` + contextfabric.ThemeRisk + `'] AS theme_risk
    FROM (
        SELECT work_unit_id,
            argMax(effort_value, computed_at) AS effort_value,
            argMax(theme_distribution_json, computed_at) AS theme_distribution_json
        FROM work_unit_investments
        WHERE org_id = {org_id:String}` + projectMixPinnedFilter + `
        GROUP BY work_unit_id
    )
)`
}

// projectNativeBugfixValuesStatement returns ONE row of parallel arrays, one
// element per work unit: the bugfix share. It reads the subcategory map only.
func projectNativeBugfixValuesStatement() string {
	return `SELECT groupArray(work_unit_id), groupArray(bugfix_share)
FROM (
    SELECT work_unit_id, ifNull(subcategory_distribution_json[{bugfix_key:String}], 0.0) AS bugfix_share
    FROM (
        SELECT work_unit_id,
            argMax(subcategory_distribution_json, computed_at) AS subcategory_distribution_json
        FROM work_unit_investments
        WHERE org_id = {org_id:String}` + projectMixPinnedFilter + `
        GROUP BY work_unit_id
    )
)`
}

type projectNativeUnitValues struct {
	effort, feature, operational, maintenance, quality, risk, bugfix float64
}

// readProjectNativeMixRows runs the native mix: phase 0, the placement, the
// unit values, then the per-project aggregation the single statement did in
// SQL: a unit counts in full for every requested project it is placed in,
// spanning when it is placed in more than one project (requested or not).
func readProjectNativeMixRows(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, ids []string, timeBound factTimeBound, rowLimit int) ([]readers.ProjectThemeMixRow, error) {
	scope, err := readProjectMixScope(ctx, client, orgID, ids, timeBound)
	if err != nil {
		return nil, err
	}
	if len(scope.unitIDs) == 0 {
		return nil, nil
	}
	projectMixBetweenPhases(ctx)
	extra := scope.bindings()

	var pUnit, pProvider, pProject []string
	var pMulti []uint8
	if err := readers.QueryOrgScopedNamed(ctx, client, "ReadProjectThemeMixPlacement", projectNativePlacementStatement(), orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		return row.Scan(&pUnit, &pProvider, &pProject, &pMulti)
	}, extra...); err != nil {
		return nil, err
	}
	if len(pProvider) != len(pUnit) || len(pProject) != len(pUnit) || len(pMulti) != len(pUnit) {
		return nil, fmt.Errorf("project native mix placement arrays disagree: %d/%d/%d/%d", len(pUnit), len(pProvider), len(pProject), len(pMulti))
	}

	var vUnit []string
	var vEffort, vFeature, vOperational, vMaintenance, vQuality, vRisk []float64
	if err := readers.QueryOrgScopedNamed(ctx, client, "ReadProjectThemeMixThemeValues", projectNativeThemeValuesStatement(), orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		return row.Scan(&vUnit, &vEffort, &vFeature, &vOperational, &vMaintenance, &vQuality, &vRisk)
	}, extra...); err != nil {
		return nil, err
	}
	var bUnit []string
	var bShare []float64
	if err := readers.QueryOrgScopedNamed(ctx, client, "ReadProjectThemeMixBugfixValues", projectNativeBugfixValuesStatement(), orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		return row.Scan(&bUnit, &bShare)
	}, append(append([]readers.Binding{}, extra...), readers.Binding{Name: "bugfix_key", Value: readers.BugfixSubcategoryKey})...); err != nil {
		return nil, err
	}
	bugfix := make(map[string]float64, len(bUnit))
	for i, unit := range bUnit {
		bugfix[unit] = bShare[i]
	}
	values := make(map[string]projectNativeUnitValues, len(vUnit))
	for i, unit := range vUnit {
		values[unit] = projectNativeUnitValues{vEffort[i], vFeature[i], vOperational[i], vMaintenance[i], vQuality[i], vRisk[i], bugfix[unit]}
	}

	projectCount := make(map[string]int, len(pUnit))
	for _, unit := range pUnit {
		projectCount[unit]++
	}
	requested := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		requested[id] = struct{}{}
	}
	byProject := map[string]*readers.ProjectThemeMixRow{}
	for i, unit := range pUnit {
		key := pProvider[i] + ":" + pProject[i]
		if _, ok := requested[key]; !ok {
			continue
		}
		v, ok := values[unit]
		if !ok {
			continue
		}
		r := byProject[key]
		if r == nil {
			r = &readers.ProjectThemeMixRow{ProjectSubjectKey: key}
			byProject[key] = r
		}
		r.WorkUnits++
		if v.effort > 0 {
			r.FeatureDelivery += v.feature * v.effort
			r.Operational += v.operational * v.effort
			r.Maintenance += v.maintenance * v.effort
			r.Quality += v.quality * v.effort
			r.Risk += v.risk * v.effort
			r.BugfixWeighted += v.bugfix * v.effort
			r.EffortUnits++
		}
		if projectCount[unit] > 1 {
			r.SpanningUnits++
		}
		if pMulti[i] == 1 {
			r.MultiPlacedUnits++
		}
	}
	rows := make([]readers.ProjectThemeMixRow, 0, len(byProject))
	for _, r := range byProject {
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ProjectSubjectKey < rows[j].ProjectSubjectKey })
	if rowLimit > 0 && len(rows) > rowLimit {
		rows = rows[:rowLimit]
	}
	return rows, nil
}
