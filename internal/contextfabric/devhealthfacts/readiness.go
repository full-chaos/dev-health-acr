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

// ReadinessProvider implements contextfabric.FactProvider for FactReadiness
// from estimate_coverage_metrics_daily -- Dev Health Ops' precomputed daily
// backlog estimate-coverage ratio (estimated vs. unestimated backlog items)
// for one team's work scope. This is a narrow, honest slice of "readiness":
// it answers "how much of this team's backlog is estimated", not a general
// release/ship-readiness judgment -- no broader canonical readiness signal
// exists in ClickHouse today, and this provider does not invent one
// (§19.6.3). The ratio itself is read exactly as Ops computed it; this
// provider never recomputes estimated_count/backlog_size into a ratio of
// its own.
//
// estimate_coverage_metrics_daily's own sort key is
// (org_id, day, provider, work_scope_id, team_id) -- two different source
// providers (live data: gitlab, linear) can report against the same
// work_scope_id string, so provider is part of this provider's partition
// key too (Codex finding F4), not folded away. The table is
// ReplacingMergeTree(computed_at): FINAL collapses an exact-key rerun, and
// row_number() ORDER BY day DESC, computed_at DESC (not day alone) still
// resolves the case where FINAL has not yet merged a same-day recompute.
// CHAOS-4363 widens FactReadiness to add SubjectProject: a project rolls up
// through team_project_ownership -> estimate_coverage_metrics_daily, the
// same real join metrics.go's readProjectMetrics uses for FactMetrics.
// estimate_coverage_metrics_daily partitions by (team, work_scope_id,
// provider), so summing estimated_count/backlog_size across teams that track
// DIFFERENT work scopes would mix unrelated backlogs into one meaningless
// total -- the project-level fact instead carries every owning team's own
// latest per-scope coverage row verbatim in a renderable team_breakdown
// table, never a summed or averaged project-native ratio.
type ReadinessProvider struct{ facts clickhouseFacts }

func newReadinessProvider(client contextpacket.ClickHouseQueryClient) *ReadinessProvider {
	return &ReadinessProvider{facts: clickhouseFacts{client: client}}
}

func (p *ReadinessProvider) Capability() contextfabric.FactCapability {
	capability := newCapability(contextfabric.FactReadiness, "devhealthfacts.readiness", []contextfabric.SubjectKind{
		contextfabric.SubjectTeam, contextfabric.SubjectProject,
	})
	// CHAOS-4645, design doc §5.2: team and project both gain a time_series
	// (daily_readiness) alongside the scalars/breakdown they already emit --
	// additive, mirroring flow.go's identical Capability() widening for
	// FactFlow.
	capability.Tables = map[contextfabric.SubjectKind][]contextfabric.FactTableShape{
		contextfabric.SubjectTeam:    {contextfabric.FactTableTimeSeries},
		contextfabric.SubjectProject: {contextfabric.FactTableBreakdown, contextfabric.FactTableTimeSeries},
	}
	capability.EstimatedItems = 20
	return capability
}

func (p *ReadinessProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
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
	rejectedCount := 0

	if teamSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectTeam); len(teamSubjects) > 0 {
		rowCount, rowsOmitted, rejected, scanErr := p.readTeamReadiness(ctx, orgID, teamSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team readiness", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery || rowsOmitted > 0
		rejectedCount += rejected
	}

	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		rowCount, rejected, breakdownTruncated, scanErr := p.readProjectReadiness(ctx, orgID, projectSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project readiness", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery || breakdownTruncated
		rejectedCount += rejected
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	result := contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: truncated}
	applySubjectShapeRejection(&result, "devhealthfacts.readiness", contextfabric.FactReadiness, rejectedCount)
	return result, nil
}

// readinessDailyRow is one (subject, day)'s SUMMED
// estimate_coverage_metrics_daily aggregate across every (provider,
// work_scope_id) scope the subject had that day (CHAOS-4645, design doc
// §5.2) -- the same "additive counts summed within one day, never a rate
// averaged" rule readProjectReadiness's own doc comment states for the
// cross-team rollup, applied here to a single subject's own concurrent
// scopes for one day. estimate_coverage_ratio is NOT read off the source
// table for this shape: it is recomputed in Go from the day's SUMMED
// estimated_count/unestimated_count (never carried forward from any one
// scope's own ratio, which would not be additive across scopes), and
// omitted -- never emitted as NaN/Inf -- when the day's summed denominator
// is zero.
type readinessDailyRow struct {
	day                                           string
	estimatedCount, unestimatedCount, backlogSize int64
}

