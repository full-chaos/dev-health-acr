package devhealthfacts

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// FlowProvider implements contextfabric.FactProvider for FactFlow
// (CHAOS-4364) -- delivery-flow / bottleneck signals genuinely computed by
// Dev Health Ops, never re-derived here (§19.6.3: Ops stays the authority
// for cycle/lead-time formulas):
//
//   - team reads work_item_metrics_daily (per team/work_scope_id/day:
//     items started/completed, WIP end-of-day, WIP age p50/p90, cycle/lead
//     time p50/p90, bug_completed_ratio, story_points_completed) -- the
//     same "latest row per partition, never stitched" row_number()
//     discipline metrics.go/health.go/workload.go already document, applied
//     here per (team_id, work_scope_id) exactly the way workload.go
//     partitions capacity_forecasts (a team can be measured under several
//     concurrent work_scope_id values).
//   - project rolls up through team_project_ownership -> the SAME
//     work_item_metrics_daily read, mirroring metrics.go's readProjectMetrics
//     rollup_basis pattern exactly: additive counts (items_started,
//     items_completed) are SUMMED across owning teams; WIP-age/cycle/lead
//     percentiles are NEVER averaged across teams of different sizes, and
//     instead ride in a disclosed per-team Rows breakdown.
//   - repository reads repo_metrics_daily's PR pickup/review-timing columns
//     (pr_pickup_time_p50_hours, pr_review_time_p50_hours,
//     pr_first_review_p50_hours/p90_hours, prs_with_first_review) -- a
//     DISTINCT shape under the SAME FactKind, the exact "second table, same
//     kind, different subject kind" precedent ci.go's ContinuousIntegrationProvider
//     already establishes for cicd_metrics_daily alongside ci_pipeline_runs.
//
// work_item_cycle_times' 003_flow_efficiency.sql columns (flow_efficiency,
// active_time_hours, wait_time_hours) are DELIBERATELY NOT read here.
// compute_work_items.py genuinely computes real values into
// WorkItemCycleTimeRecord, but the ClickHouse sink that writes
// work_item_cycle_times (ops' write_work_item_cycle_times) omits all three
// columns from its INSERT column list -- every row in the live table
// carries them at the DEFAULT 0 the migration set, not a computed value
// (codex review finding, CHAOS-4364 R1). Reading them here would publish a
// fabricated "0.0 flow efficiency" as a canonical fact for every team, the
// exact "stub data for a source with no honest canonical value" pattern
// doc.go's FactEvidence section forbids. Re-add once the Ops sink actually
// persists these columns (tracked as ops-side follow-up, out of this
// acr-only ticket's scope).
type FlowProvider struct{ facts clickhouseFacts }

func newFlowProvider(client contextpacket.ClickHouseQueryClient) *FlowProvider {
	return &FlowProvider{facts: clickhouseFacts{client: client}}
}

func (p *FlowProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactFlow, "devhealthfacts.flow", []contextfabric.SubjectKind{
		contextfabric.SubjectTeam, contextfabric.SubjectProject, contextfabric.SubjectRepository,
	})
}

