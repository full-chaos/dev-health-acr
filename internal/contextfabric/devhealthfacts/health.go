package devhealthfacts

import (
	"context"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// healthSeverityFreshnessWindowDays bounds how many days a scope's most
// recent KNOWN compounding-risk band may trail the request's own as-of
// instant before a band-reading query treats it as stale rather than
// current (CHAOS-5952). compounding_risk_daily's writer records severity
// as the closed value 'unknown' on any day it lacks enough first-reviewed
// PRs to compute review_latency_p90h; a scope with a real known band a few
// days earlier must still be served that band, disclosed with the day it
// is FROM, rather than the caller reading a blank "unknown" that a
// slightly wider window would have resolved. One named constant, so every
// consumer (a single scope's own latest row, a project's risk_breakdown
// rows, and the project-level severity aggregate) applies the identical
// window and discloses the identical number via
// severity_freshness_window_days, never a second, undisclosed copy.
const healthSeverityFreshnessWindowDays = 14

// freshnessAsOfDateSQL renders the DATE this request's freshness window
// measures against: the requested historical instant when one was named,
// or ClickHouse's own current instant when the axis is current. Mirrors
// ownershipValidityPredicate's identical active/inactive branch (shared.go)
// -- the freshness window trails the REQUEST's own reference instant, a
// historical as-of bound included, never a second, independently taken
// instant.
func freshnessAsOfDateSQL(timeBound factTimeBound) string {
	if timeBound.active {
		return "toDate({" + boundEndParam + ":DateTime64(6,'UTC')})"
	}
	return "toDate(now64(3))"
}

// freshnessIsKnownSQL is TRUE for any row whose severity is a real band --
// anything but the closed 'unknown' value. columnExpr is an internal Go
// string (a column or aliased-column reference, never caller data), so
// inlining it is the same safe pattern withRowLimit's own doc comment
// already establishes for this package's other internal literals.
func freshnessIsKnownSQL(columnExpr string) string {
	return "(" + columnExpr + " != 'unknown')"
}

// freshnessIsFreshSQL is TRUE for a row whose own day sits within
// healthSeverityFreshnessWindowDays of freshnessAsOfDateSQL, inclusive of
// the boundary day itself, and never after it (as-of honesty: a row dated
// after the request's own reference instant cannot back an answer for
// that instant). It says nothing about whether the row's severity is
// known -- callers combine it with freshnessIsKnownSQL, because a stale
// KNOWN row and a fresh UNKNOWN row are different disclosed outcomes (see
// healthSeverityUnavailableReasonStaleBeyondWindow).
func freshnessIsFreshSQL(dayColumnExpr string, timeBound factTimeBound) string {
	asOf := freshnessAsOfDateSQL(timeBound)
	return "(" + dayColumnExpr + " <= " + asOf + " AND " + dayColumnExpr + " >= (" + asOf + " - " + strconv.Itoa(healthSeverityFreshnessWindowDays) + "))"
}

// HealthProvider implements contextfabric.FactProvider for FactHealth from
// compounding_risk_daily -- the one canonical, precomputed-by-Ops risk/health
// signal in ClickHouse today. Dev Health Ops computes compounding_risk and
// severity nightly from a fixed, documented formula (see the table's own
// w_churn/w_complexity/w_ownership/w_review/threshold_* columns, which this
// provider never reads or reinterprets); this provider only ever reads the
// already-computed severity/score, never recomputes a health rule -- Ops
// stays the sole authority for what "healthy" means (§19.6.3/§19.11).
//
// compounding_risk_daily carries both repo-scoped and team-scoped rows
// (scope='repo'/'team'), so this provider supports both subject kinds, the
// same dual-block shape identity.go's IdentityProvider uses for repository +
// work item.
//
// Live data shows up to 86 rows sharing the IDENTICAL computed_at for one
// (scope, scope_id) key (Codex finding F2, confirmed against real
// ClickHouse data) -- an independent argMax(severity, computed_at) and
// argMax(compounding_risk, computed_at) in the same query have no guarantee
// of resolving that tie to the same underlying row, so this provider uses
// row_number() OVER (... ORDER BY day DESC, computed_at DESC), picking rn=1
// and scanning every field off that ONE row.
//
// day DESC, computed_at DESC is still not a TOTAL order given that same
// 86-way computed_at tie (Codex round-2 finding M1): compounding_risk_daily
// has no per-row unique id, so without a further tiebreaker row_number()
// could pick a different tied row on different executions of the identical
// query. cityHash64 of severity/compounding_risk is the last ORDER BY
// term -- arbitrary among an exact tie, but stable, so the same row wins
// every time.
// CHAOS-4363 widens FactHealth to add SubjectProject: a project rolls up
// compounding_risk_daily two ways at once, both via real ownership joins,
// never the CHAOS-4099 activity-proxy route (that route's own project-origin
// entry in fact_scope.go's factScopeEligibility, targeting SubjectRepository,
// stays policy `none` -- it names a DIFFERENT, still-nonexistent path: giving
// a project question access to the ACTUAL repository subjects underneath
// it, not this rollup):
//
//   - team layer: project -> team_project_ownership -> compounding_risk_daily
//     (scope='team'), the same join metrics.go's readProjectMetrics uses.
//   - repo layer: project -> team_project_ownership -> team_repo_ownership ->
//     compounding_risk_daily (scope='repo'), one hop further -- a project's
//     repositories are reached through the teams that own it, since there is
//     no direct project->repository ownership table.
//
// Both layers land in one renderable risk_breakdown table per project,
// tagged by `scope` ('team' or 'repo'), never summed or averaged into a
// single project-level risk score.
type HealthProvider struct{ facts clickhouseFacts }

func newHealthProvider(client contextpacket.ClickHouseQueryClient) *HealthProvider {
	return &HealthProvider{facts: clickhouseFacts{client: client}}
}

func (p *HealthProvider) Capability() contextfabric.FactCapability {
	capability := newCapability(contextfabric.FactHealth, "devhealthfacts.health", []contextfabric.SubjectKind{
		contextfabric.SubjectRepository, contextfabric.SubjectTeam, contextfabric.SubjectProject,
	})
	// CHAOS-4633: risk_rules (repo and team, readScope) and risk_breakdown
	// (project, readProjectHealth) are both breakdowns -- neither is
	// ordered by time, and neither has a natural ranking order today.
	// CHAOS-4645, design doc §5.2: team and project ALSO gain a time_series
	// (daily_health) alongside their existing breakdown -- additive, so the
	// breakdown shape stays declared too. Repository is unchanged (out of
	// this ticket's scope, per the design doc's own "team AND project
	// subjects" wording) and stays breakdown-only.
	capability.Tables = map[contextfabric.SubjectKind][]contextfabric.FactTableShape{
		contextfabric.SubjectRepository: {contextfabric.FactTableBreakdown},
		contextfabric.SubjectTeam:       {contextfabric.FactTableBreakdown, contextfabric.FactTableTimeSeries},
		contextfabric.SubjectProject:    {contextfabric.FactTableBreakdown, contextfabric.FactTableTimeSeries},
	}
	capability.EstimatedItems = 12
	return capability
}

func (p *HealthProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
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
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.health", contextfabric.FactHealth, rejectedCount)
		}
	}()

	repoIDs, repoBySubject, repoRejected := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectRepository), repositoryPrefix)
	rejectedCount += repoRejected
	if len(repoIDs) > 0 {
		rowCount, dailyOmitted, scanErr := p.readScope(ctx, orgID, "repo", repoIDs, repoBySubject, contractsv1.ContextFabricEvidenceEntityRepository, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query repository health", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery || dailyOmitted > 0
	}

	teamIDs, teamBySubject, teamRejected := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectTeam), teamPrefix)
	rejectedCount += teamRejected
	if len(teamIDs) > 0 {
		rowCount, dailyOmitted, scanErr := p.readScope(ctx, orgID, "team", teamIDs, teamBySubject, contractsv1.ContextFabricEvidenceEntityTeam, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team health", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery || dailyOmitted > 0
	}

	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		rowCount, rejected, breakdownTruncated, scanErr := p.readProjectHealth(ctx, orgID, projectSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project health", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery || breakdownTruncated
		rejectedCount += rejected
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	// CHAOS-4521b: this source has no project dimension, so an all-project
	// read that came back empty says something more specific than "no rows".
	retentionReason = explainTeamScopedProjectAbsence(timeBound, state, retentionReason, query.Subjects)
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: truncated}
	return result, nil
}

