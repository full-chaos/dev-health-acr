package devhealthfacts

import (
	"context"
	"sort"

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
	WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)}` + timeBound.timestampPredicate("window_end") + `
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
	evaluated, evaluation := coverage.measuredClear(firedTeams, rowCount >= maxFactRowsPerQuery)
	state, retentionReason := timeBound.retentionState(rowCount)
	if len(evaluated) > 0 {
		// A subject with an evaluation inside the freshness window and no
		// fired rule is a measured "nothing found": the source answered.
		state, retentionReason = contextfabric.SourceAvailable, ""
	} else if len(facts) == 0 {
		retentionReason = evaluation.noDataReason(retentionReason)
	}
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: rowCount >= maxFactRowsPerQuery, EvaluatedSubjects: evaluated, Evaluation: evaluation.coverage()}
	return result, nil
}

// deficiencyEvaluationReasonStale separates a subject whose rules were
// evaluated too long ago to speak for the request from one never evaluated,
// which keeps the generic empty-read reason.
const deficiencyEvaluationReasonStale = "operational deficiency rules were last evaluated for the requested teams outside the freshness window"

// deficiencyEvaluation is the coverage evidence behind a measured zero.
type deficiencyEvaluation struct {
	covered, stale, never int
	rules                 int
	latestWindowEnd       string
	withheld              bool
	requested             int
}

func (e deficiencyEvaluation) coverage() *contextfabric.FactEvaluationCoverage {
	if e.requested == 0 {
		return nil
	}
	return &contextfabric.FactEvaluationCoverage{
		Covered: e.covered, Stale: e.stale, NeverEvaluated: e.never,
		RulesEvaluated: e.rules, LatestWindowEnd: e.latestWindowEnd,
		FreshnessWindowDays: healthSeverityFreshnessWindowDays, Withheld: e.withheld,
	}
}

// noDataReason names why a read with no fired rule and no measured zero is
// empty. A team never evaluated keeps the generic absence reason; stale
// evaluations are named, alone or beside the absence.
func (e deficiencyEvaluation) noDataReason(fallback string) string {
	switch {
	case e.withheld || e.stale == 0:
		return fallback
	case e.never == 0:
		return deficiencyEvaluationReasonStale
	}
	return deficiencyEvaluationReasonStale + "; " + fallback
}

// teamEvaluation is one team's latest evaluation at or before the as-of date.
type teamEvaluation struct {
	subject         contextfabric.SubjectRef
	latestWindowEnd string
	rules           int
	fresh           bool
}

// readEvaluationCoverage reads, per requested team, the latest evaluation at
// or before the request's as-of date and whether it sits inside the freshness
// window. The producer wrote one recommendations_daily row per rule per
// evaluation, fired or not, so a team with such a row was evaluated. One
// aggregate row per team, never row-capped.
func (p *OperationalDeficienciesProvider) readEvaluationCoverage(ctx context.Context, orgID string, ids []string, bySubject map[string]contextfabric.SubjectRef, timeBound factTimeBound) (map[string]teamEvaluation, error) {
	byTeam := make(map[string]teamEvaluation, len(ids))
	if len(ids) == 0 {
		return byTeam, nil
	}
	statement := `SELECT team_id, toString(mx), toUInt32(uniqExactIf(rule_id, window_end = mx)), toUInt8(` + freshnessIsFreshSQL("mx", timeBound) + `)
FROM (
	SELECT team_id, rule_id, window_end, max(window_end) OVER (PARTITION BY team_id) AS mx
	FROM recommendations_daily FINAL
	WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)} AND window_end <= ` + freshnessAsOfDateSQL(timeBound) + `
)
GROUP BY team_id, mx`
	err := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var teamID, latest string
		var rules uint32
		var fresh uint8
		if err := row.Scan(&teamID, &latest, &rules, &fresh); err != nil {
			return err
		}
		subject, ok := bySubject[teamID]
		if !ok {
			return nil
		}
		byTeam[teamID] = teamEvaluation{subject: subject, latestWindowEnd: latest, rules: int(rules), fresh: fresh != 0}
		return nil
	}, timeBound.bindings()...)
	return byTeam, err
}

// evaluationCoverage is the per-team evaluation evidence for one read.
type evaluationCoverage struct {
	byTeam    map[string]teamEvaluation
	requested map[string]contextfabric.SubjectRef
}

// measuredClear returns the requested teams whose latest evaluation sits
// inside the freshness window and for which the fired-rule read returned
// nothing. A capped fired-rule read withholds every zero because a fired rule
// may sit past the cap.
func (c evaluationCoverage) measuredClear(fired map[string]struct{}, capped bool) ([]contextfabric.SubjectRef, deficiencyEvaluation) {
	evaluation := deficiencyEvaluation{requested: len(c.requested), withheld: capped}
	var measured []contextfabric.SubjectRef
	for teamID := range c.requested {
		team, ok := c.byTeam[teamID]
		switch {
		case !ok:
			evaluation.never++
		case !team.fresh:
			evaluation.stale++
		default:
			if _, hasFired := fired[teamID]; hasFired || capped {
				continue
			}
			evaluation.covered++
			if team.rules > evaluation.rules {
				evaluation.rules = team.rules
			}
			if team.latestWindowEnd > evaluation.latestWindowEnd {
				evaluation.latestWindowEnd = team.latestWindowEnd
			}
			measured = append(measured, team.subject)
		}
	}
	sort.Slice(measured, func(i, j int) bool { return measured[i].CanonicalID < measured[j].CanonicalID })
	return measured, evaluation
}
