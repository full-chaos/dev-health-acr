package devhealthfacts

// CHAOS-7257 differential oracle. This file is compiled only into test
// binaries; its exported names are the seam the external integration tests
// (package devhealthfacts_test) use to run the statement text THIS change
// replaced against the one it introduced, on the same seeded ClickHouse.

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// ProjectMixWindow is the requested time bound: the zero value is the current
// axis, Active with HasStart a range, Active alone a point in time.
type ProjectMixWindow struct {
	Active   bool
	HasStart bool
	Start    time.Time
	End      time.Time
}

func (w ProjectMixWindow) bound() factTimeBound {
	return factTimeBound{active: w.Active, hasStart: w.HasStart, start: w.Start, end: w.End}
}

// Bindings are the time-bound parameters the two project mix statements take
// beside {org_id} and {ids}.
func (w ProjectMixWindow) Bindings() []readers.Binding {
	var out []readers.Binding
	for _, b := range w.bound().bindings() {
		out = append(out, readers.Binding{Name: b.Name, Value: b.Value})
	}
	return out
}

// ProjectRollupStatement is the statement readProjectThemeMix runs now.
func ProjectRollupStatement(w ProjectMixWindow) string { return projectRollupMixStatement(w.bound()) }

// ProjectNativeStatement is the statement readProjectNativeThemeMixRows runs now.
func ProjectNativeStatement(w ProjectMixWindow, rowLimit int) string {
	return projectNativeMixStatement(w.bound(), rowLimit)
}

// OracleProjectRollupStatement is the owning-team roll-up statement exactly as
// it stood at acr 1a1ef01e (readProjectThemeMix, investment.go): the
// multi-reference form that scanned work_unit_investments three times and hit
// ClickHouse Code 307 on prod. Kept ONLY as the parity oracle; nothing serves
// from it. Do not edit it to follow the live statement -- the day the two are
// the same text the parity test proves nothing.
func OracleProjectRollupStatement(w ProjectMixWindow) string {
	timeBound := w.bound()
	ownershipPredicate := ownershipValidityPredicate(timeBound)
	rangePredicate := themeInvestmentRangePredicate(timeBound, "from_ts", "to_ts")
	return withRowProbeLimit(`SELECT * FROM (
WITH latest AS (
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
),
windowed AS (
	SELECT * FROM latest WHERE 1` + rangePredicate + `
),
repo_linked AS (
	SELECT * FROM windowed WHERE repo_id IS NOT NULL
),
project_team AS (
	SELECT DISTINCT provider AS project_provider, id AS project_id, team_id
	FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
),
project_repo_team AS (
	SELECT DISTINCT pt.project_provider AS project_provider, pt.project_id AS project_id, toUUID(tro.repo_key) AS repo_id, pt.team_id AS team_id
	FROM project_team AS pt
	INNER JOIN ` + ownedRepositoriesSource(ownershipPredicate) + ` AS tro ON tro.team_id = pt.team_id
),
project_repo AS (
	SELECT DISTINCT project_provider, project_id, repo_id FROM project_repo_team
),
attributed AS (
	SELECT pr.project_provider AS project_provider, pr.project_id AS project_id, w.work_unit_id AS work_unit_id, w.repo_id AS repo_id, w.effort_value AS effort_value, w.theme_distribution_json AS theme_distribution_json, w.subcategory_distribution_json AS subcategory_distribution_json
	FROM repo_linked AS w
	INNER JOIN project_repo AS pr ON pr.repo_id = w.repo_id
),
per_theme AS (
	SELECT concat(project_provider, ':', project_id) AS project_key, theme_kv.1 AS theme, sum(theme_kv.2 * effort_value) AS weighted_effort
	FROM attributed
	ARRAY JOIN CAST(theme_distribution_json AS Array(Tuple(String, Float64))) AS theme_kv
	GROUP BY project_key, theme
),
per_project AS (
	SELECT concat(project_provider, ':', project_id) AS project_key,
		sum(ifNull(subcategory_distribution_json['` + readers.BugfixSubcategoryKey + `'], 0.0) * effort_value) AS bugfix_weighted,
		uniqExact(work_unit_id) AS work_units,
		uniqExact(repo_id) AS repos
	FROM attributed
	GROUP BY project_provider, project_id
),
contributing_repo AS (
	SELECT DISTINCT project_provider, project_id, repo_id FROM attributed
),
team_coverage AS (
	SELECT concat(prt.project_provider, ':', prt.project_id) AS project_key, uniqExact(prt.team_id) AS team_count
	FROM project_repo_team AS prt
	INNER JOIN contributing_repo AS cr ON cr.project_provider = prt.project_provider AND cr.project_id = prt.project_id AND cr.repo_id = prt.repo_id
	GROUP BY prt.project_provider, prt.project_id
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
	WHERE org_id = {org_id:String} AND is_primary = 1
	  AND (work_item_id, computed_at) IN (
		  SELECT work_item_id, max(computed_at)
		  FROM work_item_team_attributions
		  WHERE org_id = {org_id:String}
		  GROUP BY work_item_id
	  )
),
evidence_resolved AS (
	SELECT windowed.work_unit_id AS work_unit_id,
		multiIf(
			NOT match(evidence_ref, '^[0-9a-fA-F-]{36}#pr[0-9]+$'), evidence_ref,
			evidence_repo.repo = '' OR evidence_repo.provider = '', '',
			concat(if(evidence_repo.provider = 'gitlab', 'gitlab:', 'ghpr:'), evidence_repo.repo,
				if(evidence_repo.provider = 'gitlab', '!', '#'), splitByString('#pr', evidence_ref)[2])
		) AS resolved_wi_id
	FROM windowed
	ARRAY JOIN arrayDistinct(arrayConcat(
		JSONExtract(structural_evidence_json, 'issues', 'Array(String)'),
		JSONExtract(structural_evidence_json, 'prs', 'Array(String)')
	)) AS evidence_ref
	LEFT JOIN repo_lookup AS evidence_repo ON evidence_repo.repo_uuid = splitByString('#pr', evidence_ref)[1]
),
votes AS (
	SELECT work_unit_id, argMax(vote_team_id, (cnt, vote_team_id)) AS team_id
	FROM (
		SELECT evidence_resolved.work_unit_id AS work_unit_id,
			ifNull(nullIf(t.team_id, ''), '') AS vote_team_id,
			uniqExactIf(evidence_resolved.resolved_wi_id, ` + evidenceVoteAttributedPredicate + `) AS cnt
		FROM evidence_resolved
		LEFT JOIN wita AS t ON t.work_item_id = evidence_resolved.resolved_wi_id
		GROUP BY work_unit_id, vote_team_id
	)
	GROUP BY work_unit_id
),
project_evidence_attributed AS (
	SELECT DISTINCT pt.project_provider AS project_provider, pt.project_id AS project_id, votes.work_unit_id AS work_unit_id
	FROM votes
	INNER JOIN project_team AS pt ON pt.team_id = votes.team_id
	WHERE votes.team_id != ''
),
excluded_no_repo_link AS (
	SELECT concat(project_provider, ':', project_id) AS project_key, uniqExact(work_unit_id) AS excluded_count
	FROM (
		SELECT pea.project_provider AS project_provider, pea.project_id AS project_id, pea.work_unit_id AS work_unit_id
		FROM project_evidence_attributed AS pea
		INNER JOIN windowed AS w ON w.work_unit_id = pea.work_unit_id
		WHERE w.repo_id IS NULL
	)
	GROUP BY project_provider, project_id
)
SELECT
	pp.project_key AS project_key,
	sumIf(pt.weighted_effort, pt.theme = '` + contextfabric.ThemeFeatureDelivery + `') AS feature_delivery,
	sumIf(pt.weighted_effort, pt.theme = '` + contextfabric.ThemeOperational + `') AS operational,
	sumIf(pt.weighted_effort, pt.theme = '` + contextfabric.ThemeMaintenance + `') AS maintenance,
	sumIf(pt.weighted_effort, pt.theme = '` + contextfabric.ThemeQuality + `') AS quality,
	sumIf(pt.weighted_effort, pt.theme = '` + contextfabric.ThemeRisk + `') AS risk,
	any(pp.bugfix_weighted) AS bugfix_weighted,
	any(pp.work_units) AS work_units,
	any(pp.repos) AS repos,
	any(ifNull(tc.team_count, 0)) AS team_count,
	any(ifNull(excl.excluded_count, 0)) AS excluded_no_repo_link
FROM per_project AS pp
LEFT JOIN per_theme AS pt ON pt.project_key = pp.project_key
LEFT JOIN team_coverage AS tc ON tc.project_key = pp.project_key
LEFT JOIN excluded_no_repo_link AS excl ON excl.project_key = pp.project_key
GROUP BY pp.project_key
)
ORDER BY project_key`)
}

