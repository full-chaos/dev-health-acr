package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// CHAOS-7124: the project-native theme mix reads the same latest work-unit set
// ops' LatestWorkUnitInvestmentsSource reads (superseded units excluded, only
// the current membership run), like the repository/team mix (CHAOS-7073).
//
// The statement below is a copy of dev-health-go v0.12.0
// readers.ReadProjectThemeMixWithRowLimit (readers/investment_project_theme.go)
// with the two scope fragments from investment_membership_scope.go spliced into
// its `latest` CTE. The library reader does not scope, and changing it means a
// dev-health-go release plus a pin bump; until the library reader is retired
// (follow-up), keep this copy in step with it. It does not read repo_id, so the
// NULL-preserving argMax does not apply here. Row type, reader name, bindings
// and row limit are the library's.
func readProjectNativeThemeMixRows(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, ids []string, timeBound factTimeBound, rowLimit int) ([]readers.ProjectThemeMixRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return readProjectNativeMixRows(ctx, client, orgID, ids, timeBound, rowLimit)
}

// projectNativeMixStatement is the project-native mix, one pass over
// work_unit_investments (CHAOS-7257; the shape and the reason are the
// roll-up's, see projectRollupMixStatement). Innermost first, every stage is
// referenced exactly once:
//
//  1. latest + window: the one read of work_unit_investments.
//  2. per-unit derivation: effort, the five theme scalars and the bugfix share
//     are read out of the Map columns here and the maps go no further; the
//     unit's issue refs stay as an array.
//  3. attribution: each issue ref is joined to the work item's project
//     placement (project_membership_presence), then to the project catalog, and
//     collapsed to one row per (project, unit) carrying the unit's columns and
//     whether any placing item was multi-placed.
//  4. spanning: how many projects a unit reaches is a window function over
//     those rows (PARTITION BY work_unit_id), computed BEFORE the requested
//     projects are selected, so a unit that also reaches a project nobody asked
//     about still counts as spanning.
//  5. the requested projects are selected and the counts and effort-weighted
//     sums are aggregated.
//
// Result parity with the statement it replaces is pinned by
// chaos7257_project_native_parity_integration_test.go.
func projectNativeMixStatement(timeBound factTimeBound, rowLimit int) string {
	return readers.WithRowLimit(`
SELECT project_key, feature_delivery, operational, maintenance, quality, risk, bugfix_weighted, work_units, effort_units, spanning_units, multi_placed_units FROM (
SELECT
    concat(a.project_provider, ':', a.project_id) AS project_key,
    sumIf(a.theme_feature_delivery * a.effort_value, a.effort_value > 0) AS feature_delivery,
    sumIf(a.theme_operational * a.effort_value, a.effort_value > 0) AS operational,
    sumIf(a.theme_maintenance * a.effort_value, a.effort_value > 0) AS maintenance,
    sumIf(a.theme_quality * a.effort_value, a.effort_value > 0) AS quality,
    sumIf(a.theme_risk * a.effort_value, a.effort_value > 0) AS risk,
    sumIf(a.bugfix_share * a.effort_value, a.effort_value > 0) AS bugfix_weighted,
    count() AS work_units,
    countIf(a.effort_value > 0) AS effort_units,
    countIf(a.project_count > 1) AS spanning_units,
    countIf(a.multi_placed = 1) AS multi_placed_units
FROM (
    SELECT project_provider, project_id, work_unit_id, multi_placed, effort_value,
        theme_feature_delivery, theme_operational, theme_maintenance, theme_quality, theme_risk, bugfix_share,
        uniqExact(project_provider, project_id) OVER (PARTITION BY work_unit_id) AS project_count
    FROM (
        SELECT p.provider AS project_provider, p.id AS project_id, up.work_unit_id AS work_unit_id,
            max(up.multi_placed) AS multi_placed,
            any(up.effort_value) AS effort_value,
            any(up.theme_feature_delivery) AS theme_feature_delivery,
            any(up.theme_operational) AS theme_operational,
            any(up.theme_maintenance) AS theme_maintenance,
            any(up.theme_quality) AS theme_quality,
            any(up.theme_risk) AS theme_risk,
            any(up.bugfix_share) AS bugfix_share
        FROM `+readers.ProjectIdentityCatalogSQL()+`
        INNER JOIN (
            SELECT ui.work_unit_id AS work_unit_id, ip.project_id AS project_id, ip.multi_placed AS multi_placed,
                ui.effort_value AS effort_value,
                ui.theme_feature_delivery AS theme_feature_delivery, ui.theme_operational AS theme_operational,
                ui.theme_maintenance AS theme_maintenance, ui.theme_quality AS theme_quality, ui.theme_risk AS theme_risk,
                ui.bugfix_share AS bugfix_share
            FROM (
                SELECT unit.work_unit_id AS work_unit_id, unit.effort_value AS effort_value,
                    unit.theme_feature_delivery AS theme_feature_delivery, unit.theme_operational AS theme_operational,
                    unit.theme_maintenance AS theme_maintenance, unit.theme_quality AS theme_quality, unit.theme_risk AS theme_risk,
                    unit.bugfix_share AS bugfix_share, issue_ref
                FROM (
                    SELECT work_unit_id, effort_value,
                        theme_distribution_json['`+contextfabric.ThemeFeatureDelivery+`'] AS theme_feature_delivery,
                        theme_distribution_json['`+contextfabric.ThemeOperational+`'] AS theme_operational,
                        theme_distribution_json['`+contextfabric.ThemeMaintenance+`'] AS theme_maintenance,
                        theme_distribution_json['`+contextfabric.ThemeQuality+`'] AS theme_quality,
                        theme_distribution_json['`+contextfabric.ThemeRisk+`'] AS theme_risk,
                        ifNull(subcategory_distribution_json[{bugfix_key:String}], 0.0) AS bugfix_share,
                        JSONExtract(structural_evidence_json, 'issues', 'Array(String)') AS issue_refs
                    FROM (
                        SELECT
                            work_unit_id,
                            argMax(from_ts, computed_at) AS from_ts,
                            argMax(to_ts, computed_at) AS to_ts,
                            argMax(effort_value, computed_at) AS effort_value,
                            argMax(theme_distribution_json, computed_at) AS theme_distribution_json,
                            argMax(subcategory_distribution_json, computed_at) AS subcategory_distribution_json,
                            argMax(structural_evidence_json, computed_at) AS structural_evidence_json
                        FROM work_unit_investments
                        WHERE org_id = {org_id:String}`+supersededWorkUnitIDsFilter()+investmentMembershipScopeFilter()+`
                        GROUP BY work_unit_id
                    )
                    WHERE 1`+themeInvestmentRangePredicate(timeBound, "from_ts", "to_ts")+`
                ) AS unit
                ARRAY JOIN unit.issue_refs AS issue_ref
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
    )
) AS a
WHERE concat(a.project_provider, ':', a.project_id) IN {ids:Array(String)}
GROUP BY a.project_provider, a.project_id
ORDER BY project_key
)`, rowLimit)
}