// readScope runs the compounding_risk_daily query for one scope ('repo' or
// 'team'), appending a CanonicalFact per matched subject into facts. scope is
// an internal Go string literal (never caller-supplied), so it is safe to
// inline into the statement the same way withRowLimit's maxFactRowsPerQuery
// is.
// riskRuleComponent names one compounding_risk_daily formula term, in the
// table's own declared column order (CHAOS-4418: risk_rules Rows below
// reports these as one row per component instead of leaving the formula's
// own inputs invisible behind the single combined compounding_risk score).
type riskRuleComponent struct {
	signal, normColumn, weightColumn string
}

// riskRuleComponents mirrors compounding_risk_daily's own schema exactly
// (churn_norm/complexity_norm/ownership_norm/review_norm paired with
// w_churn/w_complexity/w_ownership/w_review) -- never a second,
// independently maintained list of which signals make up the score.
var riskRuleComponents = []riskRuleComponent{
	{signal: "churn", normColumn: "churn_norm", weightColumn: "w_churn"},
	{signal: "complexity", normColumn: "complexity_norm", weightColumn: "w_complexity"},
	{signal: "ownership", normColumn: "ownership_norm", weightColumn: "w_ownership"},
	{signal: "review", normColumn: "review_norm", weightColumn: "w_review"},
}

// riskRuleValue is one riskRuleComponents entry's scanned value for one
// scope_id row.
type riskRuleValue struct {
	hasNorm bool
	norm    float64
	weight  float64
}

// healthDailyRow is one (scope_id, day)'s compounding_risk_daily row
// (CHAOS-4645, design doc §5.2) -- unlike readScope's rn=1-per-scope_id
// read, this dedupes only WITHIN a day (same tiebreak discipline, one level
// finer), so every day the org actually computed survives as its own row.
// severity is carried alongside compounding_risk (never dropped) because
// compounding_risk can be null on a day severity is still recorded for --
// the same has/value split readScope's own "compounding_risk" scalar uses.
type healthDailyRow struct {
	day      string
	severity string
	hasRisk  bool
	risk     float64
}

func (r healthDailyRow) toFactValueRow() contextfabric.FactValueRow {
	fields := map[string]contextfabric.FactValue{
		"day":      contextfabric.StringFactValue(r.day),
		"severity": stringOrNull(r.severity),
	}
	if r.hasRisk {
		fields["compounding_risk"] = contextfabric.NumberFactValue(r.risk)
	}
	return contextfabric.FactValueRow{Fields: fields}
}

// healthDailyTable builds the CHAOS-4645 time_series FactTable off rows
// already fetched by queryTeamHealthDailySeries (team) or
// queryProjectHealthDailySeries (project) -- both share this exact shape
// (a single "worst/latest compounding_risk + severity for the day" row),
// so one declaration serves both subject kinds, mirroring flow.go's
// flowDailyTable.
func healthDailyTable(rows []healthDailyRow, grain contextfabric.TemporalGrain) (contextfabric.FactValue, bool, int) {
	if len(rows) == 0 {
		return contextfabric.FactValue{}, false, 0
	}
	valueRows := make([]contextfabric.FactValueRow, 0, len(rows))
	for _, r := range rows {
		valueRows = append(valueRows, r.toFactValueRow())
	}
	valueRows, omitted := capFactValueRows(valueRows)
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape: contextfabric.FactTableTimeSeries,
		Key:   []string{"day"},
		// severity is a per-day categorical OBSERVATION of this one
		// subject (CHAOS-4680), not a quantity -- it belongs beside
		// compounding_risk, not among the measures a trend can plot.
		// Declaring it a Measure would have blocked the numeric-only rule
		// FactTable.Validate now enforces on every Measures column.
		Measures:     []string{"compounding_risk"},
		Observations: []string{"severity"},
		Grain:        grain,
		Rows:         valueRows,
	}), true, omitted
}

// queryTeamHealthDailySeries reads compounding_risk_daily as a genuine
// per-day series for scope='team' (CHAOS-4645, design doc §5.2: "the dated
// rows already exist in the ClickHouse daily tables these producers read;
// what is missing is a second, declared projection of them") -- unlike
// readScope, which collapses to the single latest row per scope_id and can
// therefore never back a time_series. The row_number() here dedupes only a
// SAME-DAY rerun per (scope_id, day) -- mirroring readScope's own
// PARTITION BY scope_id ORDER BY day DESC, computed_at DESC, cityHash64(...)
// tiebreak discipline (this file's own package doc comment: up to 86 rows
// can share an IDENTICAL computed_at for one (scope, scope_id) key on live
// data), just partitioned one level finer -- by (scope_id, day) instead of
// scope_id alone -- so every day survives instead of collapsing to the
// single latest one.
func (p *HealthProvider) queryTeamHealthDailySeries(ctx context.Context, orgID string, ids []string, timeBound factTimeBound) (byTeam map[string][]healthDailyRow, err error) {
	statement := withRowLimit(`SELECT scope_id, toString(day), toString(severity), toUInt8(isNotNull(compounding_risk)), toFloat64(ifNull(compounding_risk, 0))
FROM (
	SELECT scope_id, day, severity, compounding_risk,
		row_number() OVER (PARTITION BY scope_id, day ORDER BY computed_at DESC, cityHash64(tuple(severity, ifNull(compounding_risk, -1))) DESC) AS rn
	FROM compounding_risk_daily
	WHERE org_id = {org_id:String} AND scope = 'team' AND scope_id IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
)
WHERE rn = 1
ORDER BY scope_id, day DESC`)
	byTeam = make(map[string][]healthDailyRow)
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var r healthDailyRow
		var scopeID string
		var hasRisk uint8
		if err := row.Scan(&scopeID, &r.day, &r.severity, &hasRisk, &r.risk); err != nil {
			return err
		}
		r.hasRisk = hasRisk != 0
		byTeam[scopeID] = append(byTeam[scopeID], r)
		return nil
	}, timeBound.bindings()...)
	return byTeam, scanErr
}

