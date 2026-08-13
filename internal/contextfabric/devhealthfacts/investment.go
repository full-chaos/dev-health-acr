package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// InvestmentProvider implements contextfabric.FactProvider for FactInvestment
// from investment_metrics_daily -- Dev Health Ops' precomputed daily
// investment-area/project-stream breakdown (delivery_units, work items,
// PRs merged, churn, cycle time). This provider is a pure passthrough of the
// most recent day's already-published rows; it never sums, ranks, or
// classifies -- investment_area/project_stream are read exactly as Ops
// assigned them (§19.6.3: Ops stays the authority for investment
// semantics). One team can have several (investment_area, project_stream)
// rows, so this provider -- like blockers.go's BlockersProvider -- returns
// zero or more CanonicalFacts per requested subject, not exactly one.
type InvestmentProvider struct{ facts clickhouseFacts }

func newInvestmentProvider(client contextpacket.ClickHouseQueryClient) *InvestmentProvider {
	return &InvestmentProvider{facts: clickhouseFacts{client: client}}
}

func (p *InvestmentProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactInvestment, "devhealthfacts.investment", []contextfabric.SubjectKind{contextfabric.SubjectTeam})
}

func (p *InvestmentProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	if result, unsupported := checkCurrentTimeOnly(query); unsupported {
		return result, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, teamPrefix)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// row_number() OVER (PARTITION BY team_id, investment_area,
	// project_stream ORDER BY day DESC) picks the single most recent
	// already-computed row for each (team, area, stream) triple -- a
	// selection, never an aggregation, of Ops' own published rows.
	statement := withRowLimit(`SELECT team_id, investment_area, project_stream, toString(day), delivery_units, work_items_completed, prs_merged, churn_loc, cycle_p50_hours
FROM (
	SELECT team_id, investment_area, project_stream, day, delivery_units, work_items_completed, prs_merged, churn_loc, cycle_p50_hours,
		row_number() OVER (PARTITION BY team_id, investment_area, project_stream ORDER BY day DESC) AS rn
	FROM investment_metrics_daily
	WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)}
)
WHERE rn = 1`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var teamID, investmentArea, projectStream, day string
		var deliveryUnits, workItemsCompleted, prsMerged, churnLOC int64
		var cycleP50Hours float64
		if err := row.Scan(&teamID, &investmentArea, &projectStream, &day, &deliveryUnits, &workItemsCompleted, &prsMerged, &churnLOC, &cycleP50Hours); err != nil {
			return err
		}
		subject, ok := bySubject[teamID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{
			"investment_area":      stringOrNull(investmentArea),
			"day":                  contextfabric.StringFactValue(day),
			"delivery_units":       contextfabric.IntegerFactValue(deliveryUnits),
			"work_items_completed": contextfabric.IntegerFactValue(workItemsCompleted),
			"prs_merged":           contextfabric.IntegerFactValue(prsMerged),
			"churn_loc":            contextfabric.IntegerFactValue(churnLOC),
			"cycle_p50_hours":      contextfabric.NumberFactValue(cycleP50Hours),
		}
		if projectStream != "" {
			fields["project_stream"] = contextfabric.StringFactValue(projectStream)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactInvestment, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("team", teamID)},
		})
		return nil
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query team investment", scanErr)
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion, Truncated: rowCount >= maxFactRowsPerQuery}, nil
}
