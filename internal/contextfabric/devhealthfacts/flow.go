package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
	capability := newCapability(contextfabric.FactFlow, "devhealthfacts.flow", []contextfabric.SubjectKind{
		contextfabric.SubjectTeam, contextfabric.SubjectProject, contextfabric.SubjectRepository,
	})
	// CHAOS-4633: team's scope_breakdown is a breakdown (Key = [provider,
	// work_scope_id], per CHAOS-4364/this design's own worked example --
	// never a time_series, which is exactly CHAOS-4616's fix). Project's
	// team_breakdown is a breakdown too.
	// CHAOS-4645, design doc §5.2: team and project also gain a
	// time_series (daily_flow) alongside their existing breakdown.
	capability.Tables = map[contextfabric.SubjectKind][]contextfabric.FactTableShape{
		contextfabric.SubjectTeam:    {contextfabric.FactTableBreakdown, contextfabric.FactTableTimeSeries},
		contextfabric.SubjectProject: {contextfabric.FactTableBreakdown, contextfabric.FactTableTimeSeries},
	}
	capability.EstimatedItems = 20
	return capability
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

// flowScopeRow is one (team_id, provider, work_scope_id) partition's latest
// work_item_metrics_daily row -- never a stitched combination of several
// rows, the same row_number()-then-scan-one-row discipline every other
// provider in this package documents. provider is carried through to
// disclosure (codex R3 P2, CHAOS-4364): two DIFFERENT providers legitimately
// sharing a work_scope_id string now both survive as distinct rows (the
// exact collision queryTeamScopeRows' own doc comment describes), so a
// scope_breakdown row with no provider field would be ambiguous about which
// source it came from.
type flowScopeRow struct {
	provider, workScopeID, day                     string
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
		"provider":               contextfabric.StringFactValue(r.provider),
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

// flowDailyRow is one (team_id, day)'s SUMMED work_item_metrics_daily
// aggregate across every (provider, work_scope_id) scope the team had that
// day (CHAOS-4645, design doc §5.2). Additive counts are summed exactly the
// way readProjectFlow already sums a team's own scopes; percentile
// columns (wip_age/cycle/lead) are DELIBERATELY DROPPED from the daily
// series -- summing or averaging a percentile across concurrent scopes has
// no defined meaning, the same rule teamFlowAggregateRow's own doc comment
// states for the project rollup. bug_completed_ratio is averaged across
// the day's scopes as a representative value (a ratio of ratios has no
// single correct combination either, but an average is a defensible
// approximation for a value that is itself bounded [0,1], unlike a raw
// percentile).
type flowDailyRow struct {
	teamID, day                                    string
	itemsStarted, itemsCompleted, wipCountEndOfDay int64
	bugCompletedRatioAvg, storyPointsCompletedSum  float64
}

func (r flowDailyRow) toFactValueRow() contextfabric.FactValueRow {
	return contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
		"day":                    contextfabric.StringFactValue(r.day),
		"items_started":          contextfabric.IntegerFactValue(r.itemsStarted),
		"items_completed":        contextfabric.IntegerFactValue(r.itemsCompleted),
		"wip_count_end_of_day":   contextfabric.IntegerFactValue(r.wipCountEndOfDay),
		"bug_completed_ratio":    contextfabric.NumberFactValue(r.bugCompletedRatioAvg),
		"story_points_completed": contextfabric.NumberFactValue(r.storyPointsCompletedSum),
	}}
}