func (p *FlowProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	facts := make([]contextfabric.CanonicalFact, 0, len(query.Subjects))
	truncated := false
	omittedRows := 0

	if teamSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectTeam); len(teamSubjects) > 0 {
		rowCount, rowsOmitted, scanErr := p.readTeamFlow(ctx, orgID, teamSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team flow", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery || rowsOmitted > 0
		omittedRows += rowsOmitted
	}

	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		rowCount, rowsOmitted, scanErr := p.readProjectFlow(ctx, orgID, projectSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project flow", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery || rowsOmitted > 0
		omittedRows += rowsOmitted
	}

	if repoSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectRepository); len(repoSubjects) > 0 {
		rowCount, scanErr := p.readRepositoryFlow(ctx, orgID, repoSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query repository flow", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: truncated, OmittedCount: omittedRows}, nil
}

// flowScopeRow is one (team_id, work_scope_id) partition's latest
// work_item_metrics_daily row -- never a stitched combination of several
// rows, the same row_number()-then-scan-one-row discipline every other
// provider in this package documents.
type flowScopeRow struct {
	workScopeID, day                               string
	itemsStarted, itemsCompleted, wipCountEndOfDay int64
	hasWipAgeP50                                   bool
	wipAgeP50Hours                                 float64
	hasWipAgeP90                                   bool
	wipAgeP90Hours                                 float64
	hasCycleP50                                    bool
	cycleP50Hours                                  float64
	hasCycleP90                                    bool
	cycleP90Hours                                  float64
	hasLeadP50                                     bool
	leadP50Hours                                   float64
	hasLeadP90                                     bool
	leadP90Hours                                   float64
	bugCompletedRatio, storyPointsCompleted        float64
}

func (r flowScopeRow) toFactValueRow() contextfabric.FactValueRow {
	fields := map[string]contextfabric.FactValue{
		"work_scope_id":          contextfabric.StringFactValue(r.workScopeID),
		"day":                    contextfabric.StringFactValue(r.day),
		"items_started":          contextfabric.IntegerFactValue(r.itemsStarted),
		"items_completed":        contextfabric.IntegerFactValue(r.itemsCompleted),
		"wip_count_end_of_day":   contextfabric.IntegerFactValue(r.wipCountEndOfDay),
		"bug_completed_ratio":    contextfabric.NumberFactValue(r.bugCompletedRatio),
		"story_points_completed": contextfabric.NumberFactValue(r.storyPointsCompleted),
	}
	if r.hasWipAgeP50 {
		fields["wip_age_p50_hours"] = contextfabric.NumberFactValue(r.wipAgeP50Hours)
	}
	if r.hasWipAgeP90 {
		fields["wip_age_p90_hours"] = contextfabric.NumberFactValue(r.wipAgeP90Hours)
	}
	if r.hasCycleP50 {
		fields["cycle_time_p50_hours"] = contextfabric.NumberFactValue(r.cycleP50Hours)
	}
	if r.hasCycleP90 {
		fields["cycle_time_p90_hours"] = contextfabric.NumberFactValue(r.cycleP90Hours)
	}
	if r.hasLeadP50 {
		fields["lead_time_p50_hours"] = contextfabric.NumberFactValue(r.leadP50Hours)
	}
	if r.hasLeadP90 {
		fields["lead_time_p90_hours"] = contextfabric.NumberFactValue(r.leadP90Hours)
	}
	return contextfabric.FactValueRow{Fields: fields}
}

// queryTeamScopeRows reads work_item_metrics_daily's latest row per
// (team_id, provider, work_scope_id) -- provider is part of this table's
// own primary key (devhealthschema's EngineFull ORDER BY), and different
// source providers CAN share a work_scope_id string (readiness.go's
// identical row_number() partition documents this same collision risk for
// estimate_coverage_metrics_daily) -- omitting it here would let one
// provider's row silently overwrite another's under the same partition
// (codex R2 P1, CHAOS-4364). Returned keyed by team_id in first-seen
// (query ORDER BY) scan order -- the same deterministic-ordering
// discipline metrics.go's readProjectMetrics documents (ruling invariant
// 8).
func (p *FlowProvider) queryTeamScopeRows(ctx context.Context, orgID string, ids []string, timeBound factTimeBound) (byTeam map[string][]flowScopeRow, teamOrder []string, rowCount int, err error) {
	// The hash tiebreak's ifNull(*, -1) sentinels are unambiguous while -1
	// sits outside each column's real domain: item counts and durations are
	// never negative in live data, mirroring metrics.go/health.go's
	// identical domain-assumption tiebreaks. The tuple covers every scanned
	// value column (codex R2 P2) -- a partial tuple lets two rows that
	// differ ONLY in an omitted column tie, picking an arbitrary one.
	statement := withRowLimit(`SELECT toString(team_id), toString(work_scope_id), toString(day), toInt64(items_started), toInt64(items_completed), toInt64(wip_count_end_of_day),
	toUInt8(isNotNull(wip_age_p50_hours)), toFloat64(ifNull(wip_age_p50_hours, 0)),
	toUInt8(isNotNull(wip_age_p90_hours)), toFloat64(ifNull(wip_age_p90_hours, 0)),
	toUInt8(isNotNull(cycle_time_p50_hours)), toFloat64(ifNull(cycle_time_p50_hours, 0)),
	toUInt8(isNotNull(cycle_time_p90_hours)), toFloat64(ifNull(cycle_time_p90_hours, 0)),
	toUInt8(isNotNull(lead_time_p50_hours)), toFloat64(ifNull(lead_time_p50_hours, 0)),
	toUInt8(isNotNull(lead_time_p90_hours)), toFloat64(ifNull(lead_time_p90_hours, 0)),
	toFloat64(bug_completed_ratio), toFloat64(story_points_completed)
FROM (
	SELECT team_id, work_scope_id, day, items_started, items_completed, wip_count_end_of_day, wip_age_p50_hours, wip_age_p90_hours, cycle_time_p50_hours, cycle_time_p90_hours, lead_time_p50_hours, lead_time_p90_hours, bug_completed_ratio, story_points_completed,
		row_number() OVER (PARTITION BY team_id, provider, work_scope_id ORDER BY day DESC, computed_at DESC, cityHash64(tuple(items_started, items_completed, wip_count_end_of_day, ifNull(wip_age_p50_hours, -1), ifNull(wip_age_p90_hours, -1), ifNull(cycle_time_p50_hours, -1), ifNull(cycle_time_p90_hours, -1), ifNull(lead_time_p50_hours, -1), ifNull(lead_time_p90_hours, -1), bug_completed_ratio, story_points_completed)) DESC) AS rn
	FROM work_item_metrics_daily
	WHERE org_id = {org_id:String} AND toString(team_id) IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
)
WHERE rn = 1
ORDER BY team_id, work_scope_id`)
	byTeam = make(map[string][]flowScopeRow)
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var teamID string
		var r flowScopeRow
		var hasWipP50, hasWipP90, hasCycleP50, hasCycleP90, hasLeadP50, hasLeadP90 uint8
		if scanErr := row.Scan(&teamID, &r.workScopeID, &r.day, &r.itemsStarted, &r.itemsCompleted, &r.wipCountEndOfDay,
			&hasWipP50, &r.wipAgeP50Hours, &hasWipP90, &r.wipAgeP90Hours,
			&hasCycleP50, &r.cycleP50Hours, &hasCycleP90, &r.cycleP90Hours,
			&hasLeadP50, &r.leadP50Hours, &hasLeadP90, &r.leadP90Hours,
			&r.bugCompletedRatio, &r.storyPointsCompleted); scanErr != nil {
			return scanErr
		}
		r.hasWipAgeP50, r.hasWipAgeP90 = hasWipP50 != 0, hasWipP90 != 0
		r.hasCycleP50, r.hasCycleP90 = hasCycleP50 != 0, hasCycleP90 != 0
		r.hasLeadP50, r.hasLeadP90 = hasLeadP50 != 0, hasLeadP90 != 0
		if _, seen := byTeam[teamID]; !seen {
			teamOrder = append(teamOrder, teamID)
		}
		byTeam[teamID] = append(byTeam[teamID], r)
		return nil
	}, timeBound.bindings()...)
	return byTeam, teamOrder, rowCount, scanErr
}