func (p *HealthProvider) readScope(ctx context.Context, orgID, scope string, ids []string, bySubject map[string]contextfabric.SubjectRef, evidenceEntityType contractsv1.ContextFabricEvidenceEntityType, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount int, dailyOmitted int, err error) {
	// CHAOS-4645, design doc §5.2: "health ... gain a time_series-declared
	// table for team AND project subjects, alongside the scalars they emit
	// today (additive -- the scalar stays, so RankCohort's inputs are
	// untouched and the ranking numbers cannot move)". Repository is out of
	// this ticket's scope, so dailyByTeam stays nil there and the loop below
	// never attaches a daily_health field for it. Fetched off a genuinely
	// SEPARATE query (queryTeamHealthDailySeries), before the scalar
	// row_number()-latest-row SELECT below runs, so it can never perturb the
	// single physical row that query's severity/compounding_risk/risk_rules
	// fields already come from -- chaos4645_health_daily_test.go pins that
	// RankCohort's healthRiskSignal (which reads ONLY fields["severity"])
	// cannot observe a difference.
	var dailyByTeam map[string][]healthDailyRow
	dailySeriesRowCount := 0
	if scope == "team" {
		var seriesErr error
		dailyByTeam, seriesErr = p.queryTeamHealthDailySeries(ctx, orgID, ids, timeBound)
		if seriesErr != nil {
			return 0, 0, seriesErr
		}
		// codex CHAOS-4645 round-1 P2 (EXECUTED): queryTeamHealthDailySeries
		// carries its OWN withRowLimit(200) cap, shared across every
		// requested team in one query -- distinct from the scalar read's own
		// rowCount below. Folded into rowCount before return so the shared
		// `rowCount >= maxFactRowsPerQuery` check in ReadFacts also catches
		// this query hitting its own cap, not only the scalar one.
		for _, rows := range dailyByTeam {
			dailySeriesRowCount += len(rows)
		}
	}
	// The hash tiebreak's ifNull(compounding_risk, -1) sentinel is only
	// unambiguous while -1 is outside compounding_risk's real domain.
	// compounding_risk is a normalized risk SCORE; live data ranges
	// [0.0000127, 0.58], never negative. There is no ClickHouse-level
	// UInt/CHECK constraint enforcing this -- it is a domain assumption,
	// not a type guarantee.
	//
	// CHAOS-4418: widened to also select the formula's own 4 weighted
	// components (churn/complexity/ownership/review) off the SAME
	// row_number()-picked physical row compounding_risk/severity already
	// come from -- never a second, independent query that could stitch a
	// fact together from a different rerun of the same day (the exact
	// stitching risk this file's own package doc comment already warns
	// about for per-field argMax).
	//
	// Codex R1 (confirmed): the tiebreak hash MUST widen to cover the new
	// columns too, not stay as-is. Two reruns can share the identical
	// day/computed_at/severity/compounding_risk (an exact tie on the OLD
	// hash's own 2-column tuple) while genuinely differing on
	// churn_norm/complexity_norm/ownership_norm/review_norm/w_* -- the
	// package's own doc comment already establishes that reruns carry
	// "genuinely different values, not no-op repeats". Leaving the old
	// 2-column hash would let row_number() pick EITHER tied row
	// arbitrarily on different executions of the identical query, so
	// risk_rules could flap between two different tied rows even though
	// severity/compounding_risk themselves never change -- the exact
	// "same tied inputs must always hash to the same value" property this
	// tiebreak exists to guarantee, now violated for every column beyond
	// the original two.
	statement := withRowLimit(`SELECT scope_id, toString(severity), toUInt8(isNotNull(compounding_risk)), toFloat64(ifNull(compounding_risk, 0)), toString(computed_at), toString(day),
	toUInt8(` + freshnessIsKnownSQL("severity") + `),
	toUInt8(` + freshnessIsKnownSQL("severity") + ` AND ` + freshnessIsFreshSQL("day", timeBound) + `),
	toUInt8(isNotNull(churn_norm)), toFloat64(ifNull(churn_norm, 0)), toFloat64(w_churn),
	toUInt8(isNotNull(complexity_norm)), toFloat64(ifNull(complexity_norm, 0)), toFloat64(w_complexity),
	toUInt8(isNotNull(ownership_norm)), toFloat64(ifNull(ownership_norm, 0)), toFloat64(w_ownership),
	toUInt8(isNotNull(review_norm)), toFloat64(ifNull(review_norm, 0)), toFloat64(w_review)
FROM (
	SELECT scope_id, severity, compounding_risk, computed_at, day, churn_norm, complexity_norm, ownership_norm, review_norm, w_churn, w_complexity, w_ownership, w_review,
		row_number() OVER (PARTITION BY scope_id ORDER BY (severity != 'unknown') DESC, day DESC, computed_at DESC, cityHash64(tuple(severity, ifNull(compounding_risk, -1), ifNull(churn_norm, -1), ifNull(complexity_norm, -1), ifNull(ownership_norm, -1), ifNull(review_norm, -1), w_churn, w_complexity, w_ownership, w_review)) DESC) AS rn
	FROM compounding_risk_daily
	WHERE org_id = {org_id:String} AND scope = '` + scope + `' AND scope_id IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
)
WHERE rn = 1`)
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var scopeID, severity, computedAt, day string
		var hasRisk, isKnownFlag, isFreshFlag uint8
		var risk float64
		values := make([]riskRuleValue, len(riskRuleComponents))
		scanArgs := []any{&scopeID, &severity, &hasRisk, &risk, &computedAt, &day, &isKnownFlag, &isFreshFlag}
		hasNormFlags := make([]uint8, len(riskRuleComponents))
		for i := range riskRuleComponents {
			scanArgs = append(scanArgs, &hasNormFlags[i], &values[i].norm, &values[i].weight)
		}
		if err := row.Scan(scanArgs...); err != nil {
			return err
		}
		for i := range values {
			values[i].hasNorm = hasNormFlags[i] != 0
		}
		subject, ok := bySubject[scopeID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{
			"computed_at":                    contextfabric.StringFactValue(computedAt),
			"severity_freshness_window_days": contextfabric.IntegerFactValue(healthSeverityFreshnessWindowDays),
		}
		if hasRisk != 0 {
			fields["compounding_risk"] = contextfabric.NumberFactValue(risk)
		}
		// CHAOS-5952: this scope's OWN latest-known-within-window row backs
		// severity -- never the literal latest row regardless of band, and
		// never a DIFFERENT physical row than compounding_risk/risk_rules
		// above (same rn=1 pick, this file's own package doc comment on the
		// tiebreak hash already governs why only one row may back every
		// field of one fact). A known band outside the window is served
		// exactly like one that was never recorded: unknown, with a reason
		// that says which.
		if isFreshFlag != 0 {
			fields["severity"] = contextfabric.StringFactValue(severity)
			fields["severity_as_of"] = contextfabric.StringFactValue(day)
		} else {
			fields["severity"] = contextfabric.StringFactValue("unknown")
			if isKnownFlag != 0 {
				fields["severity_unavailable_reason"] = contextfabric.StringFactValue(healthSeverityUnavailableReasonStaleBeyondWindow)
			} else {
				fields["severity_unavailable_reason"] = contextfabric.StringFactValue(healthSeverityUnavailableReasonNoKnownBand)
			}
		}
		ruleRows := make([]contextfabric.FactValueRow, 0, len(riskRuleComponents))
		for i, component := range riskRuleComponents {
			v := values[i]
			rowFields := map[string]contextfabric.FactValue{
				"signal": contextfabric.StringFactValue(component.signal),
				"weight": contextfabric.NumberFactValue(v.weight),
			}
			// An unrecorded normalized signal is unknown, never zero
			// (AGENTS.md North Star check 12) -- the row still names the
			// signal and its configured weight, but norm_value/
			// weighted_contribution stay explicitly null rather than a
			// fabricated 0 that would understate the component's real,
			// unrecorded contribution.
			if v.hasNorm {
				rowFields["norm_value"] = contextfabric.NumberFactValue(v.norm)
				rowFields["weighted_contribution"] = contextfabric.NumberFactValue(v.weight * v.norm)
			} else {
				rowFields["norm_value"] = contextfabric.NullFactValue()
				rowFields["weighted_contribution"] = contextfabric.NullFactValue()
			}
			ruleRows = append(ruleRows, contextfabric.FactValueRow{Fields: rowFields})
		}
		// CHAOS-4633 P1: Key = [signal] -- riskRuleComponents names each
		// weighted rule component exactly once per scope, so signal alone
		// identifies a row.
		fields["risk_rules"] = contextfabric.TableFactValue(contextfabric.FactTable{
			Shape:    contextfabric.FactTableBreakdown,
			Key:      []string{"signal"},
			Measures: []string{"weight", "norm_value", "weighted_contribution"},
			Grain:    timeBound.effectiveGrain(grainDaily),
			Rows:     ruleRows,
		})
		// CHAOS-4645, design doc §5.2: additive alongside the scalar
		// severity/compounding_risk and risk_rules breakdown above -- fetched
		// off the SEPARATE dailyByTeam query, never re-derived from this
		// row's own rn=1 scalars, so this field can only ever be ABSENT
		// (when scope != "team", or when the series query found no rows),
		// never wrong.
		if dailyByTeam != nil {
			if dailyTable, ok, omitted := healthDailyTable(dailyByTeam[scopeID], timeBound.effectiveGrain(grainDaily)); ok {
				fields["daily_health"] = dailyTable
				dailyOmitted += omitted
				if omitted > 0 {
					fields["daily_health_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
				}
			}
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactHealth, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(evidenceEntityType, scopeID)},
		})
		return nil
	}, timeBound.bindings()...)
	if dailySeriesRowCount > rowCount {
		rowCount = dailySeriesRowCount
	}
	return rowCount, dailyOmitted, scanErr
}

