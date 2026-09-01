package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// teamPrefix is the CanonicalID prefix this package uses for team subjects,
// mirroring workitems.go's workItemPrefix convention. No provider in this
// repository minted this convention before CHAOS-3780 (devhealthsource has
// no canonical team producer yet -- teams_projects.go's TeamsProjectsSource
// is a documented stub), so this is a new but consistent choice.
const teamPrefix = "team:"

// WorkloadProvider implements contextfabric.FactProvider for FactWorkload
// from capacity_forecasts -- Dev Health Ops' precomputed, team-level Monte
// Carlo capacity forecast (throughput, backlog burn-down percentiles). This
// provider reads the finished forecast only; it never re-runs the
// simulation or derives a workload judgment itself (§19.6.3: Ops stays the
// authority for workload semantics). The table has no person-level column
// at all -- every row is already team-aggregated -- so this satisfies the
// "no person-level workload output" constraint structurally, not by
// filtering.
//
// A team can be forecast under several distinct work_scope_id values at
// once -- live data shows one team with 12 concurrent scopes, computed
// within the same batch, with wildly different throughput/percentile
// values (Codex finding F3, confirmed against real ClickHouse data).
// Grouping by team_id alone silently keeps one scope's forecast and
// discards the other 11 with no record they existed. This provider instead
// partitions by (team_id, work_scope_id) and emits one CanonicalFact per
// scope, naming the scope in the payload, up to the row cap.
//
// CHAOS-4363 widens FactWorkload to add SubjectProject: a project rolls up
// through team_project_ownership -> capacity_forecasts, the same real join
// metrics.go's readProjectMetrics uses for FactMetrics. Monte Carlo
// throughput/percentile stats are never additive across teams (summing two
// independent forecasts' throughput_mean is not a meaningful number), so the
// project-level fact carries every owning team's own latest per-scope
// forecast verbatim in a renderable team_breakdown table, never a summed or
// averaged project-native forecast.
type WorkloadProvider struct{ facts clickhouseFacts }

func newWorkloadProvider(client contextpacket.ClickHouseQueryClient) *WorkloadProvider {
	return &WorkloadProvider{facts: clickhouseFacts{client: client}}
}

func (p *WorkloadProvider) Capability() contextfabric.FactCapability {
	capability := newCapability(contextfabric.FactWorkload, "devhealthfacts.workload", []contextfabric.SubjectKind{
		contextfabric.SubjectTeam, contextfabric.SubjectProject,
	})
	// CHAOS-4633: the project rollup already emits a breakdown
	// (team_breakdown, one row per owning team's own latest forecast).
	// CHAOS-4645, design doc §5.2: team and project also gain a
	// time_series (daily_workload) alongside their existing scalars/breakdown.
	capability.Tables = map[contextfabric.SubjectKind][]contextfabric.FactTableShape{
		contextfabric.SubjectTeam:    {contextfabric.FactTableTimeSeries},
		contextfabric.SubjectProject: {contextfabric.FactTableBreakdown, contextfabric.FactTableTimeSeries},
	}
	capability.EstimatedItems = 12
	return capability
}

func (p *WorkloadProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
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

	if teamSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectTeam); len(teamSubjects) > 0 {
		rowCount, scanErr := p.readTeamWorkload(ctx, orgID, teamSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team workload", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
	}

	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		rowCount, breakdownTruncated, scanErr := p.readProjectWorkload(ctx, orgID, projectSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project workload", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery || breakdownTruncated
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: truncated}, nil
}

