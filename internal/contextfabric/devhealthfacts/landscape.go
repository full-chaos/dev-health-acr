package devhealthfacts

import (
	"context"
	"log/slog"
	"strconv"

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
	timeBound = statedWindowBound(query, timeBound)
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
	// cycleP50Avg30dHours is nil when no live identity of the group holds a
	// cycle: Ops stores NULL for an identity with no merged pull request.
	cycleP50Avg30dHours *float64
	wipMax30d           int64
}

func optionalNumber(value *float64) contextfabric.FactValue {
	if value == nil {
		return contextfabric.NullFactValue()
	}
	return contextfabric.NumberFactValue(*value)
}

func (r landscapeAreaRow) toFactValueRow() contextfabric.FactValueRow {
	return contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
		"as_of_day":               contextfabric.StringFactValue(r.asOfDay),
		"identity_count":          contextfabric.IntegerFactValue(r.identityCount),
		"churn_loc_30d":           contextfabric.IntegerFactValue(r.churnLOC30d),
		"delivery_units_30d":      contextfabric.IntegerFactValue(r.deliveryUnits30d),
		"cycle_p50_30d_hours_avg": optionalNumber(r.cycleP50Avg30dHours),
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
const landscapeOneCopyColumns = `argMinIf(churn_loc_30d, map_name, is_live AND as_of_day = latest_day) AS one_churn, argMinIf(delivery_units_30d, map_name, is_live AND as_of_day = latest_day) AS one_delivery, argMinIf(cycle_p50_30d_hours, map_name, is_live AND as_of_day = latest_day) AS one_cycle, argMinIf(wip_max_30d, map_name, is_live AND as_of_day = latest_day) AS one_wip`

// landscapeLiveRowSQL keeps a row the compute produced and drops the row of
// zeros Ops writes to retract a key (teamkeytables.ICLandscapeRolling30d's
// LiveRow). x_norm and y_norm are the markers: a computed row never holds 0
// in them, and a retraction row holds the column default in every measure:
// 0, or NULL in a Nullable cycle. ifNull keeps is_live 0 or 1, so a
// retraction row with a NULL cycle is still counted as retracted.
const landscapeLiveRowSQL = `(x_raw != 0 OR y_raw != 0 OR x_norm != 0 OR y_norm != 0 OR churn_loc_30d != 0 OR delivery_units_30d != 0 OR ifNull(cycle_p50_30d_hours, 0) != 0 OR wip_max_30d != 0)`

// landscapeRowsSQL reads the rows of the requested teams with three flags
// per row: is_live (the row is not a retraction), latest_day (the team's own
// latest day that holds a live row; a retracted day is never the latest) and
// retracted_rows (the retraction rows the read met for the team). Nothing is
// dropped here, so a team with only retracted rows still reports its count.
func landscapeRowsSQL(teamPredicate string, timeBound factTimeBound) string {
	return `SELECT team_id, map_name, as_of_day, identity_id, x_raw, y_raw, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d, is_live, latest_day, retracted_rows
	FROM (
		SELECT team_id, map_name, as_of_day, identity_id, x_raw, y_raw, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d,
			` + landscapeLiveRowSQL + ` AS is_live,
			max(if(is_live, as_of_day, toDate(0))) OVER (PARTITION BY team_id) AS latest_day,
			sum(toUInt64(is_live = 0)) OVER (PARTITION BY team_id) AS retracted_rows
		FROM ic_landscape_rolling_30d FINAL
		WHERE org_id = {org_id:String}` + teamPredicate + timeBound.dayPredicate("as_of_day") + `
	)`
}

// landscapeLatestRowsSQL is the live rows of each team's own latest retained
// as_of_day, one row per (team, map, identity).
func landscapeLatestRowsSQL(teamPredicate string, timeBound factTimeBound) string {
	return `SELECT team_id, map_name, as_of_day, identity_id, x_raw, y_raw
FROM (
` + landscapeRowsSQL(teamPredicate, timeBound) + `
)
WHERE is_live AND as_of_day = latest_day`
}

// landscapeOneCopySQL reduces landscapeRowsSQL to one row per (team,
// identity): live_identity says the identity holds a live row on the team's
// latest day, and its four stats are one copy taken from those rows.
func landscapeOneCopySQL(teamPredicate string, timeBound factTimeBound) string {
	return `SELECT team_id, identity_id, max(latest_day) AS team_latest_day, max(retracted_rows) AS team_retracted,
	max(toUInt8(is_live AND as_of_day = latest_day)) AS live_identity,
	` + landscapeOneCopyColumns + `
FROM (
` + landscapeRowsSQL(teamPredicate, timeBound) + `
)
GROUP BY team_id, identity_id`
}

// landscapeStatsColumns are the shared stats of the live identities of one
// group of landscapeOneCopySQL rows, then the group's retraction count.
// avgIf skips a NULL cycle, so an identity with no cycle is no sample of the
// mean, and a group with none averages to NULL.
func landscapeStatsColumns(alias string) string {
	return `toInt64(sum(` + alias + `live_identity)), toInt64(sumIf(` + alias + `one_churn, ` + alias + `live_identity = 1)), toInt64(sumIf(` + alias + `one_delivery, ` + alias + `live_identity = 1)), avgIf(` + alias + `one_cycle, ` + alias + `live_identity = 1), toInt64(maxIf(` + alias + `one_wip, ` + alias + `live_identity = 1)), toInt64(max(` + alias + `team_retracted))`
}

// landscapeRetractions is the decision basis of the retraction filter: how
// many retraction rows (Ops' rows of zeros) a read dropped, for how many
// teams, and for how many of them no live row was left to serve.
type landscapeRetractions struct {
	rows              int64
	groupsWith        int
	groupsWithoutLive int
}

func (r *landscapeRetractions) add(retractedRows int64, withoutLive bool) {
	if retractedRows > 0 {
		r.rows += retractedRows
		r.groupsWith++
	}
	if withoutLive {
		r.groupsWithoutLive++
	}
}

func (r landscapeRetractions) log(ctx context.Context, subjectKind string) {
	if r.rows == 0 && r.groupsWithoutLive == 0 {
		return
	}
	slog.LogAttrs(ctx, slog.LevelDebug, "devhealthfacts.landscape_retracted_rows",
		slog.String("subject_kind", contextfabric.SanitizeLogAttr(subjectKind)),
		slog.String("basis", "retraction_row_of_zeros"),
		slog.Int64("retracted_rows", r.rows),
		slog.Int("groups_with_retracted_rows", r.groupsWith),
		slog.Int("groups_without_live_rows", r.groupsWithoutLive))
}

// landscapeCoordinateRow is one map's mean raw coordinate for a team. A
// coordinate is aggregated per team and map and never per identity, so the
// table is no ranking surface.
type landscapeCoordinateRow struct {
	mapName, asOfDay string
	xRawAvg, yRawAvg float64
}

// landscapeDailyRow is one team's day of the series (CHAOS-12534): one copy of
// each live identity's stats, summed. Each day's values are the rolling 30 days
// that end on it, so neighbouring days overlap. heldDays is how many days the
// read held for the team before the newest landscapeSeriesDays were kept.
type landscapeDailyRow struct {
	day                        string
	identityCount              int64
	churnLOC30d, deliveryUnits int64
	heldDays                   int64
}

// landscapeSeriesDays is the newest days a team's series keeps: the most rows a
// claim carries and points a trend draws.
const landscapeSeriesDays = contextfabric.MaxFactValueRows

// landscapeDailySeriesSQL is each requested team's live rows, one copy per
// identity and day, summed per day, the newest landscapeSeriesDays per team. A
// retracted day holds no live row and is absent, never a day of zeros (check
// 17). The limit is per team, and the statement's own row budget is that limit
// times the teams, so a read of many teams cuts no team's series.
func landscapeDailySeriesSQL(timeBound factTimeBound, teams int) string {
	return withRowLimitOf(`SELECT team_key, day, identities, churn, delivery, held_days
FROM (
	SELECT toString(team_id) AS team_key, toString(as_of_day) AS day, toInt64(count()) AS identities,
		toInt64(sum(one_churn)) AS churn, toInt64(sum(one_delivery)) AS delivery,
		toInt64(count() OVER (PARTITION BY team_id)) AS held_days
	FROM (
		SELECT team_id, as_of_day, identity_id, argMin(churn_loc_30d, map_name) AS one_churn, argMin(delivery_units_30d, map_name) AS one_delivery
		FROM ic_landscape_rolling_30d FINAL
		WHERE org_id = {org_id:String} AND toString(team_id) IN {ids:Array(String)} AND `+landscapeLiveRowSQL+timeBound.dayPredicate("as_of_day")+`
		GROUP BY team_id, as_of_day, identity_id
	)
	GROUP BY team_id, as_of_day
)
ORDER BY team_key, day DESC
LIMIT `+strconv.Itoa(landscapeSeriesDays)+` BY team_key`, teams*landscapeSeriesDays)
}

// landscapeDailyTable is the declared time series of one team, newest day
// first, capped like every other daily series.
func landscapeDailyTable(rows []landscapeDailyRow, timeBound factTimeBound) (contextfabric.FactValue, bool, int) {
	if len(rows) == 0 {
		return contextfabric.FactValue{}, false, 0
	}
	valueRows := make([]contextfabric.FactValueRow, 0, len(rows))
	for _, r := range rows {
		valueRows = append(valueRows, contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
			"day":                contextfabric.StringFactValue(r.day),
			"identity_count":     contextfabric.IntegerFactValue(r.identityCount),
			"churn_loc_30d":      contextfabric.IntegerFactValue(r.churnLOC30d),
			"delivery_units_30d": contextfabric.IntegerFactValue(r.deliveryUnits),
		}})
	}
	valueRows, _ = capFactValueRows(valueRows)
	omitted := int(rows[0].heldDays) - len(valueRows)
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:    contextfabric.FactTableTimeSeries,
		Key:      []string{"day"},
		Measures: []string{"identity_count", "churn_loc_30d", "delivery_units_30d"},
		Grain:    timeBound.effectiveGrain(grainDaily),
		Rows:     valueRows,
	}), true, omitted
}