// compoundingRiskLatestSubquery returns the row_number()-deduplicated
// per-scope_id pick for one compounding_risk_daily scope ('repo' or
// 'team'), mirroring readScope's own statement exactly (scope is an
// internal Go string literal, never caller-supplied, so it is safe to
// inline the same way readScope's own `scope` parameter already is). The
// pick prefers a KNOWN severity over the day it fell on (CHAOS-5952: a
// day with no known band contributes no evidence either way, so it can
// never outrank a real reading), and among rows sharing that same
// known-ness, the latest day wins. Every consumer of this subquery --
// readProjectHealth's risk_breakdown rows and
// queryProjectHealthSeverityMax's aggregate alike -- applies
// freshnessIsFreshSQL to the SAME `day` column this pick exposes, so the
// breakdown and the aggregate can never disagree about which rows count
// as current.
func compoundingRiskLatestSubquery(scope string, timeBound factTimeBound) string {
	return `SELECT scope_id, severity, compounding_risk, computed_at, day,
		row_number() OVER (PARTITION BY scope_id ORDER BY (severity != 'unknown') DESC, day DESC, computed_at DESC, cityHash64(tuple(severity, ifNull(compounding_risk, -1))) DESC) AS rn
	FROM compounding_risk_daily
	WHERE org_id = {org_id:String} AND scope = '` + scope + `'` + timeBound.dayPredicate("day")
}

// compoundingRiskDailySubquery is compoundingRiskLatestSubquery's CHAOS-4645
// counterpart: EVERY day's row_number()-deduplicated row per (scope_id, day)
// for one compounding_risk_daily scope, instead of collapsing to the single
// latest row per scope_id. Used by queryProjectHealthDailySeries's two-layer
// UNION ALL exactly the way compoundingRiskLatestSubquery is used by
// readProjectHealth's own UNION ALL above.
func compoundingRiskDailySubquery(scope string, timeBound factTimeBound) string {
	return `SELECT scope_id, day, severity, compounding_risk,
		row_number() OVER (PARTITION BY scope_id, day ORDER BY computed_at DESC, cityHash64(tuple(severity, ifNull(compounding_risk, -1))) DESC) AS rn
	FROM compounding_risk_daily
	WHERE org_id = {org_id:String} AND scope = '` + scope + `'` + timeBound.dayPredicate("day")
}

// healthRollupRow is one (project, scope, scope_id) triple's contribution to
// a project's health rollup, scanned off the ownership join before Go-side
// grouping. scope is 'team' or 'repo' -- see readProjectHealth's doc
// comment for the two-layer chain.
type healthRollupRow struct {
	scope, scopeID, scopeName, severity, computedAt, day string
	// isKnown/isFresh are CHAOS-5952's freshness classification of THIS
	// row's own severity (see freshnessIsKnownSQL/freshnessIsFreshSQL):
	// isKnown is true for any real band, isFresh additionally requires day
	// to sit within healthSeverityFreshnessWindowDays of the request's
	// as-of instant. A row can be known but not fresh (a stale real band);
	// it can never be fresh but not known.
	hasRisk, isKnown, isFresh bool
	risk                      float64
}

// healthSeverityBasisTeamAndRepoBreakdown and
// healthSeverityUnavailableReasonNoKnownBand are the project health
// severity promotion's two disclosed, closed-vocabulary values -- each a
// single fixed literal, the same "one canonical string, declared once"
// idiom this package's own rollup_basis fields and timebound.go's reason
// constants already use. A served project FactHealth carries exactly one
// of: {severity + severity_basis == healthSeverityBasisTeamAndRepoBreakdown}
// or {severity_unavailable_reason == healthSeverityUnavailableReasonNoKnownBand},
// never both, never neither -- independent of whether risk_breakdown itself
// is present (a project's KNOWN band can come from a scope outside the
// rendered breakdown; see queryProjectHealthSeverityMax's own doc comment).
const (
	healthSeverityBasisTeamAndRepoBreakdown    = "worst_of_team_and_repo_breakdown"
	healthSeverityUnavailableReasonNoKnownBand = "no_known_severity_breakdown_rows"
	// healthSeverityUnavailableReasonStaleBeyondWindow (CHAOS-5952) is the
	// one closed value the severity_unavailable_reason alphabet gains
	// beside healthSeverityUnavailableReasonNoKnownBand: a scope (or, at
	// project scope, every one of a project's reachable scopes) DID record
	// a known band at some point, but the most recent one falls outside
	// healthSeverityFreshnessWindowDays of the request's as-of instant.
	// Distinct from healthSeverityUnavailableReasonNoKnownBand (which means
	// no known band was ever recorded at all) so a reader can tell "this
	// scope has simply never had a real reading" apart from "this scope's
	// last real reading is too old to trust".
	healthSeverityUnavailableReasonStaleBeyondWindow = "known_severity_stale_beyond_freshness_window"
)

// healthSeverityWinnerDelimiter separates the (scope, scope_id, severity)
// triple queryProjectHealthSeverityMax's own argMax packs into one string
// column -- a delimiter rather than three independent argMax calls, because
// three independent aggregates keyed by the same ORDER expression have no
// guarantee of resolving a tie to the SAME underlying row (this file's own
// package doc comment already documents this exact failure mode for
// severity vs compounding_risk). \x1f (unit separator) never appears in a
// provider name, a UUID, or the closed severity vocabulary.
const healthSeverityWinnerDelimiter = "\x1f"