func (p *FlowProvider) readTeamFlow(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, int, error) {
	ids, bySubject := subjectIndex(subjects, teamPrefix)
	if len(ids) == 0 {
		return 0, 0, nil
	}
	byTeam, teamOrder, rowCount, err := p.queryTeamScopeRows(ctx, orgID, ids, timeBound)
	if err != nil {
		return rowCount, 0, err
	}
	totalOmitted := 0
	for _, teamID := range teamOrder {
		subject, ok := bySubject[teamID]
		if !ok {
			continue
		}
		rows := byTeam[teamID]
		var totalStarted, totalCompleted int64
		valueRows := make([]contextfabric.FactValueRow, 0, len(rows))
		for _, r := range rows {
			totalStarted += r.itemsStarted
			totalCompleted += r.itemsCompleted
			valueRows = append(valueRows, r.toFactValueRow())
		}
		valueRows, omitted := capFactValueRows(valueRows)
		totalOmitted += omitted
		fields := map[string]contextfabric.FactValue{
			"scope_count":     contextfabric.IntegerFactValue(int64(len(rows))),
			"items_started":   contextfabric.IntegerFactValue(totalStarted),
			"items_completed": contextfabric.IntegerFactValue(totalCompleted),
			"scope_breakdown": contextfabric.RowsFactValue(valueRows),
		}
		if omitted > 0 {
			fields["scope_breakdown_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactFlow, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("team", teamID)},
		})
	}
	return rowCount, totalOmitted, nil
}

