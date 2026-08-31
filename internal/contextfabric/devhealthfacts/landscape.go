package devhealthfacts

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// LandscapeProvider implements contextfabric.FactProvider for FactLandscape
// (CHAOS-4364) from ic_landscape_rolling_30d -- Dev Health Ops' precomputed
// IC churn/throughput/cycle/WIP scatter (three "maps": churn_throughput,
// cycle_throughput, wip_throughput), joined against team_project_ownership
// for the project rollup. This provider never recomputes a landscape
// coordinate or a percentile itself (§19.6.3): it reads Ops' own
// already-computed per-identity 30-day rolling stats and aggregates them by
// (team_id, map_name) only -- it NEVER emits a per-identity row. That is
// deliberate, not an economy: the platform's visualization guardrail bars
// person-to-person rankings (root AGENTS.md, "No person-to-person
// rankings"), and a Rows table is exactly the renderable surface a ranking
// could ride on. Aggregating to (team, map/area) before this ever reaches a
// CanonicalFact keeps that guarantee structural rather than a rule a
// downstream renderer has to remember.
//
// ic_landscape_rolling_30d is a genuine ReplacingMergeTree(computed_at)
// keyed on (org_id, repo_id, team_id, map_name, as_of_day, identity_id)
// (024_add_org_id.sql / 027_add_org_id_to_sorting_keys.py), so FINAL alone
// is the correct dedup mechanism here -- the same distinction
// queryTeamFlowEfficiency's doc comment draws for work_item_cycle_times,
// and unlike the plain-MergeTree tables elsewhere in this package that need
// an explicit row_number() tiebreak.
//
// team_id on this table is read exactly as Ops wrote it, the same
// "Ops-computed columns are trusted, never re-derived" contract every
// sibling provider in this package already applies to team_metrics_daily.
// team_id, capacity_forecasts.team_id, and investment_metrics_daily.team_id
// (CHAOS-4321 governs how Ops itself may derive a TEAM attribution -- never
// from person membership alone -- not what this read-only provider does
// with an already-published column).
type LandscapeProvider struct{ facts clickhouseFacts }

func newLandscapeProvider(client contextpacket.ClickHouseQueryClient) *LandscapeProvider {
	return &LandscapeProvider{facts: clickhouseFacts{client: client}}
}

func (p *LandscapeProvider) Capability() contextfabric.FactCapability {
	capability := newCapability(contextfabric.FactLandscape, "devhealthfacts.landscape", []contextfabric.SubjectKind{
		contextfabric.SubjectTeam, contextfabric.SubjectProject,
	})
	capability.Tables = map[contextfabric.SubjectKind][]contextfabric.FactTableShape{
		contextfabric.SubjectTeam:    {contextfabric.FactTableBreakdown},
		contextfabric.SubjectProject: {contextfabric.FactTableBreakdown},
	}
	capability.EstimatedItems = 3
	return capability
}

func (p *LandscapeProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
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
		rowCount, rowsOmitted, scanErr := p.readTeamLandscape(ctx, orgID, teamSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team landscape", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery || rowsOmitted > 0
		omittedRows += rowsOmitted
	}

	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		rowCount, rowsOmitted, scanErr := p.readProjectLandscape(ctx, orgID, projectSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project landscape", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery || rowsOmitted > 0
		omittedRows += rowsOmitted
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	// CHAOS-4521b: this source has no project dimension, so an all-project
	// read that came back empty says something more specific than "no rows".
	retentionReason = explainTeamScopedProjectAbsence(timeBound, state, retentionReason, query.Subjects)
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: truncated, OmittedCount: omittedRows}, nil
}

// landscapeAreaRow is one (map_name, as_of_day) area's aggregate for a
// single team -- already collapsed across every identity/repo the team's
// people touched that day, never a per-identity row (see the package doc
// comment).
type landscapeAreaRow struct {
	mapName, asOfDay              string
	identityCount                 int64
	churnLOC30d, deliveryUnits30d int64
	cycleP50Avg30dHours           float64
	wipMax30d                     int64
}

func (r landscapeAreaRow) toFactValueRow() contextfabric.FactValueRow {
	return contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
		"map_name":                contextfabric.StringFactValue(r.mapName),
		"as_of_day":               contextfabric.StringFactValue(r.asOfDay),
		"identity_count":          contextfabric.IntegerFactValue(r.identityCount),
		"churn_loc_30d":           contextfabric.IntegerFactValue(r.churnLOC30d),
		"delivery_units_30d":      contextfabric.IntegerFactValue(r.deliveryUnits30d),
		"cycle_p50_30d_hours_avg": contextfabric.NumberFactValue(r.cycleP50Avg30dHours),
		"wip_max_30d":             contextfabric.IntegerFactValue(r.wipMax30d),
	}}
}