// healthSeverityMaxRow is one project's result from
// queryProjectHealthSeverityMax: how many of its reachable team+repo rows
// carry a KNOWN AND FRESH band (knownRowCount, CHAOS-5952), how many carry
// a known band at all regardless of freshness (everKnownRowCount -- lets
// the caller tell "never known" apart from "known but stale" when
// knownRowCount is zero), how many rows are reachable in total, and --
// when at least one is known and fresh -- which single scope and day
// produced the winning (worst) band, so the caller can cite it as
// evidence even when that scope's own row never reached the row-level
// breakdown scan's shared budget.
type healthSeverityMaxRow struct {
	knownRowCount, everKnownRowCount, totalRowCount       int64
	winnerScope, winnerScopeID, winnerSeverity, winnerDay string
}

// readProjectHealth rolls FactHealth up for a project two ways at once (see
// the package doc comment): a team layer via team_project_ownership and a
// repo layer one hop further via team_repo_ownership, both landing in one
// renderable risk_breakdown table per project, tagged by scope. Neither
// layer is summed or averaged into a single project-level risk score.
func (p *HealthProvider) readProjectHealth(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount int, rejected int, breakdownTruncated bool, err error) {
	ids, bySubject, rejected := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, rejected, false, nil
	}
	ownershipPredicate := ownershipValidityPredicate(timeBound)
	// Round-1 P2: the team/repo UNION ALL is wrapped in an outer SELECT
	// before withRowLimit's LIMIT is applied. Appending LIMIT directly after
	// two UNION ALL'd SELECTs binds it to the SECOND (repo) branch only --
	// the team branch would be unbounded, exceeding the advertised
	// maxFactRowsPerQuery cap -- and UNION ALL output order is otherwise
	// unspecified, which would make risk_breakdown/evidence ordering vary
	// between identical reads. The outer ORDER BY makes both the bound and
	// the ordering apply to the COMBINED result.
	// CHAOS-4521b, self-found after codex R3: projectOwnershipJoinSQL now
	// collapses to the RESOLVED grain and therefore exposes ONE alias, `p`,
	// carrying team_id -- there is no `tpo` row left to reference. Reading
	// `tpo.team_id` here produced invalid SQL, and neither the build nor
	// the fake-client tests could see it, because a fake client returns
	// canned rows regardless of the statement. Exactly the blind spot this
	// whole ticket has been about.
	statement := withRowLimit(`SELECT project_key, scope, scope_id, scope_name, severity, has_risk, risk, computed_at, day, is_known, is_fresh
FROM (
	SELECT concat(p.provider, ':', p.id) AS project_key, 'team' AS scope, p.team_id AS scope_id, ifNull(t.name, '') AS scope_name, toString(cr.severity) AS severity, toUInt8(isNotNull(cr.compounding_risk)) AS has_risk, toFloat64(ifNull(cr.compounding_risk, 0)) AS risk, toString(cr.computed_at) AS computed_at, toString(cr.day) AS day, toUInt8(` + freshnessIsKnownSQL("cr.severity") + `) AS is_known, toUInt8(` + freshnessIsKnownSQL("cr.severity") + ` AND ` + freshnessIsFreshSQL("cr.day", timeBound) + `) AS is_fresh
	FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
	INNER JOIN (` + compoundingRiskLatestSubquery("team", timeBound) + `) AS cr ON cr.scope_id = p.team_id AND cr.rn = 1
	LEFT JOIN (SELECT id, name FROM teams FINAL WHERE org_id = {org_id:String}) AS t ON t.id = p.team_id

	UNION ALL

	SELECT concat(p.provider, ':', p.id) AS project_key, 'repo' AS scope, tro.repo_key AS scope_id, tro.repo_full_name AS scope_name, toString(cr.severity) AS severity, toUInt8(isNotNull(cr.compounding_risk)) AS has_risk, toFloat64(ifNull(cr.compounding_risk, 0)) AS risk, toString(cr.computed_at) AS computed_at, toString(cr.day) AS day, toUInt8(` + freshnessIsKnownSQL("cr.severity") + `) AS is_known, toUInt8(` + freshnessIsKnownSQL("cr.severity") + ` AND ` + freshnessIsFreshSQL("cr.day", timeBound) + `) AS is_fresh
	FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
	INNER JOIN (
		SELECT team_id, toString(repo_id) AS repo_key, repo_full_name
		FROM team_repo_ownership FINAL
		WHERE org_id = {org_id:String} AND repo_id IS NOT NULL` + ownershipPredicate + `
		GROUP BY team_id, repo_key, repo_full_name
	) AS tro ON tro.team_id = p.team_id
	INNER JOIN (` + compoundingRiskLatestSubquery("repo", timeBound) + `) AS cr ON cr.scope_id = tro.repo_key AND cr.rn = 1
)
ORDER BY project_key, scope, scope_id`)
	// byProject carries WHATEVER the shared, row-capped scan reached for
	// each project -- never the project population itself (that is
	// severityOrder, from the uncapped aggregate below); no iteration
	// order is kept here because nothing iterates this map by itself.
	byProject := make(map[string][]healthRollupRow)
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var projectSubjectKey, scope, scopeID, scopeName, severity, computedAt, day string
		var hasRisk, isKnownFlag, isFreshFlag uint8
		var risk float64
		if err := row.Scan(&projectSubjectKey, &scope, &scopeID, &scopeName, &severity, &hasRisk, &risk, &computedAt, &day, &isKnownFlag, &isFreshFlag); err != nil {
			return err
		}
		if _, ok := bySubject[projectSubjectKey]; !ok {
			return nil
		}
		byProject[projectSubjectKey] = append(byProject[projectSubjectKey], healthRollupRow{
			scope: scope, scopeID: scopeID, scopeName: scopeName, severity: severity, computedAt: computedAt, day: day,
			hasRisk: hasRisk != 0, isKnown: isKnownFlag != 0, isFresh: isFreshFlag != 0, risk: risk,
		})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return rowCount, rejected, false, scanErr
	}
	// CHAOS-4645, design doc §5.2: additive, off the SAME two-layer
	// ownership join, never changing an existing field -- health carries a
	// RankCohort signal (healthRiskSignal reads fields["severity"] only, see
	// readScope's own note and chaos4645_health_daily_test.go's pin), and
	// this is a genuinely SEPARATE field (daily_health) on the SAME fact, so
	// it cannot move what healthRiskSignal reads.
	//
	// This project rollup's top-level Fields carries "compounding_risk"
	// (the freshest daily_health day's declared Measure, copied in under
	// its own field name below) alongside a SEPARATE top-level "severity"
	// (the worst band across this project's own risk_breakdown rows,
	// read from queryProjectHealthSeverityMax below, independent of which
	// day daily_health's freshest row belongs to). healthRiskSignal
	// (cohort_ranking.go) reads fields["severity"] off ANY FactHealth fact
	// regardless of subject kind, and project cohorts ARE constructed in
	// production (graphrank's DiscoveredCohort, for a frame whose subject
	// expression declares member_kind "project"), so a project cohort's
	// health-risk signal scores from the same worst-case-governs
	// doctrine every other signal in that file already documents
	// (workloadWorstDays, readinessGapSignal, deficiencySeveritySignal).
	dailyByProject, seriesErr := p.queryProjectHealthDailySeries(ctx, orgID, ids, timeBound)
	if seriesErr != nil {
		return rowCount, rejected, false, seriesErr
	}
	// codex CHAOS-4645 round-1 P2 (EXECUTED): see readScope's identical note
	// -- the daily-series query's own withRowLimit(200) cap, shared across
	// every requested project in one query, must also surface as Truncated.
	dailySeriesRowCount := 0
	for _, rows := range dailyByProject {
		dailySeriesRowCount += len(rows)
	}
	if dailySeriesRowCount > rowCount {
		rowCount = dailySeriesRowCount
	}
	// severityByProject/severityOrder are read from queryProjectHealthSeverityMax's
	// OWN server-side aggregate, never derived from the row-level
	// byProject/riskRows scan above: that scan's own withRowLimit cap is
	// shared across EVERY requested project's breakdown rows combined, so
	// a project sitting near the boundary of that shared budget can be
	// ABSENT from byProject/projectOrder entirely, not merely missing its
	// worst row. The severity aggregate below GROUPs BY project
	// server-side, so its answer for one project -- INCLUDING WHETHER THE
	// PROJECT APPEARS AT ALL -- is correct regardless of how many rows any
	// OTHER requested project contributes to the shared budget; only ITS
	// OWN project-count probe (not a row-count one) can truncate it,
	// folded into rowCount/breakdownTruncated the same way
	// dailySeriesRowCount is above.
	severityByProject, severityOrder, severityRowCount, severityErr := p.queryProjectHealthSeverityMax(ctx, orgID, ids, timeBound)
	if severityErr != nil {
		return rowCount, rejected, false, severityErr
	}
	if severityRowCount >= maxFactRowsProbe {
		breakdownTruncated = true
	}
	if severityRowCount > rowCount {
		rowCount = severityRowCount
	}
	// severityOrder drives EMISSION, never projectOrder: projectOrder only
	// lists projects the shared, row-capped scan happened to reach, while
	// severityOrder lists every project the uncapped, per-project aggregate
	// proved reachable through the SAME ownership join. A project present
	// in the aggregate but absent from byProject (rows is then nil) is
	// still a REAL project this org's ownership data reaches -- it is
	// emitted with an honestly empty/partial breakdown, never silently
	// dropped because the shared scan ran out of room before reaching it.
	for _, projectKey := range severityOrder {
		subject, ok := bySubject[projectKey]
		if !ok {
			continue
		}
		sev := severityByProject[projectKey]
		rows := byProject[projectKey]
		seenScopeEntries := make(map[string]bool, len(rows))
		seenTeams := make(map[string]bool, len(rows))
		seenRepos := make(map[string]bool, len(rows))
		riskRows := make([]contextfabric.FactValueRow, 0, len(rows))
		evidenceRefIDs := make([]string, 0, len(rows)+2)
		evidenceRefIDs = append(evidenceRefIDs, evidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, projectKey))
		for _, r := range rows {
			dedupeKey := r.scope + "\x00" + r.scopeID
			if dedupeTeamRow(seenScopeEntries, dedupeKey) {
				continue
			}
			switch r.scope {
			case "team":
				if !dedupeTeamRow(seenTeams, r.scopeID) {
					evidenceRefIDs = append(evidenceRefIDs, evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, r.scopeID))
				}
			case "repo":
				if !dedupeTeamRow(seenRepos, r.scopeID) {
					evidenceRefIDs = append(evidenceRefIDs, evidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, r.scopeID))
				}
			}
			rowFields := map[string]contextfabric.FactValue{
				"scope":       contextfabric.StringFactValue(r.scope),
				"scope_id":    contextfabric.StringFactValue(r.scopeID),
				"scope_name":  stringOrNull(r.scopeName),
				"computed_at": contextfabric.StringFactValue(r.computedAt),
			}
			if r.hasRisk {
				rowFields["compounding_risk"] = contextfabric.NumberFactValue(r.risk)
			}
			// CHAOS-5952: this row's own severity is served the same way
			// readScope serves a repo/team scope's -- a known band inside
			// the freshness window, or unknown with a reason, never the
			// literal latest day's band regardless of staleness.
			if r.isFresh {
				rowFields["severity"] = contextfabric.StringFactValue(r.severity)
				rowFields["severity_as_of"] = contextfabric.StringFactValue(r.day)
			} else {
				rowFields["severity"] = contextfabric.StringFactValue("unknown")
				if r.isKnown {
					rowFields["severity_unavailable_reason"] = contextfabric.StringFactValue(healthSeverityUnavailableReasonStaleBeyondWindow)
				} else {
					rowFields["severity_unavailable_reason"] = contextfabric.StringFactValue(healthSeverityUnavailableReasonNoKnownBand)
				}
			}
			riskRows = append(riskRows, contextfabric.FactValueRow{Fields: rowFields})
		}
		var omitted int
		riskRows, omitted = capFactValueRows(riskRows)
		breakdownTruncated = breakdownTruncated || omitted > 0
		fields := map[string]contextfabric.FactValue{
			// rollup_basis discloses BOTH chains this fact draws from --
			// see the package doc comment's two-layer explanation.
			"rollup_basis": contextfabric.StringFactValue("team_project_ownership_and_team_repo_ownership"),
			"team_count":   contextfabric.IntegerFactValue(int64(len(seenTeams))),
			"repo_count":   contextfabric.IntegerFactValue(int64(len(seenRepos))),
			// risk_breakdown_rows_shown/_total disclose, PER PROJECT,
			// exactly what the row-level scan's shared budget and the
			// display cap together left visible versus how many rows are
			// truly reachable (rows_total comes from the severity
			// aggregate's own uncapped count, so it can never be narrowed
			// by either cap) -- a project can carry a known severity with
			// rows_shown=0 when its own rows never reached the shared
			// scan at all.
			"risk_breakdown_rows_shown":      contextfabric.IntegerFactValue(int64(len(riskRows))),
			"risk_breakdown_rows_total":      contextfabric.IntegerFactValue(sev.totalRowCount),
			"severity_freshness_window_days": contextfabric.IntegerFactValue(healthSeverityFreshnessWindowDays),
		}
		// Key = [scope, scope_id, scope_name, severity, computed_at] --
		// dedupeKey above already partitions on (scope,
		// scope_id), so those two alone guarantee distinctness;
		// scope_name/severity/computed_at ride along as declared identity
		// columns, not measures. FactTable.Validate refuses a table with
		// zero rows, so risk_breakdown is present only when the shared
		// scan actually reached at least one of this project's rows --
		// its absence is itself part of the rows_shown=0 disclosure above,
		// never silently substituted with an empty table.
		if len(riskRows) > 0 {
			fields["risk_breakdown"] = contextfabric.TableFactValue(contextfabric.FactTable{
				Shape:    contextfabric.FactTableBreakdown,
				Key:      []string{"scope", "scope_id", "scope_name", "severity", "computed_at"},
				Measures: []string{"compounding_risk"},
				// severity_as_of/severity_unavailable_reason (CHAOS-5952) are
				// per-row disclosures of THIS row's own freshness pick, never
				// a value constant across the whole table -- Observations,
				// not a sibling scalar on the fact (contrast
				// severity_freshness_window_days above, which IS constant
				// across every row and belongs on the fact, per
				// FactTable.Validate's own rule).
				Observations: []string{"severity_as_of", "severity_unavailable_reason"},
				Grain:        timeBound.effectiveGrain(grainDaily),
				Rows:         riskRows,
			})
		}
		// severity promotes the WORST band across this project's own
		// reachable team+repo rows, excluding "unknown" -- a data gap
		// contributes no evidence either way, so it can never win the max
		// against a real reading, and it can never stand in as a false
		// "low" when it is the only reading a project's population
		// carries. Read from queryProjectHealthSeverityMax's own
		// server-side aggregate, never from the riskRows loop above, so
		// the value -- and its evidence -- are immune to the row-level
		// scan's shared budget. A project with at least one known band
		// discloses which combined population produced it (severity_basis)
		// and cites the WINNING scope directly from the aggregate, even
		// when that scope's own row never reached the shared scan above
		// (a promoted severity with no evidence for the scope that
		// produced it is an unverifiable claim); a project whose reachable
		// rows are ALL unknown (0 reachable rows at all means this project
		// never appears in severityOrder, so this branch is never reached
		// for it) discloses that severity is undetermined for a named
		// reason, never a defaulted "low".
		if sev.knownRowCount > 0 {
			fields["severity"] = contextfabric.StringFactValue(sev.winnerSeverity)
			fields["severity_basis"] = contextfabric.StringFactValue(healthSeverityBasisTeamAndRepoBreakdown)
			fields["severity_as_of"] = contextfabric.StringFactValue(sev.winnerDay)
			winnerKey := sev.winnerScope + "\x00" + sev.winnerScopeID
			if !seenScopeEntries[winnerKey] {
				switch sev.winnerScope {
				case "team":
					evidenceRefIDs = append(evidenceRefIDs, evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, sev.winnerScopeID))
				case "repo":
					evidenceRefIDs = append(evidenceRefIDs, evidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, sev.winnerScopeID))
				}
			}
		} else if sev.everKnownRowCount > 0 {
			// CHAOS-5952: this project's reachable rows DID carry a known
			// band at some point, but none of them falls inside
			// healthSeverityFreshnessWindowDays of the request's as-of
			// instant -- distinct from never having carried one at all.
			fields["severity_unavailable_reason"] = contextfabric.StringFactValue(healthSeverityUnavailableReasonStaleBeyondWindow)
		} else {
			fields["severity_unavailable_reason"] = contextfabric.StringFactValue(healthSeverityUnavailableReasonNoKnownBand)
		}
		if dailyTable, ok, dailyOmitted := healthDailyTable(dailyByProject[projectKey], timeBound.effectiveGrain(grainDaily)); ok {
			fields["daily_health"] = dailyTable
			if dailyOmitted > 0 {
				breakdownTruncated = true
				fields["daily_health_omitted_count"] = contextfabric.IntegerFactValue(int64(dailyOmitted))
			}
			// CHAOS-4681: same gap as readProjectWorkload's identical note --
			// a project's top-level fields carried no scalar matching
			// daily_health's sole declared Measure (compounding_risk), so a
			// trend could never be claimed here. dailyByProject's own
			// ORDER BY ... DESC makes index 0 the freshest day.
			//
			// UNLIKE metrics.go's readRepositoryMetrics idiom (which copies
			// the whole freshest-day row), this copies ONLY the declared
			// Measure, not the whole row: the row's other field, "severity"
			// (a per-day categorical observation, not a Measure), names one specific
			// day's single winning scope, a narrower population than the
			// worst-of-team-and-repo-breakdown population fields["severity"]
			// above already discloses. Overwriting the promoted worst-band
			// value with one day's single scope here would silently swap
			// its disclosed basis for a different, undisclosed one -- the
			// promoted severity keeps coming from the breakdown-wide max
			// computed from the breakdown-wide max above, never from this table.
			if risk, ok := dailyByProject[projectKey][0].toFactValueRow().Fields["compounding_risk"]; ok {
				fields["compounding_risk"] = risk
			}
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactHealth, Subject: subject, Fields: fields,
			EvidenceRefIDs: evidenceRefIDs,
		})
	}
	return rowCount, rejected, breakdownTruncated, nil
}

