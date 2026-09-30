package devhealthfacts

import (
	"context"

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
	statement := readers.WithRowLimit(`
SELECT project_key, feature_delivery, operational, maintenance, quality, risk, bugfix_weighted, work_units, effort_units, spanning_units, multi_placed_units FROM (
WITH latest AS (
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
),
windowed AS (
    SELECT * FROM latest WHERE 1`+themeInvestmentRangePredicate(timeBound, "from_ts", "to_ts")+`
),
unit_issue AS (
    SELECT work_unit_id, issue_ref
    FROM windowed
    ARRAY JOIN JSONExtract(structural_evidence_json, 'issues', 'Array(String)') AS issue_ref
),
item_project AS (
    SELECT work_item_id, project_id, toUInt8(repo_count > 1) AS multi_placed
    FROM (
        SELECT subject_id AS work_item_id, project_id,
            uniqExact(repo_id) OVER (PARTITION BY subject_id) AS repo_count
        FROM project_membership_presence
        WHERE org_id = {org_id:String} AND subject_kind = 'work_item'
    )
),
unit_project AS (
    SELECT ui.work_unit_id AS work_unit_id, ip.project_id AS project_id, ip.multi_placed AS multi_placed
    FROM unit_issue AS ui
    INNER JOIN item_project AS ip ON ip.work_item_id = ui.issue_ref
),
resolved AS (
    SELECT project_provider, project_id, work_unit_id, max(multi_placed) AS multi_placed
    FROM (
        SELECT p.provider AS project_provider, p.id AS project_id, up.work_unit_id AS work_unit_id, up.multi_placed AS multi_placed
        FROM `+readers.ProjectIdentityCatalogSQL()+`
        INNER JOIN unit_project AS up ON up.project_id = p.scope
    )
    GROUP BY project_provider, project_id, work_unit_id
),
unit_span AS (
    SELECT work_unit_id, uniqExact(project_provider, project_id) AS project_count
    FROM resolved
    GROUP BY work_unit_id
),
attributed AS (
    SELECT project_provider, project_id, work_unit_id, multi_placed
    FROM resolved
    WHERE concat(project_provider, ':', project_id) IN {ids:Array(String)}
)
SELECT
    concat(a.project_provider, ':', a.project_id) AS project_key,
    sumIf(w.theme_distribution_json['feature_delivery'] * w.effort_value, w.effort_value > 0) AS feature_delivery,
    sumIf(w.theme_distribution_json['operational'] * w.effort_value, w.effort_value > 0) AS operational,
    sumIf(w.theme_distribution_json['maintenance'] * w.effort_value, w.effort_value > 0) AS maintenance,
    sumIf(w.theme_distribution_json['quality'] * w.effort_value, w.effort_value > 0) AS quality,
    sumIf(w.theme_distribution_json['risk'] * w.effort_value, w.effort_value > 0) AS risk,
    sumIf(ifNull(w.subcategory_distribution_json[{bugfix_key:String}], 0.0) * w.effort_value, w.effort_value > 0) AS bugfix_weighted,
    count() AS work_units,
    countIf(w.effort_value > 0) AS effort_units,
    countIf(s.project_count > 1) AS spanning_units,
    countIf(a.multi_placed = 1) AS multi_placed_units
FROM attributed AS a
INNER JOIN windowed AS w ON w.work_unit_id = a.work_unit_id
LEFT JOIN unit_span AS s ON s.work_unit_id = a.work_unit_id
GROUP BY a.project_provider, a.project_id
ORDER BY project_key
)`, rowLimit)

	extra := append(append([]readers.Binding{}, timeBound.neutral().Bindings()...), readers.Binding{Name: "bugfix_key", Value: readers.BugfixSubcategoryKey})
	var rows []readers.ProjectThemeMixRow
	err := readers.QueryOrgScopedNamed(ctx, client, "ReadProjectThemeMix", statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var r readers.ProjectThemeMixRow
		if scanErr := row.Scan(&r.ProjectSubjectKey, &r.FeatureDelivery, &r.Operational, &r.Maintenance, &r.Quality, &r.Risk, &r.BugfixWeighted, &r.WorkUnits, &r.EffortUnits, &r.SpanningUnits, &r.MultiPlacedUnits); scanErr != nil {
			return scanErr
		}
		rows = append(rows, r)
		return nil
	}, extra...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
