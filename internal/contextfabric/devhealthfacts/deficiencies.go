package devhealthfacts

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"regexp"
	"sort"
	"strconv"
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
	statement := withRowLimit(`SELECT team_id, rule_id, rule_version, severity, title, rationale, success_criterion, toString(window_start), toString(window_end), toString(computed_at)
FROM (
	SELECT team_id, rule_id, rule_version, severity, title, rationale, success_criterion, window_start, window_end, computed_at, fired,
		row_number() OVER (PARTITION BY team_id, rule_id ORDER BY window_end DESC, computed_at DESC, cityHash64(tuple(fired, severity, title, rationale, success_criterion, rule_version, window_start)) DESC) AS rn
	FROM recommendations_daily FINAL
	WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)} AND ` + deficiencyWindowSQL("window_end", timeBound) + `
)
WHERE rn = 1 AND fired = 1
ORDER BY team_id, rule_id`)
	rowCount := 0
	firedTeams := map[string]struct{}{}
	var basisFacts []basisFact
	basisTeams := map[string]struct{}{}
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var teamID, ruleID, ruleVersion, severity, title, rationale, successCriterion, windowStart, windowEnd, computedAt string
		if err := row.Scan(&teamID, &ruleID, &ruleVersion, &severity, &title, &rationale, &successCriterion, &windowStart, &windowEnd, &computedAt); err != nil {
			return err
		}
		subject, ok := bySubject[teamID]
		if !ok {
			return nil
		}
		firedTeams[teamID] = struct{}{}
		fields := map[string]contextfabric.FactValue{
			"rule_id":           stringOrNull(ruleID),
			"rule_version":      stringOrNull(ruleVersion),
			"severity":          stringOrNull(severity),
			"title":             stringOrNull(title),
			"rationale":         stringOrNull(rationale),
			"success_criterion": stringOrNull(successCriterion),
			"window_start":      contextfabric.StringFactValue(windowStart),
			"window_end":        contextfabric.StringFactValue(windowEnd),
		}
		if ruleID == compoundingRiskRuleID {
			fields["rule_computed_at"] = contextfabric.StringFactValue(computedAt)
			basis, parsed := parseCompoundingRiskRationale(rationale)
			if parsed {
				fields["rule_basis_compounding_risk"] = contextfabric.NumberFactValue(basis.score)
				fields["rule_basis_severity"] = contextfabric.StringFactValue(basis.severity)
			} else {
				fields["rule_basis_unavailable_reason"] = contextfabric.StringFactValue(ruleBasisUnavailableNotRuleSentence)
				slog.Warn("devhealthfacts.rule_basis_unparsed", "rule_id", contextfabric.SanitizeLogAttr(ruleID))
			}
			basisFacts = append(basisFacts, basisFact{index: len(facts), teamID: teamID, basis: basis, parsed: parsed})
			basisTeams[teamID] = struct{}{}
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactOperationalDeficiencies, Subject: subject,
			Fields:         fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, teamID)},
		})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query team operational deficiencies", scanErr)
	}
	if len(basisTeams) > 0 {
		teamIDs := make([]string, 0, len(basisTeams))
		for id := range basisTeams {
			teamIDs = append(teamIDs, id)
		}
		sort.Strings(teamIDs)
		latest, latestErr := p.readLatestCompoundingRisk(ctx, orgID, teamIDs, timeBound)
		if latestErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team latest compounding risk", latestErr)
		}
		for _, bf := range basisFacts {
			current, found := latest[bf.teamID]
			maps.Copy(facts[bf.index].Fields, p.latestFields(current, found, bf))
		}
	}
	evaluated, evaluation := coverage.classify(firedTeams, rowCount >= maxFactRowsPerQuery)
	state, retentionReason := timeBound.retentionState(rowCount)
	if len(evaluated) > 0 {
		// A subject with an evaluation inside the window and no fired rule
		// is a measured "nothing found": the source answered.
		state = contextfabric.SourceAvailable
	}
	retentionReason, missingNote := evaluation.reason(len(evaluated) > 0 || len(facts) > 0, retentionReason)
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, RequestNote: missingNote, SubjectReasons: evaluation.memberReasons(), Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: rowCount >= maxFactRowsPerQuery, EvaluatedSubjects: evaluated, Evaluation: evaluation.coverage()}
	if len(ids) > 0 {
		owned, ownedErr := teamOwnedRepositories(ctx, p.facts.client, orgID, ids, timeBound)
		if ownedErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team owned repositories", ownedErr)
		}
		applyTeamsOwningNothing(&result, query.Subjects, len(ids), teamsOwningNothingWithoutData(ids, bySubject, owned, facts, evaluated))
	}
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