// queryProjectHealthDailySeries is queryTeamHealthDailySeries' project-rollup
// counterpart (CHAOS-4645, design doc §5.2): the SAME team+repo two-layer
// ownership join readProjectHealth's own doc comment explains, but every day
// survives (compoundingRiskDailySubquery, PARTITIONed by (scope_id, day)
// instead of scope_id alone), then GROUPed BY day across every contributing
// team+repo scope for that project.
//
// compounding_risk is a normalized RISK SCORE, not an additive count --
// summing or averaging it across a project's several teams/repos has no
// defined meaning (readProjectFlow's own note in flow.go makes the analogous
// point for percentiles: "summing a percentile has no meaning"). This
// package's own cohort_ranking.go already establishes the precedent for
// exactly this situation: workloadWorstDays and readinessGapSignal both
// aggregate the WORST value across a subject's several scope-partitioned
// facts ("worst case governs", their own doc comments) rather than summing
// or averaging. A risk score's worst case is its MAX, so MAX(compounding_risk)
// per day is that same convention, expressed as a SQL aggregate instead of a
// Go loop over CanonicalFacts because the aggregation happens WITHIN one
// fact's own daily series (across a project's contributing scopes on ONE
// day), not across several facts. severity rides along via argMax, keyed to
// the SAME (risk, tiebreak-hash) ordering that decided the max, so a day's
// reported severity is always the severity OF the scope that produced that
// day's reported risk -- never an unrelated scope's severity paired with a
// different scope's risk.
//
// A team can legitimately own a project through more than one ownership
// `source` row (readProjectHealth's own dedupeTeamRow guard), which would
// join that team's SAME (day, risk) row into this UNION more than once --
// unlike readProjectFlow's SUM, that is harmless here: max(x, x) = x, so a
// duplicated join can never inflate a MAX aggregate the way it would a SUM.
func (p *HealthProvider) queryProjectHealthDailySeries(ctx context.Context, orgID string, ids []string, timeBound factTimeBound) (byProject map[string][]healthDailyRow, err error) {
	ownershipPredicate := ownershipValidityPredicate(timeBound)
	statement := withRowLimit(`SELECT project_key, toString(day), toUInt8(isNotNull(max(risk))), toFloat64(ifNull(max(risk), 0)), toString(argMax(severity, tuple(ifNull(risk, -1), cityHash64(tuple(severity, ifNull(risk, -1))))))
FROM (
	SELECT concat(p.provider, ':', p.id) AS project_key, cr.day AS day, cr.severity AS severity, cr.compounding_risk AS risk
	FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
	INNER JOIN (` + compoundingRiskDailySubquery("team", timeBound) + `) AS cr ON cr.scope_id = p.team_id AND cr.rn = 1

	UNION ALL

	SELECT concat(p.provider, ':', p.id) AS project_key, cr.day AS day, cr.severity AS severity, cr.compounding_risk AS risk
	FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
	INNER JOIN (
		SELECT team_id, toString(repo_id) AS repo_key
		FROM team_repo_ownership FINAL
		WHERE org_id = {org_id:String} AND repo_id IS NOT NULL` + ownershipPredicate + `
		GROUP BY team_id, repo_key
	) AS tro ON tro.team_id = p.team_id
	INNER JOIN (` + compoundingRiskDailySubquery("repo", timeBound) + `) AS cr ON cr.scope_id = tro.repo_key AND cr.rn = 1
)
GROUP BY project_key, day
ORDER BY project_key, day DESC`)
	byProject = make(map[string][]healthDailyRow)
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var r healthDailyRow
		var projectKey string
		var hasRisk uint8
		if err := row.Scan(&projectKey, &r.day, &hasRisk, &r.risk, &r.severity); err != nil {
			return err
		}
		r.hasRisk = hasRisk != 0
		byProject[projectKey] = append(byProject[projectKey], r)
		return nil
	}, timeBound.bindings()...)
	return byProject, scanErr
}

