package devhealthfacts

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// OperationalDeficienciesProvider implements contextfabric.FactProvider for
// FactOperationalDeficiencies from recommendations_daily -- Dev Health Ops'
// precomputed, rule-fired recommendation feed (rule_id, severity, title,
// rationale, a stated success_criterion, and the window the rule evaluated).
// This provider only ever surfaces rows Ops itself already marked fired=1;
// it never evaluates a rule, never derives severity, and never decides what
// "fired" means -- that judgment belongs entirely to Ops' rule engine
// (§19.6.3). A team can have several distinct fired rules at once, so this
// provider returns zero or more CanonicalFacts per requested team, one per
// (rule_id, most recent window).
//
// fired must be evaluated AFTER picking the truly latest row per
// (team_id, rule_id), never before: live data shows CHAOS/saturation's most
// recent window (2026-08-12) is fired=false, while an OLDER window
// (2026-08-08) is fired=true (Codex finding F1, confirmed against real
// ClickHouse data). Filtering fired=1 in the same WHERE that feeds
// row_number() windows only the fired rows against each other, so the
// "latest" row becomes the latest FIRED row -- silently resurrecting a
// deficiency Ops already cleared. The fix: window over every row for the
// key (row_number() ORDER BY window_end DESC, computed_at DESC, no fired
// predicate), then keep rn=1 rows only if THAT row is fired=1.
// recommendations_daily is ReplacingMergeTree(computed_at) sorted on
// (org_id, team_id, rule_id, window_end); FINAL collapses a same-window
// recompute, and computed_at DESC still breaks the tie if FINAL's merge
// has not landed yet.
type OperationalDeficienciesProvider struct{ facts clickhouseFacts }

func newOperationalDeficienciesProvider(client contextpacket.ClickHouseQueryClient) *OperationalDeficienciesProvider {
	return &OperationalDeficienciesProvider{facts: clickhouseFacts{client: client}}
}

func (p *OperationalDeficienciesProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactOperationalDeficiencies, "devhealthfacts.operational_deficiencies", []contextfabric.SubjectKind{contextfabric.SubjectTeam})
}

func (p *OperationalDeficienciesProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject, rejected := subjectIndex(query.Subjects, teamPrefix)
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.operational_deficiencies", contextfabric.FactOperationalDeficiencies, rejected)
		}
	}()
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	byTeam, coverageErr := p.readEvaluationCoverage(ctx, orgID, ids, bySubject, timeBound)
	coverage := evaluationCoverage{byTeam: byTeam, requested: bySubject}
	if coverageErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query team deficiency evaluation coverage", coverageErr)
	}
	// row_number() windows over EVERY row for (team_id, rule_id) -- fired
	// is never part of that WHERE -- so rn=1 is always the truly latest
	// evaluation. fired=1 is then applied to that single winning row only,
	// in the outer WHERE, so a rule Ops has since cleared never resurfaces
	// just because it fired at some earlier point (F1).
	//
	// window_end/computed_at is still not a TOTAL order (Codex round-2
	// finding M1): without a further tiebreaker, a tie could let an
	// arbitrary fired value win between executions of the identical
	// query -- exactly the bug F1 fixed, reintroduced by a different
	// route. cityHash64 of the value columns is the final tiebreaker --
	// arbitrary among an exact tie, but stable. The hash must cover
	// EVERY column this query actually outputs (Codex round-3 finding):
	// rule_version and window_start are both selected fields, and were
	// missing from the first version of this tuple -- two rows tied on
	// (window_end, computed_at) but differing only in rule_version or
	// window_start would still have been unordered.
	statement := withRowLimit(`SELECT team_id, rule_id, rule_version, severity, title, rationale, success_criterion, toString(window_start), toString(window_end)
FROM (
	SELECT team_id, rule_id, rule_version, severity, title, rationale, success_criterion, window_start, window_end, fired,
		row_number() OVER (PARTITION BY team_id, rule_id ORDER BY window_end DESC, computed_at DESC, cityHash64(tuple(fired, severity, title, rationale, success_criterion, rule_version, window_start)) DESC) AS rn
	FROM recommendations_daily FINAL
	WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)} AND ` + deficiencyWindowSQL("window_end", timeBound) + `
)
WHERE rn = 1 AND fired = 1`)
	rowCount := 0
	firedTeams := map[string]struct{}{}
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var teamID, ruleID, ruleVersion, severity, title, rationale, successCriterion, windowStart, windowEnd string
		if err := row.Scan(&teamID, &ruleID, &ruleVersion, &severity, &title, &rationale, &successCriterion, &windowStart, &windowEnd); err != nil {
			return err
		}
		subject, ok := bySubject[teamID]
		if !ok {
			return nil
		}
		firedTeams[teamID] = struct{}{}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactOperationalDeficiencies, Subject: subject,
			Fields: map[string]contextfabric.FactValue{
				"rule_id":           stringOrNull(ruleID),
				"rule_version":      stringOrNull(ruleVersion),
				"severity":          stringOrNull(severity),
				"title":             stringOrNull(title),
				"rationale":         stringOrNull(rationale),
				"success_criterion": stringOrNull(successCriterion),
				"window_start":      contextfabric.StringFactValue(windowStart),
				"window_end":        contextfabric.StringFactValue(windowEnd),
			},
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, teamID)},
		})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query team operational deficiencies", scanErr)
	}
	evaluated, evaluation := coverage.classify(firedTeams, rowCount >= maxFactRowsPerQuery)
	state, retentionReason := timeBound.retentionState(rowCount)
	if len(evaluated) > 0 {
		// A subject with an evaluation inside the window and no fired rule
		// is a measured "nothing found": the source answered.
		state = contextfabric.SourceAvailable
	}
	retentionReason = evaluation.reason(len(evaluated) > 0 || len(facts) > 0, retentionReason)
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: rowCount >= maxFactRowsPerQuery, EvaluatedSubjects: evaluated, Evaluation: evaluation.coverage()}
	return result, nil
}