func (r readinessDailyRow) toFactValueRow() contextfabric.FactValueRow {
	fields := map[string]contextfabric.FactValue{
		"day":               contextfabric.StringFactValue(r.day),
		"estimated_count":   contextfabric.IntegerFactValue(r.estimatedCount),
		"unestimated_count": contextfabric.IntegerFactValue(r.unestimatedCount),
		"backlog_size":      contextfabric.IntegerFactValue(r.backlogSize),
	}
	if denominator := r.estimatedCount + r.unestimatedCount; denominator > 0 {
		fields["estimate_coverage_ratio"] = contextfabric.NumberFactValue(float64(r.estimatedCount) / float64(denominator))
	}
	return contextfabric.FactValueRow{Fields: fields}
}

// readinessDailyTable builds the CHAOS-4645 time_series FactTable off rows
// already fetched by queryTeamReadinessDailySeries (team) or
// queryProjectReadinessDailySeries (project) -- both share this exact shape,
// so a single declaration serves both subject kinds, mirroring flow.go's
// flowDailyTable.
func readinessDailyTable(rows []readinessDailyRow, grain contextfabric.TemporalGrain) (contextfabric.FactValue, bool, int) {
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
		Measures: []string{"estimated_count", "unestimated_count", "backlog_size", "estimate_coverage_ratio"},
		Grain:    grain,
		Rows:     valueRows,
	}), true, omitted
}

// queryTeamReadinessDailySeries reads estimate_coverage_metrics_daily as a
// genuine per-day series (CHAOS-4645, design doc §5.2), unlike
// readers.ReadTeamReadiness, which collapses to the single latest row per
// (team_id, work_scope_id, provider) and therefore can never back a
// time_series. This is raw SQL rather than a readers.* delegation because
// the pinned dev-health-go module (v0.5.5) has no daily-series reader to
// call -- widening it is out of this acr-only ticket's scope, the same
// boundary flow.go's own daily-series queries respect.
//
// The inner row_number() dedupes only a SAME-DAY rerun per (team_id,
// provider, work_scope_id, day) -- mirroring readers.ReadTeamReadiness's own
// tiebreak discipline (day/computed_at/cityHash64) -- and the outer GROUP BY
// sums each day's scopes into one row per (team_id, day), so a
// multi-scope team still yields exactly one time_series point per day.
// Every column scanned into a Go string is wrapped in toString(...): a raw
// Date/Nullable(String) column scanned straight into a Go string works
// against fakeClient but fails against real ClickHouse, which enforces type
// matching (the exact bug flow.go's queryTeamFlowDailySeries was found and
// fixed for).
func (p *ReadinessProvider) queryTeamReadinessDailySeries(ctx context.Context, orgID string, ids []string, timeBound factTimeBound) (byTeam map[string][]readinessDailyRow, err error) {
	statement := withRowLimit(`SELECT toString(team_id), toString(day), toInt64(sum(estimated_count)), toInt64(sum(unestimated_count)), toInt64(sum(backlog_size))
FROM (
	SELECT team_id, provider, work_scope_id, day, estimated_count, unestimated_count, backlog_size,
		row_number() OVER (PARTITION BY team_id, provider, work_scope_id, day ORDER BY computed_at DESC, cityHash64(tuple(estimated_count, unestimated_count, backlog_size)) DESC) AS rn
	FROM estimate_coverage_metrics_daily FINAL
	WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
)
WHERE rn = 1
GROUP BY team_id, day
ORDER BY team_id, day DESC`)
	byTeam = make(map[string][]readinessDailyRow)
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var r readinessDailyRow
		var teamID string
		if err := row.Scan(&teamID, &r.day, &r.estimatedCount, &r.unestimatedCount, &r.backlogSize); err != nil {
			return err
		}
		byTeam[teamID] = append(byTeam[teamID], r)
		return nil
	}, timeBound.bindings()...)
	return byTeam, scanErr
}