// queryProjectHealthSeverityMax computes, per project, the worst
// (low < elevated < high) severity band across the SAME latest-row-per-
// scope population readProjectHealth's own risk_breakdown draws from
// (compoundingRiskLatestSubquery over both the team layer and the
// team_repo_ownership repo layer) -- but as ONE server-side aggregate,
// GROUPed BY project, rather than a Go-side fold over the row-level scan.
//
// This is the PROJECT POPULATION SOURCE, not merely a severity lookup:
// readProjectHealth's row-level breakdown scan shares ONE withRowLimit
// budget across EVERY project a caller requests in one call, ordered by
// (project_key, scope, scope_id) -- a project sorting late enough to sit
// past that shared cap can be ABSENT from the row-level scan entirely, not
// merely missing its worst row. This query has no such shared budget (its
// own probe counts DISTINCT PROJECTS, never contributing rows), so every
// project it returns is emitted by the caller regardless of whether the
// row-level scan reached it at all -- see readProjectHealth's own doc
// comment at the call site.
//
// It is also the EVIDENCE SOURCE for the promoted value: knownRowCount,
// totalRowCount and the winning (scope, scope_id) travel WITH the severity,
// so a project whose winning row never reached the row-level scan still
// gets a citable evidence ref for the scope that actually produced its
// severity (readProjectHealth appends it directly from this result, never
// only from rows the display scan happened to carry).
//
// The severity->band mapping is the SQL expression of the same
// low < elevated < high < (unknown excluded) ordinal cohort_ranking.go's
// healthRiskSignal already reads off the served field: "unknown" (or any
// value outside {low, elevated, high}) maps to band 0 and can never win
// max(band) against a real reading, so it can never surface as a false
// "low". knownRowCount = countIf(band > 0), everKnownRowCount =
// countIf(raw_band > 0), and totalRowCount = count() let the caller
// distinguish three populations: a known band inside the freshness window
// (serves severity), a known band that exists but is entirely stale
// (serves severity_unavailable_reason's
// healthSeverityUnavailableReasonStaleBeyondWindow), and reachable rows
// that were never known at all (healthSeverityUnavailableReasonNoKnownBand)
// -- versus no reachable rows at all (this project never appears in the
// result, and readProjectHealth serves no fact for it). The winning
// (scope, scope_id, severity, day) quadruple is packed into ONE argMax'd
// string (healthSeverityWinnerDelimiter-joined) rather than four
// independent argMax calls, because four aggregates keyed by the same
// ORDER expression have no guarantee of resolving a tie to the SAME
// underlying row (this file's own package doc comment already documents
// this exact failure mode for severity vs compounding_risk). band is a
// function of severity alone (freshness only gates whether it counts at
// all), so two DIFFERENT rows -- a team's fresh "high" and a repo's fresh
// "high" on a different day -- routinely tie on band while their packed
// strings differ; the ORDER key is therefore the tuple (band, day,
// cityHash64(...)) so the tie resolves to the LATEST contributing day
// first, and only falls to the hash for a genuine same-day tie, matching
// this file's and workload.go's existing tuple-argMax convention for
// deterministic tie-break.
func (p *HealthProvider) queryProjectHealthSeverityMax(ctx context.Context, orgID string, ids []string, timeBound factTimeBound) (byProject map[string]healthSeverityMaxRow, order []string, rowCount int, err error) {
	ownershipPredicate := ownershipValidityPredicate(timeBound)
	// CHAOS-5952: band is computed off the SAME freshnessIsKnownSQL/
	// freshnessIsFreshSQL fragments readScope and readProjectHealth's own
	// risk_breakdown scan apply to this exact per-scope pick (see
	// compoundingRiskLatestSubquery's own doc comment) -- never a second,
	// independently-derived freshness rule that could disagree with the
	// breakdown about which rows are current. rawBand ignores freshness
	// entirely, so countIf(raw_band > 0) answers "did this project ever
	// reach a known band at all", distinct from countIf(band > 0)'s "does
	// it have one WITHIN the window today" -- the two together are what
	// let the caller tell a project that never had a known band apart from
	// one whose only known bands are all now stale.
	statement := withRowProbeLimit(`SELECT project_key,
	countIf(band > 0),
	countIf(raw_band > 0),
	count(),
	argMax(concat(scope, '` + healthSeverityWinnerDelimiter + `', scope_id, '` + healthSeverityWinnerDelimiter + `', severity, '` + healthSeverityWinnerDelimiter + `', day),
		tuple(band, day, cityHash64(tuple(scope, scope_id, severity, day))))
FROM (
	SELECT project_key, scope, scope_id, severity, day,
		multiIf(severity = 'high' AND ` + freshnessIsFreshSQL("day", timeBound) + `, 3, severity = 'elevated' AND ` + freshnessIsFreshSQL("day", timeBound) + `, 2, severity = 'low' AND ` + freshnessIsFreshSQL("day", timeBound) + `, 1, 0) AS band,
		multiIf(severity = 'high', 3, severity = 'elevated', 2, severity = 'low', 1, 0) AS raw_band
	FROM (
		SELECT concat(p.provider, ':', p.id) AS project_key, 'team' AS scope, p.team_id AS scope_id, cr.severity AS severity, cr.day AS day
		FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
		INNER JOIN (` + compoundingRiskLatestSubquery("team", timeBound) + `) AS cr ON cr.scope_id = p.team_id AND cr.rn = 1

		UNION ALL

		SELECT concat(p.provider, ':', p.id) AS project_key, 'repo' AS scope, tro.repo_key AS scope_id, cr.severity AS severity, cr.day AS day
		FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
		INNER JOIN (
			SELECT team_id, toString(repo_id) AS repo_key
			FROM team_repo_ownership FINAL
			WHERE org_id = {org_id:String} AND repo_id IS NOT NULL` + ownershipPredicate + `
			GROUP BY team_id, repo_key
		) AS tro ON tro.team_id = p.team_id
		INNER JOIN (` + compoundingRiskLatestSubquery("repo", timeBound) + `) AS cr ON cr.scope_id = tro.repo_key AND cr.rn = 1
	)
)
GROUP BY project_key
ORDER BY project_key`)
	byProject = make(map[string]healthSeverityMaxRow)
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var projectKey, winner string
		var known, everKnown, total uint64
		if err := row.Scan(&projectKey, &known, &everKnown, &total, &winner); err != nil {
			return err
		}
		result := healthSeverityMaxRow{knownRowCount: int64(known), everKnownRowCount: int64(everKnown), totalRowCount: int64(total)}
		if known > 0 {
			if parts := strings.SplitN(winner, healthSeverityWinnerDelimiter, 4); len(parts) == 4 {
				result.winnerScope, result.winnerScopeID, result.winnerSeverity, result.winnerDay = parts[0], parts[1], parts[2], parts[3]
			}
		}
		if _, seen := byProject[projectKey]; !seen {
			order = append(order, projectKey)
		}
		byProject[projectKey] = result
		return nil
	}, timeBound.bindings()...)
	return byProject, order, rowCount, scanErr
}