// readTeamLandscape aggregates ic_landscape_rolling_30d to (team_id,
// map_name) at each team's own latest retained as_of_day -- never averaged
// or summed ACROSS as_of_day, and never emitted per-identity.
func (p *LandscapeProvider) readTeamLandscape(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, int, error) {
	ids, bySubject := subjectIndex(subjects, teamPrefix)
	if len(ids) == 0 {
		return 0, 0, nil
	}
	statement := withRowLimit(`SELECT toString(team_id), map_name, toString(as_of_day), toInt64(count()), toInt64(sum(churn_loc_30d)), toInt64(sum(delivery_units_30d)), avg(cycle_p50_30d_hours), toInt64(max(wip_max_30d))
FROM (
	SELECT team_id, map_name, as_of_day, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d,
		max(as_of_day) OVER (PARTITION BY team_id) AS latest_day
	FROM ic_landscape_rolling_30d FINAL
	WHERE org_id = {org_id:String} AND toString(team_id) IN {ids:Array(String)}` + timeBound.dayPredicate("as_of_day") + `
)
WHERE as_of_day = latest_day
GROUP BY team_id, map_name, as_of_day
ORDER BY team_id, map_name`)
	rowCount := 0
	byTeam := make(map[string][]landscapeAreaRow)
	var teamOrder []string
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var teamID string
		var r landscapeAreaRow
		if err := row.Scan(&teamID, &r.mapName, &r.asOfDay, &r.identityCount, &r.churnLOC30d, &r.deliveryUnits30d, &r.cycleP50Avg30dHours, &r.wipMax30d); err != nil {
			return err
		}
		if _, ok := bySubject[teamID]; !ok {
			return nil
		}
		if _, seen := byTeam[teamID]; !seen {
			teamOrder = append(teamOrder, teamID)
		}
		byTeam[teamID] = append(byTeam[teamID], r)
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return rowCount, 0, scanErr
	}
	totalOmitted := 0
	for _, teamID := range teamOrder {
		rows := byTeam[teamID]
		subject := bySubject[teamID]
		areaRows := make([]contextfabric.FactValueRow, 0, len(rows))
		for _, r := range rows {
			areaRows = append(areaRows, r.toFactValueRow())
		}
		areaRows, omitted := capFactValueRows(areaRows)
		totalOmitted += omitted
		fields := map[string]contextfabric.FactValue{
			"area_count": contextfabric.IntegerFactValue(int64(len(rows))),
			// CHAOS-4633 P1: Key = [map_name, as_of_day] -- GROUP BY
			// team_id, map_name, as_of_day above already yields one row per
			// map for this team's own latest as_of_day, so map_name alone
			// identifies a row here; as_of_day rides along even though it
			// is the same value across this particular team's whole table
			// (a known Fable-F3 case kept in Key rather than hoisted to a
			// sibling scalar, so as not to change Rows' shape in this
			// additive P1; flagged as follow-up).
			"area_breakdown": contextfabric.TableFactValue(contextfabric.FactTable{
				Shape: contextfabric.FactTableBreakdown,
				Key:   []string{"map_name", "as_of_day"},
				Measures: []string{
					"identity_count", "churn_loc_30d", "delivery_units_30d",
					"cycle_p50_30d_hours_avg", "wip_max_30d",
				},
				Grain: timeBound.effectiveGrain(grainDaily),
				Rows:  areaRows,
			}),
		}
		if omitted > 0 {
			fields["area_breakdown_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactLandscape, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("team", teamID)},
		})
	}
	return rowCount, totalOmitted, nil
}