// workloadDailyRow is one (team_id or project, day)'s COMBINED
// capacity_forecasts snapshot across every work_scope_id forecast a subject
// has active that day (CHAOS-4645, design doc §5.2: "the dated rows already
// exist in the ClickHouse daily tables these producers read; what is missing
// is a second, declared projection of them"). capacity_forecasts carries no
// `day` column at all (ReplacingMergeTree(computed_at) ORDER BY (org_id,
// forecast_id), no day partition), so the day this row is keyed on is
// DERIVED as toString(toDate(computed_at)) by the query below -- the same
// YYYY-MM-DD string every other producer's own `day` column already
// produces, so FactTable.Validate's instant-parse check accepts it the same
// way.
//
// backlog_size is SUMMED across the day's concurrent scopes -- an additive
// count, valid the same way readProjectFlow already sums a team's own
// scopes.
//
// throughput_mean is SUMMED, not averaged: two independent forecasts' own
// expected throughputs add to one total expected throughput for the
// subject, which is a meaningful number -- unlike a percentile, which has no
// defined meaning under summation (flowDailyRow's own doc comment states
// the identical rule for wip_age/cycle/lead percentiles).
//
// throughput_stddev is COMBINED via sqrt(sum(stddev^2)): variances add
// under independence, so this is the standard combination rule for the sum
// of independent random variables. Concurrent work-scope forecasts for one
// team/project are NOT proven independent -- this is a stated SIMPLIFYING
// ASSUMPTION, not a measured property of the underlying Monte Carlo
// simulations -- but it is the only defensible combination rule available,
// and it is strictly better than either silently dropping the field or
// naively summing the raw stddevs (which is not a valid combination under
// any correlation assumption).
//
// p50_days, insufficient_history and high_variance are DELIBERATELY DROPPED
// from the daily series: there is no valid combination rule for a rounded
// percentile-of-days or a boolean flag across concurrent scopes, matching
// this ticket's own precedent (flowDailyRow drops wip_age/cycle/lead
// percentiles for the identical reason) of omitting a measure with no valid
// combination rule rather than fabricating one.
type workloadDailyRow struct {
	day                      string
	backlogSize              int64
	throughputMeanSum        float64
	throughputStddevCombined float64
}

func (r workloadDailyRow) toFactValueRow() contextfabric.FactValueRow {
	return contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
		"day":               contextfabric.StringFactValue(r.day),
		"backlog_size":      contextfabric.IntegerFactValue(r.backlogSize),
		"throughput_mean":   contextfabric.NumberFactValue(r.throughputMeanSum),
		"throughput_stddev": contextfabric.NumberFactValue(r.throughputStddevCombined),
	}}
}

// workloadDailyTable builds the CHAOS-4645 time_series FactTable off rows
// already fetched by queryTeamWorkloadDailySeries (team) or
// queryProjectWorkloadDailySeries (project) -- both share this exact shape,
// so a single declaration serves both subject kinds (mirrors flow.go's
// flowDailyTable).
func workloadDailyTable(rows []workloadDailyRow, grain contextfabric.TemporalGrain) (contextfabric.FactValue, bool, int) {
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
		Measures: []string{"backlog_size", "throughput_mean", "throughput_stddev"},
		Grain:    grain,
		Rows:     valueRows,
	}), true, omitted
}