// deficiencyWindowSQL is the ONE temporal predicate every deficiency read
// applies to recommendations_daily.window_end: the requested range
// intersected with the freshness window that trails the request's as-of date.
// Fired facts, tombstones and the latest evaluation are all judged by it, so
// a row that one read counts as current is current for the others.
func deficiencyWindowSQL(column string, timeBound factTimeBound) string {
	predicate := freshnessIsFreshSQL(column, timeBound)
	if timeBound.active && timeBound.hasStart {
		predicate = "(" + predicate + " AND " + column + " >= toDate({" + boundStartParam + ":DateTime64(6,'UTC')}))"
	}
	return predicate
}

// Per-member evaluation states a deficiency read reports.
const (
	deficiencyStateFired        = "fired"
	deficiencyStateMeasuredZero = "measured_zero"
	deficiencyStateStale        = "stale"
	deficiencyStateBeforeRange  = "before_range"
	deficiencyStateNever        = "never_evaluated"
	deficiencyStateWithheld     = "withheld_capped_read"
)

// deficiencyEvaluation is the per-member evidence behind one read.
type deficiencyEvaluation struct {
	members         []contextfabric.FactEvaluationMember
	counts          map[string]int
	rules           int
	latestWindowEnd string
	capped          bool
}

func (e deficiencyEvaluation) coverage() *contextfabric.FactEvaluationCoverage {
	if len(e.members) == 0 {
		return nil
	}
	return &contextfabric.FactEvaluationCoverage{
		Covered: e.counts[deficiencyStateMeasuredZero], Stale: e.counts[deficiencyStateStale], BeforeRange: e.counts[deficiencyStateBeforeRange],
		NeverEvaluated: e.counts[deficiencyStateNever], Fired: e.counts[deficiencyStateFired],
		RulesEvaluated: e.rules, LatestWindowEnd: e.latestWindowEnd,
		FreshnessWindowDays: healthSeverityFreshnessWindowDays, Withheld: e.capped, Members: e.members,
	}
}

// reason names every requested team the read could not measure, whatever
// other teams it did measure. A read where every requested team was simply
// never evaluated keeps the generic empty-read reason.
func (e deficiencyEvaluation) reason(answered bool, fallback string) string {
	parts := make([]string, 0, 4)
	missing := 0
	for _, entry := range []struct{ state, text string }{
		{deficiencyStateStale, "evaluated outside the freshness window"},
		{deficiencyStateBeforeRange, "evaluated before the requested range"},
		{deficiencyStateNever, "never evaluated"},
		{deficiencyStateWithheld, "withheld by a capped read"},
	} {
		if n := e.counts[entry.state]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, entry.text))
			missing += n
		}
	}
	if missing == 0 {
		return ""
	}
	if !answered && missing == e.counts[deficiencyStateNever] {
		return fallback
	}
	return fmt.Sprintf("operational deficiency evaluation missing for %d of %d requested teams: %s", missing, len(e.members), strings.Join(parts, ", "))
}