// readTeamReadiness is CHAOS-3780's original estimate_coverage_metrics_daily
// read. The query itself (row_number() tiebreak over day/computed_at/
// cityHash64 for the multi-(work_scope, provider)-per-team shape) now lives
// in readers.ReadTeamReadiness -- see that function's doc comment for the
// full tiebreak reasoning. This adapter keeps the CanonicalFact-building
// half, factored out so ReadFacts can branch by subject kind the same way
// metrics.go/health.go already do.
func (p *ReadinessProvider) readTeamReadiness(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount int, omittedRows int, rejected int, err error) {
	ids, bySubject, rejected := subjectIndex(subjects, teamPrefix)
	rows, err := readers.ReadTeamReadiness(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, 0, rejected, err
	}
	// CHAOS-4645, design doc §5.2: a second, ADDITIVE query for the dated
	// series -- every existing scalar field below is computed byte-identically
	// to before this ticket, off the SAME readers.ReadTeamReadiness result;
	// dailyByTeam only ever adds a new "daily_readiness" field, never changes
	// an existing one, so RankCohort's readinessGapSignal (which reads
	// "estimate_coverage_ratio" off the fact's TOP level, never off a row)
	// cannot observe a difference -- pinned by
	// TestRankCohortReadinessUntouchedByDailySeriesField.
	dailyByTeam, seriesErr := p.queryTeamReadinessDailySeries(ctx, orgID, ids, timeBound)
	if seriesErr != nil {
		return 0, 0, rejected, seriesErr
	}
	// codex CHAOS-4645 round-1 P2 (EXECUTED): see flow.go's readTeamFlow
	// identical note -- the daily-series query's own withRowLimit(200) cap,
	// shared across every requested team in one query, must also surface as
	// Truncated.
	dailySeriesRowCount := 0
	for _, dailyRows := range dailyByTeam {
		dailySeriesRowCount += len(dailyRows)
	}
	rowCount = len(rows)
	if dailySeriesRowCount > rowCount {
		rowCount = dailySeriesRowCount
	}
	// readTeamReadiness mints ONE CanonicalFact per (team, work_scope,
	// provider) row -- unlike flow.go's readTeamFlow, which pre-aggregates
	// one fact per team. daily_readiness is already summed ACROSS a team's
	// scopes (queryTeamReadinessDailySeries' own doc comment), so it is
	// attached to only the FIRST fact minted for each team: attaching the
	// identical team-wide table to every one of that team's per-scope facts
	// would duplicate it N times for an N-scope team, without adding
	// information -- a consumer reading the team's FactReadiness facts finds
	// it on the first one, the same "first-seen" discipline this package
	// uses elsewhere (subjectIndex, queryTeamScopeRows' teamOrder).
	dailyAttached := make(map[string]bool, len(dailyByTeam))
	for _, r := range rows {
		subject, ok := bySubject[r.TeamID]
		if !ok {
			continue
		}
		fields := map[string]contextfabric.FactValue{
			// basis states, in the fact's own structure (not only in this
			// file's doc comment), exactly what slice of "readiness" this
			// value is: backlog estimate coverage, never a general
			// release/ship-readiness verdict. A synthesizer must not
			// present this value as answering a broader readiness
			// question than it does.
			"basis":             contextfabric.StringFactValue("estimate_coverage"),
			"work_scope_id":     stringOrNull(r.WorkScopeID),
			"provider":          stringOrNull(r.Provider),
			"day":               contextfabric.StringFactValue(r.Day),
			"estimated_count":   contextfabric.IntegerFactValue(r.EstimatedCount),
			"unestimated_count": contextfabric.IntegerFactValue(r.UnestimatedCount),
			"backlog_size":      contextfabric.IntegerFactValue(r.BacklogSize),
		}
		if r.HasRatio != 0 {
			fields["estimate_coverage_ratio"] = contextfabric.NumberFactValue(r.Ratio)
		}
		if !dailyAttached[r.TeamID] {
			// codex CHAOS-4645 round-1 P2 (ARGUED, confirmed on read):
			// readiness was the one producer of the four discarding
			// capFactValueRows' own omitted count for its daily table
			// instead of reporting it -- health/workload/flow all already
			// surface a "daily_<x>_omitted_count" field and fold it into
			// Truncated (see flow.go's readTeamFlow). Matched here.
			if dailyTable, ok, dailyOmitted := readinessDailyTable(dailyByTeam[r.TeamID], timeBound.effectiveGrain(grainDaily)); ok {
				fields["daily_readiness"] = dailyTable
				dailyAttached[r.TeamID] = true
				omittedRows += dailyOmitted
				if dailyOmitted > 0 {
					fields["daily_readiness_omitted_count"] = contextfabric.IntegerFactValue(int64(dailyOmitted))
				}
			}
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactReadiness, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, r.TeamID)},
		})
	}
	return rowCount, omittedRows, rejected, nil
}

