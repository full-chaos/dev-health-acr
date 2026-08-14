package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
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

func (p *OperationalDeficienciesProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, teamPrefix)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
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
			EvidenceRefIDs: []string{evidenceRefID("team", teamID)},
		})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query team operational deficiencies", scanErr)
	}
	state, retentionReason := timeBound.retentionState(rowCount)
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: queryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: rowCount >= maxFactRowsPerQuery}, nil
}