// landscapeAnswerOmits are the columns an answer does not show, because the
// source reads 0 for missing. A direct reader still gets them.
//
//   - wip_max_30d: every work-item count lands on rows no team owns
//     (CHAOS-12535), so a team reads 0 with or without work in progress.
//   - cycle_p50_30d_hours_avg: a day with no merged pull request is stored as a
//     0-hour cycle (CHAOS-12562), so a team's mean reads far too low. Held by
//     CHAOS-12565; lift it when CHAOS-12562 lands.
var landscapeAnswerOmits = []string{"wip_max_30d", "cycle_p50_30d_hours_avg"}

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
		// Deprecated (CHAOS-12533): on two of the three maps x_raw_mean is a mean
		// of log1p values, not a raw value. read_facts serves it until an
		// acr-data version bump can drop it.
		DirectOnly: true,
		Rows:       rows,
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
	statement := withRowLimit(`SELECT toString(team_id), toString(max(team_latest_day)), ` + landscapeStatsColumns("") + `
FROM (
` + landscapeOneCopySQL(teamPredicate, timeBound) + `
)
GROUP BY team_id
ORDER BY team_id`)
	rowCount := 0
	var retractions landscapeRetractions
	byTeam := make(map[string]landscapeAreaRow)
	var teamOrder []string
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var teamID string
		var r landscapeAreaRow
		var retracted int64
		if err := row.Scan(&teamID, &r.asOfDay, &r.identityCount, &r.churnLOC30d, &r.deliveryUnits30d, &r.cycleP50Avg30dHours, &r.wipMax30d, &retracted); err != nil {
			return err
		}
		if _, ok := bySubject[teamID]; !ok {
			return nil
		}
		retractions.add(retracted, r.identityCount == 0)
		if r.identityCount == 0 {
			return nil
		}
		if _, seen := byTeam[teamID]; !seen {
			teamOrder = append(teamOrder, teamID)
		}
		byTeam[teamID] = r
		return nil
	}, timeBound.bindings()...)
	retractions.log(ctx, "team")
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
		return max(rowCount, coordinateRows), 0, rejected, scanErr
	}
	commonDay, commonErr := p.readCommonLiveDay(ctx, orgID, teamOrder, byTeam, timeBound)
	if commonErr != nil {
		return max(rowCount, coordinateRows), 0, rejected, commonErr
	}
	seriesOmitted := 0
	dailyByTeam := make(map[string][]landscapeDailyRow)
	scanErr = p.facts.query(ctx, landscapeDailySeriesSQL(timeBound, len(ids)), orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var teamID string
		var r landscapeDailyRow
		if err := row.Scan(&teamID, &r.day, &r.identityCount, &r.churnLOC30d, &r.deliveryUnits, &r.heldDays); err != nil {
			return err
		}
		dailyByTeam[teamID] = append(dailyByTeam[teamID], r)
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return max(rowCount, coordinateRows), 0, rejected, scanErr
	}
	for _, teamID := range teamOrder {
		row := byTeam[teamID].toFactValueRow()
		fields := map[string]contextfabric.FactValue{
			// A table reaches no model (CHAOS-4355), so these scalars are what a
			// model can read and claim. The claim then carries the row.
			"as_of_day":          row.Fields["as_of_day"],
			"identity_count":     row.Fields["identity_count"],
			"churn_loc_30d":      row.Fields["churn_loc_30d"],
			"delivery_units_30d": row.Fields["delivery_units_30d"],
			"area_breakdown": contextfabric.TableFactValue(contextfabric.FactTable{
				Shape: contextfabric.FactTableBreakdown,
				Key:   []string{"as_of_day"},
				Measures: []string{
					"identity_count", "churn_loc_30d", "delivery_units_30d",
					"cycle_p50_30d_hours_avg", "wip_max_30d",
				},
				Grain:             timeBound.effectiveGrain(grainDaily),
				DirectOnlyColumns: landscapeAnswerOmits,
				Rows:              []contextfabric.FactValueRow{row},
			}),
		}
		if daily, ok, omitted := landscapeDailyTable(dailyByTeam[teamID], timeBound); ok {
			fields["daily_landscape"] = daily
			if omitted > 0 {
				// The fact says the cut. It is a cut in the answer only when the
				// question stated the span: with no stated window the series is
				// the newest days by design.
				fields["daily_landscape_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
				if timeBound.statedWindow {
					seriesOmitted += omitted
				}
			}
		}
		if coordinates := coordinatesByTeam[teamID]; len(coordinates) > 0 {
			fields["map_coordinates"] = landscapeCoordinatesTable(coordinates, []string{"map_name", "as_of_day"}, timeBound)
		}
		if commonDay != "" {
			fields["common_live_day"] = contextfabric.StringFactValue(commonDay)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactLandscape, Subject: bySubject[teamID], Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, teamID)},
		})
	}
	return max(rowCount, coordinateRows), seriesOmitted, rejected, nil
}