// queryTeamFlowDailySeries reads work_item_metrics_daily as a genuine
// per-day series (CHAOS-4645, design doc §5.2: "the dated rows already
// exist in the ClickHouse daily tables these producers read; what is
// missing is a second, declared projection of them") -- unlike
// queryTeamScopeRows, which collapses to the single latest row per
// (team_id, provider, work_scope_id) and therefore can never back a
// time_series. The inner row_number() here dedupes only a SAME-DAY rerun
// per (team_id, provider, work_scope_id, day) -- mirroring the tiebreak
// discipline queryTeamScopeRows already documents -- and the outer
// GROUP BY sums each day's scopes into one row per (team_id, day), so a
// multi-scope team still yields exactly one time_series point per day.
func (p *FlowProvider) queryTeamFlowDailySeries(ctx context.Context, orgID string, ids []string, timeBound factTimeBound) (byTeam map[string][]flowDailyRow, err error) {
	statement := withRowLimit(`SELECT toString(team_id), toString(day), toInt64(sum(items_started)), toInt64(sum(items_completed)), toInt64(sum(wip_count_end_of_day)), avg(bug_completed_ratio), toFloat64(sum(story_points_completed))
FROM (
	SELECT team_id, provider, work_scope_id, day, items_started, items_completed, wip_count_end_of_day, bug_completed_ratio, story_points_completed,
		row_number() OVER (PARTITION BY team_id, provider, work_scope_id, day ORDER BY computed_at DESC, cityHash64(tuple(items_started, items_completed, wip_count_end_of_day, bug_completed_ratio, story_points_completed)) DESC) AS rn
	FROM work_item_metrics_daily
	WHERE org_id = {org_id:String} AND toString(team_id) IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
)
WHERE rn = 1
GROUP BY team_id, day
ORDER BY team_id, day DESC`)
	byTeam = make(map[string][]flowDailyRow)
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var r flowDailyRow
		var teamID string
		if err := row.Scan(&teamID, &r.day, &r.itemsStarted, &r.itemsCompleted, &r.wipCountEndOfDay, &r.bugCompletedRatioAvg, &r.storyPointsCompletedSum); err != nil {
			return err
		}
		r.teamID = teamID
		byTeam[teamID] = append(byTeam[teamID], r)
		return nil
	}, timeBound.bindings()...)
	return byTeam, scanErr
}