// queryTeamWorkloadDailySeries reads capacity_forecasts as a genuine per-day
// series (CHAOS-4645, design doc §5.2), grouped by toDate(computed_at)
// across a team's own concurrent work_scope_id forecasts -- unlike
// readers.ReadTeamWorkload (readTeamWorkload's own base read below), which
// collapses to the single latest forecast per (team_id, work_scope_id) and
// therefore can never back a time_series.
//
// The inner row_number() dedupes only a SAME-DAY rerun per (team_id,
// work_scope_id, day) -- mirroring readers.ReadTeamWorkload's own
// "computed_at DESC, forecast_id DESC" tiebreak (capacity_forecasts carries
// a real per-row unique id, forecast_id, unlike the hash tiebreaks this
// package's other daily-series queries need) -- and the outer GROUP BY
// combines each day's scopes into one row per (team_id, day) using the
// combination rules workloadDailyRow's own doc comment states.
//
// Every column scanned into a Go string is explicitly cast (toString/
// toDate) rather than left to the driver's own type inference: flow.go's
// first version of this exact pattern shipped without that cast and passed
// against the fakeClient test double while failing against real ClickHouse
// (a type mismatch the driver rejects). FINAL mirrors readers.ReadTeamWorkload's
// own defensive use -- it only collapses a re-emitted identical forecast_id,
// never distinct scopes or distinct days, which the row_number() partition
// above is what actually resolves.
func (p *WorkloadProvider) queryTeamWorkloadDailySeries(ctx context.Context, orgID string, ids []string, timeBound factTimeBound) (byTeam map[string][]workloadDailyRow, err error) {
	statement := withRowLimit(`SELECT toString(team_id), toString(toDate(computed_at)), toInt64(sum(backlog_size)), sum(throughput_mean), sqrt(sum(pow(throughput_stddev, 2)))
FROM (
	SELECT ifNull(team_id, '') AS team_id, work_scope_id, computed_at, backlog_size, throughput_mean, throughput_stddev,
		row_number() OVER (PARTITION BY team_id, work_scope_id, toDate(computed_at) ORDER BY computed_at DESC, forecast_id DESC) AS rn
	FROM capacity_forecasts FINAL
	WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)}` + timeBound.timestampPredicate("computed_at") + `
)
WHERE rn = 1
GROUP BY team_id, toDate(computed_at)
ORDER BY team_id, toDate(computed_at) DESC`)
	byTeam = make(map[string][]workloadDailyRow)
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var r workloadDailyRow
		var teamID string
		if err := row.Scan(&teamID, &r.day, &r.backlogSize, &r.throughputMeanSum, &r.throughputStddevCombined); err != nil {
			return err
		}
		byTeam[teamID] = append(byTeam[teamID], r)
		return nil
	}, timeBound.bindings()...)
	return byTeam, scanErr
}

// queryProjectWorkloadDailySeries is queryTeamWorkloadDailySeries' project-
// rollup counterpart (CHAOS-4645, design doc §5.2): the SAME
// projectIdentityJoinSQL/projectIdentityMatchSQL join readProjectWorkload's
// base read (readers.ReadProjectWorkload) uses, summed across EVERY
// contributing team's own concurrent work-scope forecasts FOR EACH DAY,
// using the identical combination rules workloadDailyRow's own doc comment
// states -- backlog_size and throughput_mean summed, throughput_stddev
// combined via sqrt(sum(stddev^2)), never averaged.
func (p *WorkloadProvider) queryProjectWorkloadDailySeries(ctx context.Context, orgID string, ids []string, timeBound factTimeBound) (byProject map[string][]workloadDailyRow, err error) {
	statement := withRowLimit(`SELECT concat(p.provider, ':', p.id), toString(toDate(cf.computed_at)), toInt64(sum(cf.backlog_size)), sum(cf.throughput_mean), sqrt(sum(pow(cf.throughput_stddev, 2)))
FROM ` + projectIdentityJoinSQL() + `
INNER JOIN (
	SELECT team_id, work_scope_id, computed_at, backlog_size, throughput_mean, throughput_stddev,
		row_number() OVER (PARTITION BY team_id, work_scope_id, toDate(computed_at) ORDER BY computed_at DESC, forecast_id DESC) AS rn
	FROM capacity_forecasts FINAL
	WHERE org_id = {org_id:String}` + timeBound.timestampPredicate("computed_at") + `
) AS cf ON ` + projectIdentityMatchSQL("cf", "work_scope_id") + ` AND cf.rn = 1
GROUP BY p.provider, p.id, toDate(cf.computed_at)
ORDER BY p.id, toDate(cf.computed_at) DESC`)
	byProject = make(map[string][]workloadDailyRow)
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var r workloadDailyRow
		var projectKey string
		if err := row.Scan(&projectKey, &r.day, &r.backlogSize, &r.throughputMeanSum, &r.throughputStddevCombined); err != nil {
			return err
		}
		byProject[projectKey] = append(byProject[projectKey], r)
		return nil
	}, timeBound.bindings()...)
	return byProject, scanErr
}