// readProjectLandscape rolls FactLandscape up for a project through
// projects -> team_project_ownership -> ic_landscape_rolling_30d: every team
// currently (or, for a bounded historical query, AS OF the requested
// instant) owning the project contributes its own (team_id, map_name)
// landscape aggregate -- see metrics.go's readProjectMetrics for why the
// join is on (provider, project_key), never project_id.
func (p *LandscapeProvider) readProjectLandscape(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, int, error) {
	ids, bySubject := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, 0, nil
	}
	ownershipPredicate := " AND valid_from <= now64(3) AND valid_to IS NULL"
	if timeBound.active {
		ownershipPredicate = fmt.Sprintf(" AND valid_from <= {%s:DateTime64(6,'UTC')} AND (valid_to IS NULL OR valid_to > {%s:DateTime64(6,'UTC')})", boundEndParam, boundEndParam)
	}
	// CHAOS-4521b, self-found after codex R3: this reader carried its OWN
	// inline copy of the ownership join and never called the shared helper,
	// so it silently missed the move onto the project identity -- it would
	// have gone to zero for Linear the moment CHAOS-4530 deployed, while
	// health and investment kept working. That is the duplicated-SQL drift
	// in its most literal form: one copy was updated and the other was not,
	// and nothing failed.
	statement := withRowLimit(`SELECT concat(p.provider, ':', p.id), il.team_id, il.map_name, toString(il.as_of_day), toInt64(count()), toInt64(sum(il.churn_loc_30d)), toInt64(sum(il.delivery_units_30d)), avg(il.cycle_p50_30d_hours), toInt64(max(il.wip_max_30d))
FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
INNER JOIN (
	SELECT team_id, map_name, as_of_day, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d,
		max(as_of_day) OVER (PARTITION BY team_id) AS latest_day
	FROM ic_landscape_rolling_30d FINAL
	WHERE org_id = {org_id:String}` + timeBound.dayPredicate("as_of_day") + `
) AS il ON il.team_id = p.team_id AND il.as_of_day = il.latest_day
GROUP BY p.id, p.provider, il.team_id, il.map_name, il.as_of_day
ORDER BY p.id, il.team_id, il.map_name`)
	rowCount := 0
	type projectAreaRow struct {
		teamID string
		row    landscapeAreaRow
	}
	byProject := make(map[string][]projectAreaRow)
	var projectOrder []string
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var projectSubjectKey, teamID string
		var r landscapeAreaRow
		if err := row.Scan(&projectSubjectKey, &teamID, &r.mapName, &r.asOfDay, &r.identityCount, &r.churnLOC30d, &r.deliveryUnits30d, &r.cycleP50Avg30dHours, &r.wipMax30d); err != nil {
			return err
		}
		if _, ok := bySubject[projectSubjectKey]; !ok {
			return nil
		}
		if _, seen := byProject[projectSubjectKey]; !seen {
			projectOrder = append(projectOrder, projectSubjectKey)
		}
		byProject[projectSubjectKey] = append(byProject[projectSubjectKey], projectAreaRow{teamID: teamID, row: r})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return rowCount, 0, scanErr
	}
	totalOmitted := 0
	for _, projectKey := range projectOrder {
		rows := byProject[projectKey]
		subject := bySubject[projectKey]
		teamRows := make([]contextfabric.FactValueRow, 0, len(rows))
		evidenceRefIDs := make([]string, 0, len(rows)+1)
		evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("project", projectKey))
		seenTeams := make(map[string]bool, len(rows))
		for _, entry := range rows {
			teamRow := entry.row.toFactValueRow()
			teamRow.Fields["team_id"] = contextfabric.StringFactValue(entry.teamID)
			teamRows = append(teamRows, teamRow)
			if !seenTeams[entry.teamID] {
				seenTeams[entry.teamID] = true
				evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("team", entry.teamID))
			}
		}
		if len(teamRows) == 0 {
			continue
		}
		teamRows, omitted := capFactValueRows(teamRows)
		totalOmitted += omitted
		fields := map[string]contextfabric.FactValue{
			"rollup_basis": contextfabric.StringFactValue("team_project_ownership_landscape"),
			"team_count":   contextfabric.IntegerFactValue(int64(len(seenTeams))),
			// CHAOS-4633 P1: Key = [team_id, map_name, as_of_day] -- the
			// SQL's own GROUP BY p.id, il.team_id, il.map_name, il.as_of_day
			// already yields one row per (team, map) for the project.
			"team_breakdown": contextfabric.TableFactValue(contextfabric.FactTable{
				Shape: contextfabric.FactTableBreakdown,
				Key:   []string{"team_id", "map_name", "as_of_day"},
				Measures: []string{
					"identity_count", "churn_loc_30d", "delivery_units_30d",
					"cycle_p50_30d_hours_avg", "wip_max_30d",
				},
				Grain: timeBound.effectiveGrain(grainDaily),
				Rows:  teamRows,
			}),
		}
		if omitted > 0 {
			fields["team_breakdown_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactLandscape, Subject: subject, Fields: fields,
			EvidenceRefIDs: evidenceRefIDs,
		})
	}
	return rowCount, totalOmitted, nil
}