// flowDailyTable builds the CHAOS-4645 time_series FactTable off rows
// already fetched by queryTeamFlowDailySeries (team) or the project-level
// per-day rollup (readProjectFlowDailySeries) -- both share this exact
// shape, so a single declaration serves both subject kinds.
func flowDailyTable(rows []flowDailyRow, grain contextfabric.TemporalGrain) (contextfabric.FactValue, bool, int) {
	if len(rows) == 0 {
		return contextfabric.FactValue{}, false, 0
	}
	valueRows := make([]contextfabric.FactValueRow, 0, len(rows))
	for _, r := range rows {
		valueRows = append(valueRows, r.toFactValueRow())
	}
	valueRows, omitted := capFactValueRows(valueRows)
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:    contextfabric.FactTableTimeSeries,
		Key:      []string{"day"},
		Measures: []string{"items_started", "items_completed", "wip_count_end_of_day", "bug_completed_ratio", "story_points_completed"},
		Grain:    grain,
		Rows:     valueRows,
	}), true, omitted
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
	statement := withRowLimit(`SELECT toString(team_id), toString(provider), toString(work_scope_id), toString(day), toInt64(items_started), toInt64(items_completed), toInt64(wip_count_end_of_day),
	toUInt8(isNotNull(wip_age_p50_hours)), toFloat64(ifNull(wip_age_p50_hours, 0)),
	toUInt8(isNotNull(wip_age_p90_hours)), toFloat64(ifNull(wip_age_p90_hours, 0)),
	toUInt8(isNotNull(cycle_time_p50_hours)), toFloat64(ifNull(cycle_time_p50_hours, 0)),
	toUInt8(isNotNull(cycle_time_p90_hours)), toFloat64(ifNull(cycle_time_p90_hours, 0)),
	toUInt8(isNotNull(lead_time_p50_hours)), toFloat64(ifNull(lead_time_p50_hours, 0)),
	toUInt8(isNotNull(lead_time_p90_hours)), toFloat64(ifNull(lead_time_p90_hours, 0)),
	toFloat64(bug_completed_ratio), toFloat64(story_points_completed)
FROM (
	SELECT team_id, provider, work_scope_id, day, items_started, items_completed, wip_count_end_of_day, wip_age_p50_hours, wip_age_p90_hours, cycle_time_p50_hours, cycle_time_p90_hours, lead_time_p50_hours, lead_time_p90_hours, bug_completed_ratio, story_points_completed,
		row_number() OVER (PARTITION BY team_id, provider, work_scope_id ORDER BY day DESC, computed_at DESC, cityHash64(tuple(items_started, items_completed, wip_count_end_of_day, ifNull(wip_age_p50_hours, -1), ifNull(wip_age_p90_hours, -1), ifNull(cycle_time_p50_hours, -1), ifNull(cycle_time_p90_hours, -1), ifNull(lead_time_p50_hours, -1), ifNull(lead_time_p90_hours, -1), bug_completed_ratio, story_points_completed)) DESC) AS rn
	FROM work_item_metrics_daily
	WHERE org_id = {org_id:String} AND toString(team_id) IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
)
WHERE rn = 1
ORDER BY team_id, work_scope_id, provider`)
	byTeam = make(map[string][]flowScopeRow)
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var teamID string
		var r flowScopeRow
		var hasWipP50, hasWipP90, hasCycleP50, hasCycleP90, hasLeadP50, hasLeadP90 uint8
		if scanErr := row.Scan(&teamID, &r.provider, &r.workScopeID, &r.day, &r.itemsStarted, &r.itemsCompleted, &r.wipCountEndOfDay,
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
	// CHAOS-4645, design doc §5.2: a second, ADDITIVE query for the dated
	// series -- the scalar fields below are computed byte-identically to
	// before this ticket, off the SAME byTeam/queryTeamScopeRows result;
	// dailyByTeam only ever adds a new "daily_flow" field, never changes an
	// existing one, so nothing that already reads FactFlow's scalars (flow
	// is not one of cohort_ranking.go's five RankingSignal* families, so
	// there is no RankCohort input to pin here) can observe a difference.
	dailyByTeam, seriesErr := p.queryTeamFlowDailySeries(ctx, orgID, ids, timeBound)
	if seriesErr != nil {
		return rowCount, 0, seriesErr
	}
	// codex CHAOS-4645 round-1 P2 (EXECUTED): the daily-series query carries
	// its OWN withRowLimit(200) cap, shared across every requested team in
	// ONE query -- distinct from queryTeamScopeRows' own rowCount above. Five
	// teams x 50 days each can silently drop an entire team's daily_flow rows
	// under the shared LIMIT while the legacy scope_breakdown read (~1
	// row/team) never comes close to it, so Truncated must also reflect this
	// query hitting its own cap, not only the legacy one.
	dailySeriesRowCount := 0
	for _, rows := range dailyByTeam {
		dailySeriesRowCount += len(rows)
	}
	if dailySeriesRowCount > rowCount {
		rowCount = dailySeriesRowCount
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
			// CHAOS-4633 P1: Key = [provider, work_scope_id] -- NEVER
			// team_id. The design's own canonical citation: two different
			// providers can legitimately share one work_scope_id string
			// (queryTeamScopeRows' doc comment), so provider is part of
			// row identity; team_id is this fact's Subject, never a row
			// column (toFactValueRow never emits one).
			"scope_breakdown": contextfabric.TableFactValue(contextfabric.FactTable{
				Shape: contextfabric.FactTableBreakdown,
				Key:   []string{"provider", "work_scope_id"},
				// day is a StringFactValue as-of date, not a quantity
				// (CHAOS-4680): an informational sibling column on each
				// scope's row, not something this table measures.
				Measures: []string{
					"items_started", "items_completed", "wip_count_end_of_day",
					"bug_completed_ratio", "story_points_completed",
					"wip_age_p50_hours", "wip_age_p90_hours",
					"cycle_time_p50_hours", "cycle_time_p90_hours",
					"lead_time_p50_hours", "lead_time_p90_hours",
				},
				Observations: []string{"day"},
				Grain:        timeBound.effectiveGrain(grainDaily),
				Rows:         valueRows,
			}),
		}
		if omitted > 0 {
			fields["scope_breakdown_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
		}
		if dailyTable, ok, dailyOmitted := flowDailyTable(dailyByTeam[teamID], timeBound.effectiveGrain(grainDaily)); ok {
			// CHAOS-4785: never hand the write-path validator a dual-table
			// fact it must reject outright -- check the SAME joint bound
			// here and drop the additive time series instead, DISCLOSED:
			// the drop is folded into totalOmitted (which flows to
			// FactProviderResult.Truncated/OmittedCount, degrading served
			// coverage exactly as capFactValueRows' own row-cap truncation
			// already does) and named on the fact with a closed reason.
			if drop, dropped, reason := disclosedDualTableDrop("flow", contextfabric.FactFlow, valueRows, dailyTable.Rows); drop {
				totalOmitted += dropped
				fields["daily_flow_omitted_count"] = contextfabric.IntegerFactValue(int64(dropped))
				fields["daily_flow_omitted_reason"] = contextfabric.StringFactValue(reason)
			} else {
				fields["daily_flow"] = dailyTable
				totalOmitted += dailyOmitted
				if dailyOmitted > 0 {
					fields["daily_flow_omitted_count"] = contextfabric.IntegerFactValue(int64(dailyOmitted))
				}
			}
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactFlow, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, teamID)},
		})
	}
	return rowCount, totalOmitted, nil
}