// queryProjectReadinessDailySeries is queryTeamReadinessDailySeries'
// project-rollup counterpart (CHAOS-4645, design doc §5.2): the SAME
// project-identity join readProjectReadiness's own doc comment explains
// (projects -> estimate_coverage_metrics_daily via work_scope_id, no
// team-ownership hop -- CHAOS-4521b), summed across every contributing
// team's rows FOR EACH DAY -- additive counts only, matching
// readProjectReadiness's own "never summed across teams" rule for a single
// day's snapshot the same way it never sums a scope's LATEST row across
// teams: within one day, every owning team's own scope rows are genuinely
// part of the same project's coverage picture, so a daily total is honest
// where team_breakdown's cross-scope total would not be.
func (p *ReadinessProvider) queryProjectReadinessDailySeries(ctx context.Context, orgID string, ids []string, timeBound factTimeBound) (byProject map[string][]readinessDailyRow, err error) {
	statement := withRowLimit(`SELECT concat(p.provider, ':', p.id), toString(ec.day), toInt64(sum(ec.estimated_count)), toInt64(sum(ec.unestimated_count)), toInt64(sum(ec.backlog_size))
FROM ` + projectIdentityJoinSQL() + `
INNER JOIN (
	SELECT team_id, provider, work_scope_id, day, estimated_count, unestimated_count, backlog_size,
		row_number() OVER (PARTITION BY team_id, provider, work_scope_id, day ORDER BY computed_at DESC, cityHash64(tuple(estimated_count, unestimated_count, backlog_size)) DESC) AS rn
	FROM estimate_coverage_metrics_daily FINAL
	WHERE org_id = {org_id:String}` + timeBound.dayPredicate("day") + `
) AS ec ON ` + projectIdentityMatchSQL("ec", "work_scope_id") + ` AND ec.rn = 1
GROUP BY p.provider, p.id, ec.day
ORDER BY p.id, ec.day DESC`)
	byProject = make(map[string][]readinessDailyRow)
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var r readinessDailyRow
		var projectKey string
		if err := row.Scan(&projectKey, &r.day, &r.estimatedCount, &r.unestimatedCount, &r.backlogSize); err != nil {
			return err
		}
		byProject[projectKey] = append(byProject[projectKey], r)
		return nil
	}, timeBound.bindings()...)
	return byProject, scanErr
}

