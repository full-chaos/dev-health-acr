package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// LandscapeProvider implements contextfabric.FactProvider for FactLandscape
// (CHAOS-4364) from ic_landscape_rolling_30d -- Dev Health Ops' precomputed
// IC churn/throughput/cycle/WIP scatter (three "maps": churn_throughput,
// cycle_throughput, wip_throughput), joined against team_project_ownership
// for the project rollup. This provider never recomputes a landscape
// coordinate or a percentile itself (§19.6.3): it reads Ops' own
// already-computed per-identity 30-day rolling stats and aggregates them by
// team only -- it NEVER emits a per-identity row. That is
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
// team_id and capacity_forecasts.team_id
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

func (p *LandscapeProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
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
	rejectedCount := 0
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.landscape", contextfabric.FactLandscape, rejectedCount)
		}
	}()

	if teamSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectTeam); len(teamSubjects) > 0 {
		rowCount, rowsOmitted, rejected, scanErr := p.readTeamLandscape(ctx, orgID, teamSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team landscape", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery || rowsOmitted > 0
		omittedRows += rowsOmitted
		rejectedCount += rejected
	}

	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		rowCount, rowsOmitted, rejected, scanErr := p.readProjectLandscape(ctx, orgID, projectSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project landscape", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery || rowsOmitted > 0
		omittedRows += rowsOmitted
		rejectedCount += rejected
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	// CHAOS-4521b: this source has no project dimension, so an all-project
	// read that came back empty says something more specific than "no rows".
	retentionReason = explainTeamScopedProjectAbsence(timeBound, state, retentionReason, query.Subjects)
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: truncated, OmittedCount: omittedRows}
	return result, nil
}

// landscapeAreaRow is one as_of_day's aggregate for a single team --
// already collapsed across every identity/repo the team's people touched
// that day, never a per-identity row (see the package doc comment). Ops
// writes the same four stats on each of an identity's three map rows, so
// the statements below take ONE copy per identity before they aggregate.
type landscapeAreaRow struct {
	asOfDay                       string
	identityCount                 int64
	churnLOC30d, deliveryUnits30d int64
	cycleP50Avg30dHours           float64
	wipMax30d                     int64
}

func (r landscapeAreaRow) toFactValueRow() contextfabric.FactValueRow {
	return contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
		"as_of_day":               contextfabric.StringFactValue(r.asOfDay),
		"identity_count":          contextfabric.IntegerFactValue(r.identityCount),
		"churn_loc_30d":           contextfabric.IntegerFactValue(r.churnLOC30d),
		"delivery_units_30d":      contextfabric.IntegerFactValue(r.deliveryUnits30d),
		"cycle_p50_30d_hours_avg": contextfabric.NumberFactValue(r.cycleP50Avg30dHours),
		"wip_max_30d":             contextfabric.IntegerFactValue(r.wipMax30d),
	}}
}

// landscapeOneCopyColumns takes one copy of an identity's four stats out of
// its map rows. Ops writes the same churn, delivery, cycle and WIP values on
// each of the identity's three map rows (icfinalize/landscape.go copies one
// RollingStat into every map); only the map coordinates differ. Summing the
// rows of all maps would count every person three times. argMin(., map_name)
// names the copy, so the pick is the same on every read and an identity with
// fewer map rows still counts once.
const landscapeOneCopyColumns = `argMin(churn_loc_30d, map_name) AS one_churn, argMin(delivery_units_30d, map_name) AS one_delivery, argMin(cycle_p50_30d_hours, map_name) AS one_cycle, argMin(wip_max_30d, map_name) AS one_wip`

// landscapeLiveRowSQL keeps a row the compute produced and drops the row of
// zeros Ops writes to retract a key (teamkeytables.ICLandscapeRolling30d's
// LiveRow). x_norm and y_norm are the markers: a computed row never holds 0
// in them, and a retraction row holds the column default 0 in every measure.
const landscapeLiveRowSQL = `(x_raw != 0 OR y_raw != 0 OR x_norm != 0 OR y_norm != 0 OR churn_loc_30d != 0 OR delivery_units_30d != 0 OR cycle_p50_30d_hours != 0 OR wip_max_30d != 0)`

// landscapeLatestRowsSQL is the live rows of each team's own latest retained
// as_of_day, one row per (team, map, identity).
func landscapeLatestRowsSQL(teamPredicate string, timeBound factTimeBound) string {
	return `SELECT team_id, map_name, as_of_day, identity_id, x_raw, y_raw, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d
	FROM (
		SELECT team_id, map_name, as_of_day, identity_id, x_raw, y_raw, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d,
			max(as_of_day) OVER (PARTITION BY team_id) AS latest_day
		FROM ic_landscape_rolling_30d FINAL
		WHERE org_id = {org_id:String}` + teamPredicate + ` AND ` + landscapeLiveRowSQL + timeBound.dayPredicate("as_of_day") + `
	)
	WHERE as_of_day = latest_day`
}