// projectFlowRow is one owning team's contribution to a project's flow
// rollup -- the SAME "collect rows per project, aggregate Go-side" shape
// metrics.go's readProjectMetrics uses, adapted to read work_item_metrics_daily
// instead of team_metrics_daily. Unlike the team-level fact above (which
// discloses every concurrent work_scope_id a team has), the project rollup
// does NOT pick one representative scope per team (codex R2 P1, CHAOS-4364:
// that WAS the bug -- see teamFlowAggregateRow's own doc comment): its row
// SQL-aggregates (SUM additive counts, AVG percentiles) across every
// (provider, work_scope_id) row the team has, so team_breakdown's one row
// per team already reflects the team's full flow picture. The per-scope
// breakdown for any one team is still available by asking about that team
// subject directly.
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
	// CHAOS-4521b: the project's OWN work_item_metrics_daily rows, matched
	// on work_scope_id, with NO team-ownership hop.
	//
	// The ownership form was wrong twice over, both observed on live data
	// (org 70d529e0, 2026-08-29). It could not reach a real project at all
	// -- the join keyed on projects.project_key, which is NULL for every
	// real Linear project (CHAOS-4530). And when it DID resolve, it
	// aggregated by team_id across every work scope that team touched, so
	// a project's flow was assembled from other projects' rows.
	//
	// work_scope_id IS work_items.project_id -- dev-health-ops' own oracle
	// asserts it (github_work_item_derived_surfaces_oracle_test.go: "same
	// work_scope_id (project_id)") -- so the project's rows were one hop
	// away from where this query was looking.
	//
	// The team dimension SURVIVES, and still means what the fact's
	// per-team breakdown says it means: the aggregation is now "within this
	// project's work scopes, grouped by the team that produced each row".
	// That is a narrower and honest team list, not a different concept.
	// The aggregation moved into this SELECT with the GROUP BY: additive
	// counts summed, percentiles and the ratio averaged -- byte-for-byte
	// the same rule the old inner `agg` subquery applied, over the rows
	// that now actually belong to this project.
	statement := withRowLimit(`SELECT concat(p.provider, ':', p.id), wm.team_id, toInt64(sum(wm.items_started)), toInt64(sum(wm.items_completed)), toInt64(sum(wm.wip_count_end_of_day)),
	toUInt8(isNotNull(avg(wm.wip_age_p50_hours))), toFloat64(ifNull(avg(wm.wip_age_p50_hours), 0)),
	toUInt8(isNotNull(avg(wm.wip_age_p90_hours))), toFloat64(ifNull(avg(wm.wip_age_p90_hours), 0)),
	toUInt8(isNotNull(avg(wm.cycle_time_p50_hours))), toFloat64(ifNull(avg(wm.cycle_time_p50_hours), 0)),
	toUInt8(isNotNull(avg(wm.cycle_time_p90_hours))), toFloat64(ifNull(avg(wm.cycle_time_p90_hours), 0)),
	toUInt8(isNotNull(avg(wm.lead_time_p50_hours))), toFloat64(ifNull(avg(wm.lead_time_p50_hours), 0)),
	toUInt8(isNotNull(avg(wm.lead_time_p90_hours))), toFloat64(ifNull(avg(wm.lead_time_p90_hours), 0)),
	toFloat64(avg(wm.bug_completed_ratio)), toFloat64(sum(wm.story_points_completed))
FROM ` + projectIdentityJoinSQL() + `
INNER JOIN (
	-- One row per (team_id, provider, work_scope_id) triple, latest day
	-- first (same discipline queryTeamScopeRows uses). The aggregation to
	-- one row per team happens OUTSIDE this subquery now, because which
	-- rows belong to the answer depends on the project each is matched
	-- against -- a fact this subquery cannot see.
	SELECT team_id, provider, work_scope_id, items_started, items_completed, wip_count_end_of_day, wip_age_p50_hours, wip_age_p90_hours, cycle_time_p50_hours, cycle_time_p90_hours, lead_time_p50_hours, lead_time_p90_hours, bug_completed_ratio, story_points_completed,
		row_number() OVER (PARTITION BY team_id, provider, work_scope_id ORDER BY day DESC, computed_at DESC, cityHash64(tuple(items_started, items_completed, wip_count_end_of_day, ifNull(wip_age_p50_hours, -1), ifNull(wip_age_p90_hours, -1), ifNull(cycle_time_p50_hours, -1), ifNull(cycle_time_p90_hours, -1), ifNull(lead_time_p50_hours, -1), ifNull(lead_time_p90_hours, -1), bug_completed_ratio, story_points_completed)) DESC) AS rn
	FROM work_item_metrics_daily
	WHERE org_id = {org_id:String}` + timeBound.dayPredicate("day") + `
) AS wm ON ` + projectIdentityMatchSQL("wm", "work_scope_id") + ` AND wm.rn = 1
GROUP BY p.provider, p.id, wm.team_id
ORDER BY p.id, wm.team_id`)
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
	// CHAOS-4645: additive, off the SAME work-scope join, never changing
	// an existing field -- flow carries no RankCohort signal, so there is
	// nothing to pin here (see readTeamFlow's identical note).
	dailyByProject, seriesErr := p.queryProjectFlowDailySeries(ctx, orgID, ids, timeBound)
	if seriesErr != nil {
		return rowCount, 0, seriesErr
	}
	// codex CHAOS-4645 round-1 P2 (EXECUTED): see readTeamFlow's identical
	// note -- the daily-series query's own withRowLimit(200) cap, shared
	// across every requested project in one query, must also surface as
	// Truncated.
	dailySeriesRowCount := 0
	for _, rows := range dailyByProject {
		dailySeriesRowCount += len(rows)
	}
	if dailySeriesRowCount > rowCount {
		rowCount = dailySeriesRowCount
	}
	totalOmitted := 0
	for _, projectKey := range projectOrder {
		rows := byProject[projectKey]
		subject := bySubject[projectKey]
		seenTeams := make(map[string]bool, len(rows))
		var totalStarted, totalCompleted int64
		teamRows := make([]contextfabric.FactValueRow, 0, len(rows))
		evidenceRefIDs := make([]string, 0, len(rows)+1)
		evidenceRefIDs = append(evidenceRefIDs, evidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, projectKey))
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
			evidenceRefIDs = append(evidenceRefIDs, evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, r.teamID))
		}
		if len(teamRows) == 0 {
			continue
		}
		teamRows, omitted := capFactValueRows(teamRows)
		totalOmitted += omitted
		fields := map[string]contextfabric.FactValue{
			"rollup_basis": contextfabric.StringFactValue("project_work_scope_sum"),
			// CHAOS-4521b, codex P2: renamed with the join. rollup_basis
			// reaches canonical claimed facts and synthesis, so an answer
			// could report an ownership derivation that no longer
			// happens -- provenance describing a chain the read did not
			// traverse. This path groups the project's OWN work-scope
			// rows; no ownership edge is consulted.
			"team_count":      contextfabric.IntegerFactValue(int64(len(seenTeams))),
			"items_started":   contextfabric.IntegerFactValue(totalStarted),
			"items_completed": contextfabric.IntegerFactValue(totalCompleted),
			// CHAOS-4633 P1: Key = [team_id] -- one row per team
			// (seenTeams dedupe above), each already SUMMED/AVERAGED
			// across that team's own scopes (teamFlowAggregateRow's own
			// doc comment).
			"team_breakdown": contextfabric.TableFactValue(contextfabric.FactTable{
				Shape: contextfabric.FactTableBreakdown,
				Key:   []string{"team_id"},
				Measures: []string{
					"items_started", "items_completed", "wip_count_end_of_day",
					"bug_completed_ratio", "story_points_completed",
					"wip_age_p50_hours", "wip_age_p90_hours",
					"cycle_time_p50_hours", "cycle_time_p90_hours",
					"lead_time_p50_hours", "lead_time_p90_hours",
				},
				Grain: timeBound.effectiveGrain(grainDaily),
				Rows:  teamRows,
			}),
		}
		if omitted > 0 {
			fields["team_breakdown_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
		}
		if dailyTable, ok, dailyOmitted := flowDailyTable(dailyByProject[projectKey], timeBound.effectiveGrain(grainDaily)); ok {
			// CHAOS-4785: see the matching note in readTeamFlow.
			if drop, dropped, reason := disclosedDualTableDrop("flow", contextfabric.FactFlow, teamRows, dailyTable.Rows); drop {
				totalOmitted += dropped
				fields["daily_flow_omitted_count"] = contextfabric.IntegerFactValue(int64(dropped))
				fields["daily_flow_omitted_reason"] = contextfabric.StringFactValue(reason)
			} else {
				fields["daily_flow"] = dailyTable
				totalOmitted += dailyOmitted
				if dailyOmitted > 0 {
					fields["daily_flow_omitted_count"] = contextfabric.IntegerFactValue(int64(dailyOmitted))
				}
			}
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactFlow, Subject: subject, Fields: fields,
			EvidenceRefIDs: evidenceRefIDs,
		})
	}
	return rowCount, totalOmitted, nil
}

