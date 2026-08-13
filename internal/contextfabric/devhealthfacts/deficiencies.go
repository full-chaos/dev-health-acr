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
type OperationalDeficienciesProvider struct{ facts clickhouseFacts }

func newOperationalDeficienciesProvider(client contextpacket.ClickHouseQueryClient) *OperationalDeficienciesProvider {
	return &OperationalDeficienciesProvider{facts: clickhouseFacts{client: client}}
}

func (p *OperationalDeficienciesProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactOperationalDeficiencies, "devhealthfacts.operational_deficiencies", []contextfabric.SubjectKind{contextfabric.SubjectTeam})
}

func (p *OperationalDeficienciesProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	if result, unsupported := checkCurrentTimeOnly(query); unsupported {
		return result, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, teamPrefix)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// fired = 1 restricts to rows Ops' own rule engine already decided are a
	// live deficiency finding; row_number() picks the most recently
	// evaluated window for each (team, rule) pair.
	statement := withRowLimit(`SELECT team_id, rule_id, rule_version, severity, title, rationale, success_criterion, toString(window_start), toString(window_end)
FROM (
	SELECT team_id, rule_id, rule_version, severity, title, rationale, success_criterion, window_start, window_end,
		row_number() OVER (PARTITION BY team_id, rule_id ORDER BY window_end DESC) AS rn
	FROM recommendations_daily
	WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)} AND fired = 1
)
WHERE rn = 1`)
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
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query team operational deficiencies", scanErr)
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion, Truncated: rowCount >= maxFactRowsPerQuery}, nil
}