// readProjectReadiness rolls FactReadiness up for a project through
// projects -> team_project_ownership -> estimate_coverage_metrics_daily:
// every team owning the project contributes its own latest per-(work_scope,
// provider) coverage row, verbatim, into one renderable team_breakdown
// table -- see the package doc comment for why estimate/backlog counts are
// never summed across teams here. The query itself now lives in
// readers.ReadProjectReadiness; this adapter does the Go-side
// grouping/breakdown-table construction the reader deliberately leaves to
// its caller.
func (p *ReadinessProvider) readProjectReadiness(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount int, rejected int, breakdownTruncated bool, err error) {
	ids, bySubject, rejected := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, rejected, false, nil
	}
	scanned, err := readers.ReadProjectReadiness(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, rejected, false, err
	}
	// CHAOS-4645, design doc §5.2: additive, off the SAME project-identity
	// join -- never changing an existing field.
	//
	// UPDATED for CHAOS-4681: this rollup's top-level Fields NOW sets
	// "estimate_coverage_ratio" (the freshest daily_readiness day, copied in
	// below under its own field name), so the claim this comment used to
	// make here no longer holds. readinessGapSignal's numberField read of
	// that key is subject-kind-blind, and project cohorts ARE constructed
	// in production (graphrank's DiscoveredCohort, for a frame declaring
	// member_kind "project"; before CHAOS-4736 the same cohorts arrived
	// through a keyword match on a "project"/"initiative" question -- an
	// earlier version of this comment
	// claimed otherwise; that claim was wrong, caught in codex round 1, and
	// no test by the name this comment used to cite ever existed).
	//
	// Unlike health.go's project rollup (which deliberately does NOT copy
	// its Observation field, "severity", to avoid engaging
	// healthRiskSignal), estimate_coverage_ratio here IS the declared
	// Measure this ticket exists to expose -- there is no narrower copy
	// that both satisfies the ticket and avoids readinessGapSignal. A
	// project cohort's readiness-gap signal, previously always
	// `available=false`, now correctly reports the same worst-covered-scope
	// gap a team cohort already gets. This is accepted as the intended
	// generalization, not a defect: readinessGapSignal was always written
	// to be subject-kind-blind, and this ticket is precisely what was
	// missing for it to work on project subjects too.
	dailyByProject, seriesErr := p.queryProjectReadinessDailySeries(ctx, orgID, ids, timeBound)
	if seriesErr != nil {
		return 0, rejected, false, seriesErr
	}
	rowCount = len(scanned)
	// codex CHAOS-4645 round-1 P2 (EXECUTED): see readTeamReadiness's
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
	byProject := make(map[string][]readers.ReadinessProjectRow)
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
			dedupeKey := r.TeamID + "\x00" + r.WorkScopeID + "\x00" + r.Provider
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
				// null, not "": the row says "no team recorded", which is a
				// different claim from a team with an empty id.
				"team_id":           teamIDOrNull(r.HasTeam, r.TeamID),
				"team_name":         stringOrNull(r.TeamName),
				"work_scope_id":     stringOrNull(r.WorkScopeID),
				"provider":          stringOrNull(r.Provider),
				"day":               contextfabric.StringFactValue(r.Day),
				"estimated_count":   contextfabric.IntegerFactValue(r.EstimatedCount),
				"unestimated_count": contextfabric.IntegerFactValue(r.UnestimatedCount),
				"backlog_size":      contextfabric.IntegerFactValue(r.BacklogSize),
			}
			if r.HasRatio != 0 {
				rowFields["estimate_coverage_ratio"] = contextfabric.NumberFactValue(r.Ratio)
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
			// CHAOS-4645 F3 FIX: "basis" moves from a per-row column
			// (constant "estimate_coverage" on every team_breakdown row) to
			// this sibling scalar field on the fact itself -- the Fable F3
			// rule verbatim (model.go's FactTable.Validate error, and
			// design doc §5.1: "a column constant across the whole table
			// belongs in a sibling scalar field on the fact, never in the
			// row"). CHAOS-4633 deliberately deferred this fix to avoid
			// changing Rows' shape mid-additive-P1; CHAOS-4645 is already
			// touching this producer's Rows shape (daily_readiness below),
			// so the debt is paid down here rather than deferred again.
			// This changes WHERE the value lives, never what it says or how
			// many rows team_breakdown has.
			"basis": contextfabric.StringFactValue("estimate_coverage"),
			// CHAOS-4633 P1: Key = [team_id, team_name, work_scope_id,
			// provider, day] -- "basis" dropped (see above); dedupeKey above
			// already partitions on (team_id, work_scope_id, provider), so
			// Key distinctness is unaffected by its removal.
			"team_breakdown": contextfabric.TableFactValue(contextfabric.FactTable{
				Shape: contextfabric.FactTableBreakdown,
				Key:   []string{"team_id", "team_name", "work_scope_id", "provider", "day"},
				Measures: []string{
					"estimated_count", "unestimated_count", "backlog_size", "estimate_coverage_ratio",
				},
				Grain: timeBound.effectiveGrain(grainDaily),
				Rows:  teamRows,
			}),
		}
		// codex CHAOS-4645 round-1 P2: see readTeamReadiness's identical note.
		if dailyTable, ok, dailyOmitted := readinessDailyTable(dailyByProject[projectKey], timeBound.effectiveGrain(grainDaily)); ok {
			fields["daily_readiness"] = dailyTable
			if dailyOmitted > 0 {
				breakdownTruncated = true
				fields["daily_readiness_omitted_count"] = contextfabric.IntegerFactValue(int64(dailyOmitted))
			}
			// CHAOS-4681: same gap and same fix as readProjectWorkload's
			// identical note -- a project's top-level fields carried no
			// scalar matching any of daily_readiness's declared Measures, so
			// a trend could never be claimed here. dailyByProject's own
			// ORDER BY ... DESC makes index 0 the freshest day; copied under
			// its own field names, metrics.go's readRepositoryMetrics idiom.
			for name, value := range dailyByProject[projectKey][0].toFactValueRow().Fields {
				fields[name] = value
			}
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactReadiness, Subject: subject, Fields: fields,
			EvidenceRefIDs: evidenceRefIDs,
		})
	}
	return rowCount, rejected, breakdownTruncated, nil
}