// readTeamWorkload is CHAOS-3780's original capacity_forecasts read. The
// query itself (row_number() tiebreak over computed_at/forecast_id for the
// F3 multi-scope-per-team shape) now lives in readers.ReadTeamWorkload --
// see that function's doc comment for the full tiebreak reasoning. This
// adapter keeps the CanonicalFact-building half, factored out so ReadFacts
// can branch by subject kind the same way metrics.go/health.go already do.
func (p *WorkloadProvider) readTeamWorkload(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := subjectIndex(subjects, teamPrefix)
	rows, err := readers.ReadTeamWorkload(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, err
	}
	// CHAOS-4645, design doc §5.2: a second, ADDITIVE query for the dated
	// series -- computed byte-identically to before this ticket off the SAME
	// base read above; dailyByTeam only ever adds a new "daily_workload"
	// field, never changes an existing one. This provider emits one
	// CanonicalFact PER (team_id, work_scope_id) -- the F3 fix -- so a
	// team's combined-across-scopes daily series is attached to EVERY one of
	// that team's per-scope facts (never only the first), a deliberate
	// choice: it is simple, order-independent (rows is not guaranteed
	// ordered per team), and purely additive to each fact individually. It
	// duplicates the same table across a team's concurrent-scope facts
	// rather than inventing a new "team-summary" fact shape this design does
	// not otherwise have.
	dailyByTeam, seriesErr := p.queryTeamWorkloadDailySeries(ctx, orgID, ids, timeBound)
	if seriesErr != nil {
		return 0, seriesErr
	}
	// codex CHAOS-4645 round-1 P2 (EXECUTED): see flow.go's readTeamFlow
	// identical note -- the daily-series query's own withRowLimit(200) cap,
	// shared across every requested team in one query, must also surface as
	// Truncated.
	dailySeriesRowCount := 0
	for _, dailyRows := range dailyByTeam {
		dailySeriesRowCount += len(dailyRows)
	}
	rowCount := len(rows)
	if dailySeriesRowCount > rowCount {
		rowCount = dailySeriesRowCount
	}
	for _, r := range rows {
		subject, ok := bySubject[r.TeamID]
		if !ok {
			continue
		}
		fields := map[string]contextfabric.FactValue{
			// basis states, in the fact's own structure (not only in this
			// file's doc comment), exactly what slice of "workload" this
			// value is: a precomputed Monte Carlo capacity forecast, never
			// a current/real-time load reading. A synthesizer must not
			// present this value as today's workload.
			"basis":                contextfabric.StringFactValue("capacity_forecast"),
			"throughput_mean":      contextfabric.NumberFactValue(r.ThroughputMean),
			"throughput_stddev":    contextfabric.NumberFactValue(r.ThroughputStddev),
			"insufficient_history": contextfabric.BooleanFactValue(r.InsufficientHistory != 0),
			"high_variance":        contextfabric.BooleanFactValue(r.HighVariance != 0),
			"backlog_size":         contextfabric.IntegerFactValue(r.BacklogSize),
			"computed_at":          contextfabric.StringFactValue(r.ComputedAt),
		}
		if r.WorkScopeID != "" {
			fields["work_scope_id"] = contextfabric.StringFactValue(r.WorkScopeID)
		}
		if r.HasP50Days != 0 {
			fields["forecast_p50_days"] = contextfabric.IntegerFactValue(r.P50Days)
		}
		// codex CHAOS-4645 round-1 P3 (ARGUED, confirmed on read): the scalar
		// forecast is legitimately "instant" (grainExact, ReadFacts' own
		// provider-level Grain above), but daily_workload buckets by
		// toDate(computed_at) -- a genuine daily grain, like every other
		// producer's own daily table -- so its own declared Grain must say
		// so, not overstate the precision as instant.
		if dailyTable, ok, dailyOmitted := workloadDailyTable(dailyByTeam[r.TeamID], timeBound.effectiveGrain(grainDaily)); ok {
			fields["daily_workload"] = dailyTable
			if dailyOmitted > 0 {
				fields["daily_workload_omitted_count"] = contextfabric.IntegerFactValue(int64(dailyOmitted))
			}
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactWorkload, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, r.TeamID)},
		})
	}
	return rowCount, nil
}