// landscapeOneCopySQL is landscapeLatestRowsSQL reduced to one copy of the
// stats per (team, identity).
func landscapeOneCopySQL(teamPredicate string, timeBound factTimeBound) string {
	return `SELECT team_id, as_of_day, identity_id, ` + landscapeOneCopyColumns + `
FROM (
` + landscapeLatestRowsSQL(teamPredicate, timeBound) + `
)
GROUP BY team_id, as_of_day, identity_id`
}

// landscapeCoordinateRow is one map's mean raw coordinate for a team. A
// coordinate is aggregated per team and map and never per identity, so the
// table is no ranking surface.
type landscapeCoordinateRow struct {
	mapName, asOfDay string
	xRawAvg, yRawAvg float64
}

func (r landscapeCoordinateRow) toFactValueRow() contextfabric.FactValueRow {
	return contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
		"map_name":   contextfabric.StringFactValue(r.mapName),
		"as_of_day":  contextfabric.StringFactValue(r.asOfDay),
		"x_raw_mean": contextfabric.NumberFactValue(r.xRawAvg),
		"y_raw_mean": contextfabric.NumberFactValue(r.yRawAvg),
	}}
}

func landscapeCoordinatesTable(rows []contextfabric.FactValueRow, key []string, timeBound factTimeBound) contextfabric.FactValue {
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:    contextfabric.FactTableBreakdown,
		Key:      key,
		Measures: []string{"x_raw_mean", "y_raw_mean"},
		Grain:    timeBound.effectiveGrain(grainDaily),
		Rows:     rows,
	})
}

// readTeamLandscape aggregates ic_landscape_rolling_30d to one row per team
// at each team's own latest retained as_of_day -- never averaged or summed
// ACROSS as_of_day, and never emitted per-identity. The stats are read once
// per team; the per-map raw coordinates ride in map_coordinates.
func (p *LandscapeProvider) readTeamLandscape(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, int, int, error) {
	ids, bySubject, rejected := subjectIndex(subjects, teamPrefix)
	if len(ids) == 0 {
		return 0, 0, rejected, nil
	}
	teamPredicate := ` AND toString(team_id) IN {ids:Array(String)}`
	statement := withRowLimit(`SELECT toString(team_id), toString(as_of_day), toInt64(count()), toInt64(sum(one_churn)), toInt64(sum(one_delivery)), avg(one_cycle), toInt64(max(one_wip))
FROM (
` + landscapeOneCopySQL(teamPredicate, timeBound) + `
)
GROUP BY team_id, as_of_day
ORDER BY team_id`)
	rowCount := 0
	byTeam := make(map[string]landscapeAreaRow)
	var teamOrder []string
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var teamID string
		var r landscapeAreaRow
		if err := row.Scan(&teamID, &r.asOfDay, &r.identityCount, &r.churnLOC30d, &r.deliveryUnits30d, &r.cycleP50Avg30dHours, &r.wipMax30d); err != nil {
			return err
		}
		if _, ok := bySubject[teamID]; !ok {
			return nil
		}
		if _, seen := byTeam[teamID]; !seen {
			teamOrder = append(teamOrder, teamID)
		}
		byTeam[teamID] = r
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return rowCount, 0, rejected, scanErr
	}
	coordinateStatement := withRowLimit(`SELECT toString(team_id), map_name, toString(as_of_day), avg(x_raw), avg(y_raw)
FROM (
` + landscapeLatestRowsSQL(teamPredicate, timeBound) + `
)
GROUP BY team_id, map_name, as_of_day
ORDER BY team_id, map_name`)
	coordinatesByTeam := make(map[string][]contextfabric.FactValueRow)
	coordinateRows := 0
	scanErr = p.facts.query(ctx, coordinateStatement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		coordinateRows++
		var teamID string
		var c landscapeCoordinateRow
		if err := row.Scan(&teamID, &c.mapName, &c.asOfDay, &c.xRawAvg, &c.yRawAvg); err != nil {
			return err
		}
		coordinatesByTeam[teamID] = append(coordinatesByTeam[teamID], c.toFactValueRow())
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return rowCount + coordinateRows, 0, rejected, scanErr
	}
	for _, teamID := range teamOrder {
		fields := map[string]contextfabric.FactValue{
			"area_breakdown": contextfabric.TableFactValue(contextfabric.FactTable{
				Shape: contextfabric.FactTableBreakdown,
				Key:   []string{"as_of_day"},
				Measures: []string{
					"identity_count", "churn_loc_30d", "delivery_units_30d",
					"cycle_p50_30d_hours_avg", "wip_max_30d",
				},
				Grain: timeBound.effectiveGrain(grainDaily),
				Rows:  []contextfabric.FactValueRow{byTeam[teamID].toFactValueRow()},
			}),
		}
		if coordinates := coordinatesByTeam[teamID]; len(coordinates) > 0 {
			fields["map_coordinates"] = landscapeCoordinatesTable(coordinates, []string{"map_name", "as_of_day"}, timeBound)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactLandscape, Subject: bySubject[teamID], Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, teamID)},
		})
	}
	return rowCount + coordinateRows, 0, rejected, nil
}