// queryProjectFlowDailySeries is queryTeamFlowDailySeries' project-rollup
// counterpart (CHAOS-4645, design doc §5.2): the project's OWN
// work_item_metrics_daily rows, matched on work_scope_id with no
// team-ownership hop (the SAME CHAOS-4521b join readProjectFlow's own doc
// comment explains), summed across every contributing team FOR EACH DAY --
// additive counts only, matching readProjectFlow's own "additive counts
// summed, percentiles dropped" rule for the analogous team_breakdown.
func (p *FlowProvider) queryProjectFlowDailySeries(ctx context.Context, orgID string, ids []string, timeBound factTimeBound) (byProject map[string][]flowDailyRow, err error) {
	statement := withRowLimit(`SELECT concat(p.provider, ':', p.id), toString(wm.day), toInt64(sum(wm.items_started)), toInt64(sum(wm.items_completed)), toInt64(sum(wm.wip_count_end_of_day)), avg(wm.bug_completed_ratio), toFloat64(sum(wm.story_points_completed))
FROM ` + projectIdentityJoinSQL() + `
INNER JOIN (
	SELECT team_id, provider, work_scope_id, day, items_started, items_completed, wip_count_end_of_day, bug_completed_ratio, story_points_completed,
		row_number() OVER (PARTITION BY team_id, provider, work_scope_id, day ORDER BY computed_at DESC, cityHash64(tuple(items_started, items_completed, wip_count_end_of_day, bug_completed_ratio, story_points_completed)) DESC) AS rn
	FROM work_item_metrics_daily
	WHERE org_id = {org_id:String}` + timeBound.dayPredicate("day") + `
) AS wm ON ` + projectIdentityMatchSQL("wm", "work_scope_id") + ` AND wm.rn = 1
GROUP BY p.provider, p.id, wm.day
ORDER BY p.id, wm.day DESC`)
	byProject = make(map[string][]flowDailyRow)
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var r flowDailyRow
		var projectKey string
		if err := row.Scan(&projectKey, &r.day, &r.itemsStarted, &r.itemsCompleted, &r.wipCountEndOfDay, &r.bugCompletedRatioAvg, &r.storyPointsCompletedSum); err != nil {
			return err
		}
		byProject[projectKey] = append(byProject[projectKey], r)
		return nil
	}, timeBound.bindings()...)
	return byProject, scanErr
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
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, repoID)},
		})
		return nil
	}, timeBound.bindings()...)
	return rowCount, scanErr
}