// readProjectWorkload rolls FactWorkload up for a project through
// projects -> team_project_ownership -> capacity_forecasts: every team
// owning the project contributes its own latest per-scope forecast,
// verbatim, into one renderable team_breakdown table -- Monte Carlo
// throughput/percentile stats are never summed or averaged across teams
// (see the package doc comment). The query itself now lives in
// readers.ReadProjectWorkload; this adapter does the Go-side
// grouping/breakdown-table construction the reader deliberately leaves to
// its caller.
func (p *WorkloadProvider) readProjectWorkload(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount int, breakdownTruncated bool, err error) {
	ids, bySubject := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, false, nil
	}
	scanned, err := readers.ReadProjectWorkload(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, false, err
	}
	// CHAOS-4645, design doc §5.2: additive, off the SAME project-identity
	// join readProjectWorkload's base read above uses -- never changes an
	// existing field. Workload carries no RankCohort signal at the project
	// level either (workloadWorstDays only ever reads a team-level
	// forecast_p50_days scalar, which this project rollup never emits), so
	// there is nothing to pin for the project subject specifically; the team
	// subject's pin test covers the shared signal.
	dailyByProject, seriesErr := p.queryProjectWorkloadDailySeries(ctx, orgID, ids, timeBound)
	if seriesErr != nil {
		return 0, false, seriesErr
	}
	rowCount = len(scanned)
	// codex CHAOS-4645 round-1 P2 (EXECUTED): see readTeamWorkload's
	// identical note -- the daily-series query's own withRowLimit(200) cap,
	// shared across every requested project in one query, must also surface
	// as Truncated.
	dailySeriesRowCount := 0
	for _, dailyRows := range dailyByProject {
		dailySeriesRowCount += len(dailyRows)
	}
	if dailySeriesRowCount > rowCount {
		rowCount = dailySeriesRowCount
	}
	byProject := make(map[string][]readers.WorkloadProjectRow)
	var projectOrder []string
	for _, r := range scanned {
		if _, ok := bySubject[r.ProjectSubjectKey]; !ok {
			continue
		}
		if _, seen := byProject[r.ProjectSubjectKey]; !seen {
			projectOrder = append(projectOrder, r.ProjectSubjectKey)
		}
		byProject[r.ProjectSubjectKey] = append(byProject[r.ProjectSubjectKey], r)
	}
	for _, projectKey := range projectOrder {
		rows := byProject[projectKey]
		subject := bySubject[projectKey]
		seenTeamScope := make(map[string]bool, len(rows))
		seenTeams := make(map[string]bool, len(rows))
		teamRows := make([]contextfabric.FactValueRow, 0, len(rows))
		evidenceRefIDs := make([]string, 0, len(rows)+1)
		evidenceRefIDs = append(evidenceRefIDs, evidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, projectKey))
		for _, r := range rows {
			dedupeKey := r.TeamID + "\x00" + r.WorkScopeID
			if dedupeTeamRow(seenTeamScope, dedupeKey) {
				continue
			}
			// CHAOS-4521b: an UNATTRIBUTED row (the source's team_id was
			// NULL) is kept with its measurements -- that coverage is
			// genuinely the project's -- but it is NOT a team. It must not
			// be counted in team_count, and it must not mint an evidence
			// ref, which would otherwise be the malformed `acr:v1:team:`
			// with an empty id. Missing is not a team whose name is blank.
			if r.HasTeam != 0 && !dedupeTeamRow(seenTeams, r.TeamID) {
				evidenceRefIDs = append(evidenceRefIDs, evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, r.TeamID))
			}
			rowFields := map[string]contextfabric.FactValue{
				// null, not "": see readProjectReadiness.
				"team_id":              teamIDOrNull(r.HasTeam, r.TeamID),
				"team_name":            stringOrNull(r.TeamName),
				"throughput_mean":      contextfabric.NumberFactValue(r.ThroughputMean),
				"throughput_stddev":    contextfabric.NumberFactValue(r.ThroughputStddev),
				"insufficient_history": contextfabric.BooleanFactValue(r.InsufficientHistory != 0),
				"high_variance":        contextfabric.BooleanFactValue(r.HighVariance != 0),
				"backlog_size":         contextfabric.IntegerFactValue(r.BacklogSize),
				"computed_at":          contextfabric.StringFactValue(r.ComputedAt),
			}
			// CHAOS-4633: work_scope_id is normalized to always-present
			// (null when absent, matching team_id/team_name's own
			// teamIDOrNull/stringOrNull convention two lines above) rather
			// than conditionally omitted -- one team can legitimately
			// contribute more than one row here (dedupeKey includes
			// WorkScopeID), so work_scope_id must be a declared Key column
			// for the table's row identity to stay distinct, and a Key
			// column must be present on every row (FactTable.Validate).
			rowFields["work_scope_id"] = stringOrNull(r.WorkScopeID)
			if r.HasP50Days != 0 {
				rowFields["forecast_p50_days"] = contextfabric.IntegerFactValue(r.P50Days)
			}
			teamRows = append(teamRows, contextfabric.FactValueRow{Fields: rowFields})
		}
		if len(teamRows) == 0 {
			continue
		}
		var omitted int
		teamRows, omitted = capFactValueRows(teamRows)
		breakdownTruncated = breakdownTruncated || omitted > 0
		fields := map[string]contextfabric.FactValue{
			"rollup_basis": contextfabric.StringFactValue("project_work_scope_breakdown"),
			"team_count":   contextfabric.IntegerFactValue(int64(len(seenTeams))),
			// CHAOS-4645 (fixing the CHAOS-4633 F3 debt this file's own doc
			// comment used to flag here): basis is CONSTANT
			// "capacity_forecast" across every row of team_breakdown, so it
			// belongs on the fact as a sibling SCALAR -- exactly Fable F3's
			// ruling -- rather than repeated on every row. CHAOS-4633
			// deliberately kept it as a row column because touching it would
			// have changed Rows' shape mid-additive-P1; this ticket is
			// already touching this producer's Rows shape (daily_workload
			// below), so the debt is paid down now rather than deferred
			// again. This is a DELIBERATE, DISCLOSED behavior change: a
			// consumer reading team_breakdown row Fields["basis"] must now
			// read fact.Fields["basis"] instead.
			"basis": contextfabric.StringFactValue("capacity_forecast"),
			// CHAOS-4633 P1: Key = [team_id, team_name, work_scope_id,
			// computed_at] -- team_id alone does not guarantee distinctness
			// (a team can contribute more than one work_scope_id row), so
			// work_scope_id is part of the declared identity, not a
			// measure.
			"team_breakdown": contextfabric.TableFactValue(contextfabric.FactTable{
				Shape: contextfabric.FactTableBreakdown,
				Key:   []string{"team_id", "team_name", "work_scope_id", "computed_at"},
				// insufficient_history/high_variance are BooleanFactValue
				// flags, not quantities (CHAOS-4680): a boolean is exactly
				// the "not a quantity" case Observations exists for, so
				// they moved out of Measures rather than tripping the
				// numeric-measures rule FactTable.Validate now enforces.
				Measures: []string{
					"throughput_mean", "throughput_stddev", "backlog_size", "forecast_p50_days",
				},
				Observations: []string{"insufficient_history", "high_variance"},
				Grain:        timeBound.effectiveGrain(grainExact),
				Rows:         teamRows,
			}),
		}
		// codex CHAOS-4645 round-1 P3: see readTeamWorkload's identical note.
		if dailyTable, ok, dailyOmitted := workloadDailyTable(dailyByProject[projectKey], timeBound.effectiveGrain(grainDaily)); ok {
			fields["daily_workload"] = dailyTable
			if dailyOmitted > 0 {
				fields["daily_workload_omitted_count"] = contextfabric.IntegerFactValue(int64(dailyOmitted))
			}
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactWorkload, Subject: subject, Fields: fields,
			EvidenceRefIDs: evidenceRefIDs,
		})
	}
	return rowCount, breakdownTruncated, nil
}