// readCommonLiveDay is the latest day every one of the read's teams holds live
// rows for at least landscapeMinIdentitiesPerDay identities, or "" when the
// teams' own latest days already agree or they share none in the window read
// (CHAOS-12958). It only feeds the offer the answer makes when the quadrant is
// refused for different days: no stat is read on it, and the facts above stay
// each team's own latest day (CHAOS-4611 ruling 3).
func (p *LandscapeProvider) readCommonLiveDay(ctx context.Context, orgID string, teamOrder []string, byTeam map[string]landscapeAreaRow, timeBound factTimeBound) (string, error) {
	if len(teamOrder) < 2 {
		return "", nil
	}
	first := byTeam[teamOrder[0]].asOfDay
	differ := false
	ownDays := make([]string, 0, len(teamOrder))
	for _, teamID := range teamOrder {
		ownDays = append(ownDays, byTeam[teamID].asOfDay)
		if byTeam[teamID].asOfDay != first {
			differ = true
		}
	}
	if !differ {
		return "", nil
	}
	teamPredicate := ` AND toString(team_id) IN {ids:Array(String)}`
	statement := `SELECT toString(as_of_day)
FROM (
	SELECT as_of_day
	FROM (
		SELECT as_of_day, team_id, uniqExact(identity_id) AS day_identities
		FROM (
` + landscapeRowsSQL(teamPredicate, timeBound) + `
		)
		WHERE is_live
		GROUP BY as_of_day, team_id
		HAVING day_identities >= ` + strconv.Itoa(contextfabric.LandscapeMinIdentities) + `
	)
	GROUP BY as_of_day
	HAVING uniqExact(team_id) = ` + strconv.Itoa(len(teamOrder)) + `
)
ORDER BY as_of_day DESC
LIMIT 1`
	day := ""
	err := p.facts.query(ctx, statement, orgID, teamOrder, func(row contextpacket.ClickHouseRowScanner) error {
		return row.Scan(&day)
	}, timeBound.bindings()...)
	if err != nil {
		return "", err
	}
	slog.DebugContext(ctx, "landscape common live day read", "teams", len(teamOrder), "own_latest_days", contextfabric.SanitizeLogStrings(ownDays), "common_live_day", contextfabric.SanitizeLogAttr(day), "found", day != "")
	return day, nil
}