// projectFlowRow is one owning team's contribution to a project's flow
// rollup -- the SAME "collect rows per project, aggregate Go-side" shape
// metrics.go's readProjectMetrics uses, adapted to read work_item_metrics_daily
// instead of team_metrics_daily. Unlike the team-level fact above (which
// discloses every concurrent work_scope_id a team has), the project rollup
// picks ONE representative (latest) scope per owning team -- the full
// per-scope breakdown for any one team is available by asking about that
// team subject directly.
type projectFlowRow struct {
	projectKey, teamID string
	row                teamFlowAggregateRow
}

// teamFlowAggregateRow is one team's SUMMED/AVERAGED work_item_metrics_daily
// aggregate across every (provider, work_scope_id) scope-row this package
// found for it (codex R2 P1, CHAOS-4364): readProjectFlow previously picked
// ONE arbitrary (provider, work_scope_id) row per team via row_number()
// PARTITION BY team_id alone, silently discarding every other concurrent
// scope for that team AND colliding across source providers -- work_scope_id
// is not provider-qualified, and readiness.go's own doc comment documents
// the exact same cross-provider work_scope_id collision this package must
// also guard against. Additive counts (items_started/items_completed/
// wip_count_end_of_day/story_points_completed) are SUMMED across the
// team's own scopes/providers; percentile/rate fields (wip_age/cycle/lead
// percentiles, bug_completed_ratio) are AVERAGED across them -- summing a
// percentile has no meaning. This never averages ACROSS TEAMS (each team
// keeps its own row in team_breakdown, per the package doc comment) --
// only within one team's own multiple source rows.
type teamFlowAggregateRow struct {
	itemsStarted, itemsCompleted, wipCountEndOfDay int64
	hasWipAgeP50                                   bool
	wipAgeP50Hours                                 float64
	hasWipAgeP90                                   bool
	wipAgeP90Hours                                 float64
	hasCycleP50                                    bool
	cycleP50Hours                                  float64
	hasCycleP90                                    bool
	cycleP90Hours                                  float64
	hasLeadP50                                     bool
	leadP50Hours                                   float64
	hasLeadP90                                     bool
	leadP90Hours                                   float64
	bugCompletedRatio, storyPointsCompleted        float64
}

func (r teamFlowAggregateRow) toFactValueRow() contextfabric.FactValueRow {
	fields := map[string]contextfabric.FactValue{
		"items_started":          contextfabric.IntegerFactValue(r.itemsStarted),
		"items_completed":        contextfabric.IntegerFactValue(r.itemsCompleted),
		"wip_count_end_of_day":   contextfabric.IntegerFactValue(r.wipCountEndOfDay),
		"bug_completed_ratio":    contextfabric.NumberFactValue(r.bugCompletedRatio),
		"story_points_completed": contextfabric.NumberFactValue(r.storyPointsCompleted),
	}
	if r.hasWipAgeP50 {
		fields["wip_age_p50_hours"] = contextfabric.NumberFactValue(r.wipAgeP50Hours)
	}
	if r.hasWipAgeP90 {
		fields["wip_age_p90_hours"] = contextfabric.NumberFactValue(r.wipAgeP90Hours)
	}
	if r.hasCycleP50 {
		fields["cycle_time_p50_hours"] = contextfabric.NumberFactValue(r.cycleP50Hours)
	}
	if r.hasCycleP90 {
		fields["cycle_time_p90_hours"] = contextfabric.NumberFactValue(r.cycleP90Hours)
	}
	if r.hasLeadP50 {
		fields["lead_time_p50_hours"] = contextfabric.NumberFactValue(r.leadP50Hours)
	}
	if r.hasLeadP90 {
		fields["lead_time_p90_hours"] = contextfabric.NumberFactValue(r.leadP90Hours)
	}
	return contextfabric.FactValueRow{Fields: fields}
}