var deficiencyMissingStates = []struct{ state, text string }{
	{deficiencyStateStale, "evaluated outside the freshness window"},
	{deficiencyStateBeforeRange, "evaluated before the requested range"},
	{deficiencyStateNever, "never evaluated"},
	{deficiencyStateWithheld, "withheld by a capped read"},
}

// memberReasons is each unmeasured team's own state sentence.
func (e deficiencyEvaluation) memberReasons() map[string]string {
	out := map[string]string{}
	for _, member := range e.members {
		for _, entry := range deficiencyMissingStates {
			if member.State == entry.state {
				out[contextfabric.FactSubjectKey(member.Subject)] = "operational deficiency evaluation: " + entry.text
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// reason splits what a read says about teams it could not measure: the kind
// reason (true of every subject) and the request note (a count over the
// requested teams, which no single team's row may carry). A read where every
// requested team was simply never evaluated keeps the generic empty-read
// reason.
func (e deficiencyEvaluation) reason(answered bool, fallback string) (kindReason, requestNote string) {
	parts := make([]string, 0, 4)
	missing := 0
	for _, entry := range deficiencyMissingStates {
		if n := e.counts[entry.state]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, entry.text))
			missing += n
		}
	}
	if missing == 0 {
		return "", ""
	}
	if !answered && missing == e.counts[deficiencyStateNever] {
		return fallback, ""
	}
	return "", fmt.Sprintf("operational deficiency evaluation missing for %d of %d requested teams: %s", missing, len(e.members), strings.Join(parts, ", "))
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

const compoundingRiskRuleID = "compounding-risk"

// compoundingRiskMovedDelta is the score change, in compounding_risk units,
// that counts as "moved since the rule ran" when the severity band itself did
// not change.
const compoundingRiskMovedDelta = 0.1

const (
	latestUnavailableNoRow              = "no_compounding_risk_row"
	latestScoreUnavailableNull          = "compounding_risk_null_on_latest_row"
	ruleBasisUnavailableNotRuleSentence = "rationale_not_in_rule_sentence_form"
)

var compoundingRiskRationalePattern = regexp.MustCompile(`^Compounding Risk score is (\d+\.\d+) \(severity: (elevated|high)\)(?:, computed from \d+% of its inputs \((?:churn|complexity trend|ownership concentration|review latency)(?:, (?:churn|complexity trend|ownership concentration|review latency))*\))?\.`)

// ruleBasis is the score and band the Ops rule states it ran on, read from
// the rule's own rationale text (Ops stores no structured column for it).
type ruleBasis struct {
	score    float64
	severity string
}

type basisFact struct {
	index  int
	teamID string
	basis  ruleBasis
	parsed bool
}

// parseCompoundingRiskRationale reads the rule-time score and band. Any text
// that is not the rule's own sentence is unparsed: the caller serves no
// basis rather than a zero.
func parseCompoundingRiskRationale(rationale string) (ruleBasis, bool) {
	match := compoundingRiskRationalePattern.FindStringSubmatch(rationale)
	if match == nil {
		return ruleBasis{}, false
	}
	score, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return ruleBasis{}, false
	}
	return ruleBasis{score: score, severity: match[2]}, true
}

type latestCompoundingRisk struct {
	severity   string
	hasRisk    bool
	risk       float64
	computedAt string
	day        string
	known      bool
	fresh      bool
}

// readLatestCompoundingRisk reads each team's latest compounding_risk_daily
// row with the SAME pick health.go's readScope makes, so the deficiency fact
// and the health fact can never name two different latest rows.
func (p *OperationalDeficienciesProvider) readLatestCompoundingRisk(ctx context.Context, orgID string, ids []string, timeBound factTimeBound) (map[string]latestCompoundingRisk, error) {
	statement := withRowLimit(`SELECT scope_id, toString(severity), toUInt8(isNotNull(compounding_risk)), toFloat64(ifNull(compounding_risk, 0)), toString(computed_at), toString(day),
	toUInt8(` + freshnessIsKnownSQL("severity") + `),
	toUInt8(` + freshnessIsKnownSQL("severity") + ` AND ` + freshnessIsFreshSQL("day", timeBound) + `)
FROM (
	SELECT scope_id, severity, compounding_risk, computed_at, day,
		row_number() OVER (PARTITION BY scope_id ORDER BY (severity != 'unknown') DESC, day DESC, computed_at DESC, cityHash64(tuple(severity, ifNull(compounding_risk, -1), ifNull(churn_norm, -1), ifNull(complexity_norm, -1), ifNull(ownership_norm, -1), ifNull(review_norm, -1), w_churn, w_complexity, w_ownership, w_review)) DESC) AS rn
	FROM compounding_risk_daily
	WHERE org_id = {org_id:String} AND scope = 'team' AND scope_id IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
)
WHERE rn = 1
ORDER BY scope_id`)
	out := make(map[string]latestCompoundingRisk, len(ids))
	err := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var scopeID string
		var hasRisk, isKnown, isFresh uint8
		var latest latestCompoundingRisk
		if err := row.Scan(&scopeID, &latest.severity, &hasRisk, &latest.risk, &latest.computedAt, &latest.day, &isKnown, &isFresh); err != nil {
			return err
		}
		latest.hasRisk = hasRisk != 0
		latest.known = isKnown != 0
		latest.fresh = isFresh != 0
		out[scopeID] = latest
		return nil
	}, timeBound.bindings()...)
	return out, err
}