// queryProjectLandscapePopulation is the project population of
// readProjectLandscape: one row per project from a server-side aggregate over
// the same team rows, so a project whose team rows sort past the shared
// row-capped scan is still known, with its uncapped count of teams that hold a
// live identity.
func (p *LandscapeProvider) queryProjectLandscapePopulation(ctx context.Context, orgID string, ids []string, timeBound factTimeBound) (byProject map[string]int64, order []string, rowCount int, err error) {
	statement := withProjectPopulationLimit(`SELECT project_key, toInt64(count())
FROM (
	SELECT concat(p.provider, ':', p.id) AS project_key, il.team_id AS population_team_key
	FROM ` + projectOwnershipJoinSQL(ownershipValidityPredicate(timeBound)) + `
	INNER JOIN (
` + landscapeOneCopySQL("", timeBound) + `
	) AS il ON il.team_id = p.team_id
	GROUP BY p.id, p.provider, il.team_id
	HAVING sum(il.live_identity) > 0
)
GROUP BY project_key
ORDER BY project_key -- project population`)
	byProject = make(map[string]int64)
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var key string
		var teams int64
		if err := row.Scan(&key, &teams); err != nil {
			return err
		}
		if _, seen := byProject[key]; !seen {
			order = append(order, key)
		}
		byProject[key] = teams
		return nil
	}, timeBound.bindings()...)
	return byProject, order, rowCount, scanErr
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
	statement := withRowLimit(`SELECT concat(p.provider, ':', p.id), il.team_id, toString(max(il.team_latest_day)), ` + landscapeStatsColumns("il.") + `
FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
INNER JOIN (
` + landscapeOneCopySQL("", timeBound) + `
) AS il ON il.team_id = p.team_id
GROUP BY p.id, p.provider, il.team_id
ORDER BY p.provider, p.id, il.team_id`)
	rowCount := 0
	var retractions landscapeRetractions
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
		var retracted int64
		if err := row.Scan(&projectSubjectKey, &teamID, &r.asOfDay, &r.identityCount, &r.churnLOC30d, &r.deliveryUnits30d, &r.cycleP50Avg30dHours, &r.wipMax30d, &retracted); err != nil {
			return err
		}
		if _, ok := bySubject[projectSubjectKey]; !ok {
			return nil
		}
		retractions.add(retracted, r.identityCount == 0)
		if r.identityCount == 0 {
			return nil
		}
		if _, seen := byProject[projectSubjectKey]; !seen {
			projectOrder = append(projectOrder, projectSubjectKey)
		}
		byProject[projectSubjectKey] = append(byProject[projectSubjectKey], projectAreaRow{teamID: teamID, row: r})
		return nil
	}, timeBound.bindings()...)
	retractions.log(ctx, "project")
	if scanErr != nil {
		return rowCount, 0, rejected, scanErr
	}
	coordinateStatement := withRowLimit(`SELECT concat(p.provider, ':', p.id), il.team_id, il.map_name, toString(il.as_of_day), avg(il.x_raw), avg(il.y_raw)
FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
INNER JOIN (
` + landscapeLatestRowsSQL("", timeBound) + `
) AS il ON il.team_id = p.team_id
GROUP BY p.id, p.provider, il.team_id, il.map_name, il.as_of_day
ORDER BY p.provider, p.id, il.team_id, il.map_name`)
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
		return max(rowCount, coordinateRows), 0, rejected, scanErr
	}
	// The population is the aggregate's, never the capped scan's: the scan
	// shares one row budget across every requested project, so a project
	// sorting late can be absent from it. A project the scan reached is kept
	// even when the aggregate does not list it.
	populationByProject, populationOrder, populationRowCount, populationErr := p.queryProjectLandscapePopulation(ctx, orgID, ids, timeBound)
	if populationErr != nil {
		return max(rowCount, coordinateRows), 0, rejected, populationErr
	}
	rowCount = max(rowCount, coordinateRows)
	if populationRowCount >= maxProjectPopulationProbe {
		rowCount = max(rowCount, maxFactRowsPerQuery)
	}
	emissionOrder := append([]string(nil), populationOrder...)
	for _, projectKey := range projectOrder {
		if _, known := populationByProject[projectKey]; !known {
			emissionOrder = append(emissionOrder, projectKey)
		}
	}
	totalOmitted := 0
	for _, projectKey := range emissionOrder {
		rows := byProject[projectKey]
		subject, known := bySubject[projectKey]
		if !known {
			continue
		}
		populationTeams, hasPopulation := populationByProject[projectKey]
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
		if len(teamRows) == 0 && !hasPopulation {
			continue
		}
		teamRows, omitted := capFactValueRows(teamRows)
		teamCount := int64(len(seenTeams))
		if hasPopulation {
			omitted = max(omitted, int(populationTeams)-len(teamRows))
			teamCount = populationTeams
		}
		totalOmitted += omitted
		fields := map[string]contextfabric.FactValue{
			"rollup_basis": contextfabric.StringFactValue("team_project_ownership_landscape"),
			"team_count":   contextfabric.IntegerFactValue(teamCount),
		}
		if len(teamRows) > 0 {
			fields["team_breakdown"] = contextfabric.TableFactValue(contextfabric.FactTable{
				Shape: contextfabric.FactTableBreakdown,
				Key:   []string{"team_id", "as_of_day"},
				Measures: []string{
					"identity_count", "churn_loc_30d", "delivery_units_30d",
					"cycle_p50_30d_hours_avg", "wip_max_30d",
				},
				Grain:             timeBound.effectiveGrain(grainDaily),
				DirectOnlyColumns: landscapeAnswerOmits,
				Rows:              teamRows,
			})
		}
		if omitted > 0 {
			fields["team_breakdown_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
		}
		if coordinates := coordinatesByProject[projectKey]; len(coordinates) > 0 {
			// A cut here is said on the fact. It stays out of the read's omitted
			// count: this table reaches no answer, so a cut in it is no cut in one.
			coordinates, coordinatesOmitted := capFactValueRows(coordinates)
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
	return max(rowCount, coordinateRows), totalOmitted, rejected, nil
}