func (p *FlowProvider) readProjectFlow(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, int, error) {
	ids, bySubject := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, 0, nil
	}
	// Ownership predicate and project_key resolution mirror metrics.go's
	// readProjectMetrics exactly -- see that function's doc comment for why
	// the join is on (provider, project_key), never project_id, and why an
	// ambiguous project_key is omitted rather than guessed.
	ownershipPredicate := " AND valid_from <= now64(3) AND valid_to IS NULL"
	if timeBound.active {
		ownershipPredicate = fmt.Sprintf(" AND valid_from <= {%s:DateTime64(6,'UTC')} AND (valid_to IS NULL OR valid_to > {%s:DateTime64(6,'UTC')})", boundEndParam, boundEndParam)
	}
	statement := withRowLimit(`SELECT concat(p.provider, ':', p.id), agg.team_id, toInt64(agg.items_started), toInt64(agg.items_completed), toInt64(agg.wip_count_end_of_day),
	toUInt8(isNotNull(agg.wip_age_p50_hours)), toFloat64(ifNull(agg.wip_age_p50_hours, 0)),
	toUInt8(isNotNull(agg.wip_age_p90_hours)), toFloat64(ifNull(agg.wip_age_p90_hours, 0)),
	toUInt8(isNotNull(agg.cycle_time_p50_hours)), toFloat64(ifNull(agg.cycle_time_p50_hours, 0)),
	toUInt8(isNotNull(agg.cycle_time_p90_hours)), toFloat64(ifNull(agg.cycle_time_p90_hours, 0)),
	toUInt8(isNotNull(agg.lead_time_p50_hours)), toFloat64(ifNull(agg.lead_time_p50_hours, 0)),
	toUInt8(isNotNull(agg.lead_time_p90_hours)), toFloat64(ifNull(agg.lead_time_p90_hours, 0)),
	toFloat64(agg.bug_completed_ratio), toFloat64(agg.story_points_completed)
FROM (
	SELECT id, provider, project_key
	FROM (
		SELECT id, provider, ifNull(project_key, '') AS project_key,
			count() OVER (PARTITION BY provider, project_key) AS key_resolution_count
		FROM projects FINAL
		WHERE org_id = {org_id:String}
	)
	WHERE project_key != '' AND key_resolution_count = 1 AND concat(provider, ':', id) IN {ids:Array(String)}
) AS p
INNER JOIN (
	SELECT provider, project_key, team_id
	FROM team_project_ownership FINAL
	WHERE org_id = {org_id:String} AND project_key IS NOT NULL` + ownershipPredicate + `
	GROUP BY provider, project_key, team_id
) AS tpo ON tpo.provider = p.provider AND tpo.project_key = p.project_key
INNER JOIN (
	-- One row per (team_id, provider, work_scope_id) triple, latest day
	-- first (same discipline queryTeamScopeRows uses), THEN aggregated to
	-- one row per team_id: additive counts summed, percentiles/ratio
	-- averaged. Never one arbitrary row per team (codex R2 P1) -- a team
	-- tracking several concurrent scopes, or two providers sharing a
	-- work_scope_id string, must contribute ALL of its rows, not just one.
	SELECT team_id,
		sum(items_started) AS items_started,
		sum(items_completed) AS items_completed,
		sum(wip_count_end_of_day) AS wip_count_end_of_day,
		avg(wip_age_p50_hours) AS wip_age_p50_hours,
		avg(wip_age_p90_hours) AS wip_age_p90_hours,
		avg(cycle_time_p50_hours) AS cycle_time_p50_hours,
		avg(cycle_time_p90_hours) AS cycle_time_p90_hours,
		avg(lead_time_p50_hours) AS lead_time_p50_hours,
		avg(lead_time_p90_hours) AS lead_time_p90_hours,
		avg(bug_completed_ratio) AS bug_completed_ratio,
		sum(story_points_completed) AS story_points_completed
	FROM (
		SELECT team_id, provider, work_scope_id, items_started, items_completed, wip_count_end_of_day, wip_age_p50_hours, wip_age_p90_hours, cycle_time_p50_hours, cycle_time_p90_hours, lead_time_p50_hours, lead_time_p90_hours, bug_completed_ratio, story_points_completed,
			row_number() OVER (PARTITION BY team_id, provider, work_scope_id ORDER BY day DESC, computed_at DESC, cityHash64(tuple(items_started, items_completed, wip_count_end_of_day, ifNull(wip_age_p50_hours, -1), ifNull(wip_age_p90_hours, -1), ifNull(cycle_time_p50_hours, -1), ifNull(cycle_time_p90_hours, -1), ifNull(lead_time_p50_hours, -1), ifNull(lead_time_p90_hours, -1), bug_completed_ratio, story_points_completed)) DESC) AS rn
		FROM work_item_metrics_daily
		WHERE org_id = {org_id:String}` + timeBound.dayPredicate("day") + `
	)
	WHERE rn = 1
	GROUP BY team_id
) AS agg ON agg.team_id = tpo.team_id
ORDER BY p.id, agg.team_id`)
	rowCount := 0
	byProject := make(map[string][]projectFlowRow)
	var projectOrder []string
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var projectSubjectKey, teamID string
		var r teamFlowAggregateRow
		var hasWipP50, hasWipP90, hasCycleP50, hasCycleP90, hasLeadP50, hasLeadP90 uint8
		if err := row.Scan(&projectSubjectKey, &teamID, &r.itemsStarted, &r.itemsCompleted, &r.wipCountEndOfDay,
			&hasWipP50, &r.wipAgeP50Hours, &hasWipP90, &r.wipAgeP90Hours,
			&hasCycleP50, &r.cycleP50Hours, &hasCycleP90, &r.cycleP90Hours,
			&hasLeadP50, &r.leadP50Hours, &hasLeadP90, &r.leadP90Hours,
			&r.bugCompletedRatio, &r.storyPointsCompleted); err != nil {
			return err
		}
		r.hasWipAgeP50, r.hasWipAgeP90 = hasWipP50 != 0, hasWipP90 != 0
		r.hasCycleP50, r.hasCycleP90 = hasCycleP50 != 0, hasCycleP90 != 0
		r.hasLeadP50, r.hasLeadP90 = hasLeadP50 != 0, hasLeadP90 != 0
		if _, ok := bySubject[projectSubjectKey]; !ok {
			return nil
		}
		if _, seen := byProject[projectSubjectKey]; !seen {
			projectOrder = append(projectOrder, projectSubjectKey)
		}
		byProject[projectSubjectKey] = append(byProject[projectSubjectKey], projectFlowRow{projectKey: projectSubjectKey, teamID: teamID, row: r})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return rowCount, 0, scanErr
	}
	totalOmitted := 0
	for _, projectKey := range projectOrder {
		rows := byProject[projectKey]
		subject := bySubject[projectKey]
		seenTeams := make(map[string]bool, len(rows))
		var totalStarted, totalCompleted int64
		teamRows := make([]contextfabric.FactValueRow, 0, len(rows))
		evidenceRefIDs := make([]string, 0, len(rows)+1)
		evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("project", projectKey))
		for _, r := range rows {
			if seenTeams[r.teamID] {
				continue
			}
			seenTeams[r.teamID] = true
			totalStarted += r.row.itemsStarted
			totalCompleted += r.row.itemsCompleted
			teamRow := r.row.toFactValueRow()
			teamRow.Fields["team_id"] = contextfabric.StringFactValue(r.teamID)
			teamRows = append(teamRows, teamRow)
			evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("team", r.teamID))
		}
		if len(teamRows) == 0 {
			continue
		}
		teamRows, omitted := capFactValueRows(teamRows)
		totalOmitted += omitted
		fields := map[string]contextfabric.FactValue{
			"rollup_basis":    contextfabric.StringFactValue("team_project_ownership_sum"),
			"team_count":      contextfabric.IntegerFactValue(int64(len(seenTeams))),
			"items_started":   contextfabric.IntegerFactValue(totalStarted),
			"items_completed": contextfabric.IntegerFactValue(totalCompleted),
			"team_breakdown":  contextfabric.RowsFactValue(teamRows),
		}
		if omitted > 0 {
			fields["team_breakdown_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactFlow, Subject: subject, Fields: fields,
			EvidenceRefIDs: evidenceRefIDs,
		})
	}
	return rowCount, totalOmitted, nil
}