// readProjectLandscape rolls FactLandscape up for a project through
// projects -> team_project_ownership -> ic_landscape_rolling_30d: every team
// currently (or, for a bounded historical query, AS OF the requested
// instant) owning the project contributes its own team
// landscape aggregate -- see metrics.go's readProjectMetrics for why the
// join is on (provider, project_key), never project_id.
func (p *LandscapeProvider) readProjectLandscape(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, int, int, error) {
	ids, bySubject, rejected := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, 0, rejected, nil
	}
	ownershipPredicate := ownershipValidityPredicate(timeBound)
	// CHAOS-4521b, self-found after codex R3: this reader carried its OWN
	// inline copy of the ownership join and never called the shared helper,
	// so it silently missed the move onto the project identity -- it would
	// have gone to zero for Linear the moment CHAOS-4530 deployed, while
	// health and investment kept working. That is the duplicated-SQL drift
	// in its most literal form: one copy was updated and the other was not,
	// and nothing failed.
	statement := withRowLimit(`SELECT concat(p.provider, ':', p.id), il.team_id, toString(il.as_of_day), toInt64(count()), toInt64(sum(il.one_churn)), toInt64(sum(il.one_delivery)), avg(il.one_cycle), toInt64(max(il.one_wip))
FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
INNER JOIN (
` + landscapeOneCopySQL("", timeBound) + `
) AS il ON il.team_id = p.team_id
GROUP BY p.id, p.provider, il.team_id, il.as_of_day
ORDER BY p.id, il.team_id`)
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
		if err := row.Scan(&projectSubjectKey, &teamID, &r.asOfDay, &r.identityCount, &r.churnLOC30d, &r.deliveryUnits30d, &r.cycleP50Avg30dHours, &r.wipMax30d); err != nil {
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
		return rowCount, 0, rejected, scanErr
	}
	coordinateStatement := withRowLimit(`SELECT concat(p.provider, ':', p.id), il.team_id, il.map_name, toString(il.as_of_day), avg(il.x_raw), avg(il.y_raw)
FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
INNER JOIN (
` + landscapeLatestRowsSQL("", timeBound) + `
) AS il ON il.team_id = p.team_id
GROUP BY p.id, p.provider, il.team_id, il.map_name, il.as_of_day
ORDER BY p.id, il.team_id, il.map_name`)
	coordinatesByProject := make(map[string][]contextfabric.FactValueRow)
	coordinateRows := 0
	scanErr = p.facts.query(ctx, coordinateStatement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		coordinateRows++
		var projectSubjectKey, teamID string
		var c landscapeCoordinateRow
		if err := row.Scan(&projectSubjectKey, &teamID, &c.mapName, &c.asOfDay, &c.xRawAvg, &c.yRawAvg); err != nil {
			return err
		}
		coordinate := c.toFactValueRow()
		coordinate.Fields["team_id"] = contextfabric.StringFactValue(teamID)
		coordinatesByProject[projectSubjectKey] = append(coordinatesByProject[projectSubjectKey], coordinate)
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return rowCount + coordinateRows, 0, rejected, scanErr
	}
	totalOmitted := 0
	for _, projectKey := range projectOrder {
		rows := byProject[projectKey]
		subject := bySubject[projectKey]
		teamRows := make([]contextfabric.FactValueRow, 0, len(rows))
		evidenceRefIDs := make([]string, 0, len(rows)+1)
		evidenceRefIDs = append(evidenceRefIDs, evidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, projectKey))
		seenTeams := make(map[string]bool, len(rows))
		for _, entry := range rows {
			teamRow := entry.row.toFactValueRow()
			teamRow.Fields["team_id"] = contextfabric.StringFactValue(entry.teamID)
			teamRows = append(teamRows, teamRow)
			if !seenTeams[entry.teamID] {
				seenTeams[entry.teamID] = true
				evidenceRefIDs = append(evidenceRefIDs, evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, entry.teamID))
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
			"team_breakdown": contextfabric.TableFactValue(contextfabric.FactTable{
				Shape: contextfabric.FactTableBreakdown,
				Key:   []string{"team_id", "as_of_day"},
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
		if coordinates := coordinatesByProject[projectKey]; len(coordinates) > 0 {
			coordinates, coordinatesOmitted := capFactValueRows(coordinates)
			totalOmitted += coordinatesOmitted
			fields["map_coordinates"] = landscapeCoordinatesTable(coordinates, []string{"team_id", "map_name", "as_of_day"}, timeBound)
			if coordinatesOmitted > 0 {
				fields["map_coordinates_omitted_count"] = contextfabric.IntegerFactValue(int64(coordinatesOmitted))
			}
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactLandscape, Subject: subject, Fields: fields,
			EvidenceRefIDs: evidenceRefIDs,
		})
	}
	return rowCount + coordinateRows, totalOmitted, rejected, nil
}