// latestFields names the latest computation beside the rule basis, with the
// same severity freshness policy the health fact applies. Nothing is
// zero-filled; every absent value carries its reason, and a move is stated
// only when it can be decided.
func (p *OperationalDeficienciesProvider) latestFields(latest latestCompoundingRisk, found bool, bf basisFact) map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{}
	if !found {
		fields["latest_unavailable_reason"] = contextfabric.StringFactValue(latestUnavailableNoRow)
		return fields
	}
	fields["latest_computed_at"] = contextfabric.StringFactValue(latest.computedAt)
	fields["latest_as_of_day"] = contextfabric.StringFactValue(latest.day)
	if latest.hasRisk {
		fields["latest_compounding_risk"] = contextfabric.NumberFactValue(latest.risk)
	} else {
		fields["latest_score_unavailable_reason"] = contextfabric.StringFactValue(latestScoreUnavailableNull)
	}
	bandServed := latest.known && latest.fresh
	switch {
	case bandServed:
		fields["latest_severity"] = contextfabric.StringFactValue(latest.severity)
	case latest.known:
		fields["latest_severity_unavailable_reason"] = contextfabric.StringFactValue(healthSeverityUnavailableReasonStaleBeyondWindow)
	default:
		fields["latest_severity_unavailable_reason"] = contextfabric.StringFactValue(healthSeverityUnavailableReasonNoKnownBand)
	}
	if !bf.parsed {
		return fields
	}
	switch {
	case latest.hasRisk:
		moved := math.Abs(latest.risk-bf.basis.score) >= compoundingRiskMovedDelta-1e-9
		if bandServed && latest.severity != bf.basis.severity {
			moved = true
		}
		fields["score_moved_since_rule"] = contextfabric.BooleanFactValue(moved)
	case bandServed && latest.severity != bf.basis.severity:
		fields["score_moved_since_rule"] = contextfabric.BooleanFactValue(true)
	}
	return fields
}