// teamEvaluation is one team's latest evaluation at or before the as-of date.
type teamEvaluation struct {
	subject         contextfabric.SubjectRef
	latestWindowEnd string
	rules           int
	inWindow        bool
	fresh           bool
}

// readEvaluationCoverage reads, per requested team, the latest evaluation at
// or before the request's as-of date and where it sits against the shared
// window. The producer wrote one recommendations_daily row per rule per
// evaluation, fired or not, so a team with such a row was evaluated. One
// aggregate row per team, never row-capped.
func (p *OperationalDeficienciesProvider) readEvaluationCoverage(ctx context.Context, orgID string, ids []string, bySubject map[string]contextfabric.SubjectRef, timeBound factTimeBound) (map[string]teamEvaluation, error) {
	byTeam := make(map[string]teamEvaluation, len(ids))
	if len(ids) == 0 {
		return byTeam, nil
	}
	statement := `SELECT team_id, toString(mx), toUInt32(uniqExactIf(rule_id, window_end = mx)), toUInt8(` + deficiencyWindowSQL("mx", timeBound) + `), toUInt8(` + freshnessIsFreshSQL("mx", timeBound) + `)
FROM (
	SELECT team_id, rule_id, window_end, max(window_end) OVER (PARTITION BY team_id) AS mx
	FROM recommendations_daily FINAL
	WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)} AND window_end <= ` + freshnessAsOfDateSQL(timeBound) + `
)
GROUP BY team_id, mx`
	err := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var teamID, latest string
		var rules uint32
		var inWindow, fresh uint8
		if err := row.Scan(&teamID, &latest, &rules, &inWindow, &fresh); err != nil {
			return err
		}
		subject, ok := bySubject[teamID]
		if !ok {
			return nil
		}
		byTeam[teamID] = teamEvaluation{subject: subject, latestWindowEnd: latest, rules: int(rules), inWindow: inWindow != 0, fresh: fresh != 0}
		return nil
	}, timeBound.bindings()...)
	return byTeam, err
}

// evaluationCoverage is the per-team evaluation evidence for one read.
type evaluationCoverage struct {
	byTeam    map[string]teamEvaluation
	requested map[string]contextfabric.SubjectRef
}

// classify decides every requested team's state on its own: fired, measured
// zero, evaluated before the range, evaluated outside the freshness window,
// never evaluated, or withheld because the fired-rule read was capped (a
// fired rule may sit past the cap). It returns the measured-zero subjects.
func (c evaluationCoverage) classify(fired map[string]struct{}, capped bool) ([]contextfabric.SubjectRef, deficiencyEvaluation) {
	evaluation := deficiencyEvaluation{counts: map[string]int{}, capped: capped}
	var measured []contextfabric.SubjectRef
	teamIDs := make([]string, 0, len(c.requested))
	for teamID := range c.requested {
		teamIDs = append(teamIDs, teamID)
	}
	sort.Strings(teamIDs)
	for _, teamID := range teamIDs {
		team, ok := c.byTeam[teamID]
		_, hasFired := fired[teamID]
		var state string
		switch {
		case hasFired:
			state = deficiencyStateFired
		case !ok:
			state = deficiencyStateNever
		case !team.fresh:
			state = deficiencyStateStale
		case !team.inWindow:
			// Fresh against the as-of date yet outside the window: it sits
			// before the requested range's own start.
			state = deficiencyStateBeforeRange
		case capped:
			state = deficiencyStateWithheld
		default:
			state = deficiencyStateMeasuredZero
			if team.rules > evaluation.rules {
				evaluation.rules = team.rules
			}
			if team.latestWindowEnd > evaluation.latestWindowEnd {
				evaluation.latestWindowEnd = team.latestWindowEnd
			}
			measured = append(measured, team.subject)
		}
		evaluation.counts[state]++
		evaluation.members = append(evaluation.members, contextfabric.FactEvaluationMember{Subject: c.requested[teamID], State: state})
	}
	return measured, evaluation
}