// readRepositoryFlow reads repo_metrics_daily's PR pickup/review-timing
// columns -- CHAOS-4364's repository-scoped flow shape, the same
// "latest-day row_number()" discipline metrics.go's own repo_metrics_daily
// read (readRepositoryMetrics) documents, applied to a different column
// set from the SAME table (no conflict: two providers may each read their
// own columns off one row).
func (p *FlowProvider) readRepositoryFlow(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := subjectIndex(subjects, repositoryPrefix)
	if len(ids) == 0 {
		return 0, nil
	}
	statement := withRowLimit(`SELECT toString(repo_id), toString(day), toInt64(prs_merged), toInt64(prs_with_first_review),
	toUInt8(isNotNull(pr_pickup_time_p50_hours)), toFloat64(ifNull(pr_pickup_time_p50_hours, 0)),
	toUInt8(isNotNull(pr_review_time_p50_hours)), toFloat64(ifNull(pr_review_time_p50_hours, 0)),
	toUInt8(isNotNull(pr_first_review_p50_hours)), toFloat64(ifNull(pr_first_review_p50_hours, 0)),
	toUInt8(isNotNull(pr_first_review_p90_hours)), toFloat64(ifNull(pr_first_review_p90_hours, 0))
FROM (
	SELECT repo_id, day, prs_merged, prs_with_first_review, pr_pickup_time_p50_hours, pr_review_time_p50_hours, pr_first_review_p50_hours, pr_first_review_p90_hours,
		row_number() OVER (PARTITION BY repo_id ORDER BY day DESC, computed_at DESC, cityHash64(tuple(prs_merged, prs_with_first_review, ifNull(pr_pickup_time_p50_hours, -1), ifNull(pr_review_time_p50_hours, -1), ifNull(pr_first_review_p50_hours, -1), ifNull(pr_first_review_p90_hours, -1))) DESC) AS rn
	FROM repo_metrics_daily
	WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
)
WHERE rn = 1`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var repoID, day string
		var prsMerged, prsWithFirstReview int64
		var hasPickup, hasReview, hasFirstP50, hasFirstP90 uint8
		var pickup, review, firstP50, firstP90 float64
		if err := row.Scan(&repoID, &day, &prsMerged, &prsWithFirstReview,
			&hasPickup, &pickup, &hasReview, &review, &hasFirstP50, &firstP50, &hasFirstP90, &firstP90); err != nil {
			return err
		}
		subject, ok := bySubject[repoID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{
			"day":                   contextfabric.StringFactValue(day),
			"prs_merged":            contextfabric.IntegerFactValue(prsMerged),
			"prs_with_first_review": contextfabric.IntegerFactValue(prsWithFirstReview),
		}
		if hasPickup != 0 {
			fields["pr_pickup_time_p50_hours"] = contextfabric.NumberFactValue(pickup)
		}
		if hasReview != 0 {
			fields["pr_review_time_p50_hours"] = contextfabric.NumberFactValue(review)
		}
		if hasFirstP50 != 0 {
			fields["pr_first_review_p50_hours"] = contextfabric.NumberFactValue(firstP50)
		}
		if hasFirstP90 != 0 {
			fields["pr_first_review_p90_hours"] = contextfabric.NumberFactValue(firstP90)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactFlow, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("repository", repoID)},
		})
		return nil
	}, timeBound.bindings()...)
	return rowCount, scanErr
}