// OracleProjectNativeStatement is the project-native mix statement exactly as
// it stood at acr 1a1ef01e (readProjectNativeThemeMixRows,
// investment_project_native_mix.go, added by CHAOS-7124): the CTE chain that
// referenced `windowed` twice and `resolved` twice, so it scanned
// work_unit_investments three times. Kept ONLY as the parity oracle.
func OracleProjectNativeStatement(w ProjectMixWindow, rowLimit int) string {
	timeBound := w.bound()
	return readers.WithRowLimit(`
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
}

// CHAOS-7271: the phased production path, exported for the differential tests.

// ProjectRollupMixRow is one row of the phased roll-up read.
type ProjectRollupMixRow = projectRollupMixRow

// RunProjectRollupMix runs the roll-up mix exactly as readProjectThemeMix does.
func RunProjectRollupMix(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, ids []string, w ProjectMixWindow) ([]ProjectRollupMixRow, error) {
	return readProjectRollupMixRows(ctx, client, orgID, ids, w.bound(), nil)
}

// RunProjectNativeMix runs the native mix exactly as readProjectNativeThemeMix does.
func RunProjectNativeMix(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, ids []string, w ProjectMixWindow, rowLimit int) ([]readers.ProjectThemeMixRow, error) {
	return readProjectNativeThemeMixRows(ctx, client, orgID, ids, w.bound(), rowLimit, nil)
}

// WithProjectMixBetweenPhases returns a context whose hook runs after phase 0 of
// a project mix read and before its next phase: the instant a writer can land a
// version the read must not see.
func WithProjectMixBetweenPhases(ctx context.Context, hook func()) context.Context {
	return context.WithValue(ctx, projectMixBetweenPhasesKey{}, hook)
}

// WithProjectMixAfterBaseline returns a context whose hook runs right after a
// project mix read took its baseline input digests and before it reads anything
// else: the instant a write must be caught by the closing digests.
func WithProjectMixAfterBaseline(ctx context.Context, hook func()) context.Context {
	return context.WithValue(ctx, projectMixAfterBaselineKey{}, hook)
}

// WithProjectMixAfterScope returns a context whose hook runs right after a project
// mix read's scope statement and before its next read (the roll-up's link capture).
func WithProjectMixAfterScope(ctx context.Context, hook func()) context.Context {
	return context.WithValue(ctx, projectMixAfterScopeKey{}, hook)
}
