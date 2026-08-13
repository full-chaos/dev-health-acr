package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
type ReadinessProvider struct{ facts clickhouseFacts }

func newReadinessProvider(client contextpacket.ClickHouseQueryClient) *ReadinessProvider {
	return &ReadinessProvider{facts: clickhouseFacts{client: client}}
}

func (p *ReadinessProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactReadiness, "devhealthfacts.readiness", []contextfabric.SubjectKind{contextfabric.SubjectTeam})
}

func (p *ReadinessProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	if result, unsupported := checkCurrentTimeOnly(query); unsupported {
		return result, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, teamPrefix)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// row_number() OVER (PARTITION BY team_id, work_scope_id ORDER BY day
	// DESC) picks the single most recent already-computed row for each
	// (team, work scope) pair a team can have several concurrent work
	// scopes (e.g. sprints) tracked at once.
	statement := withRowLimit(`SELECT team_id, work_scope_id, provider, toString(day), estimated_count, unestimated_count, backlog_size, toUInt8(isNotNull(ratio)), toFloat64(ifNull(ratio, 0))
FROM (
	SELECT ifNull(team_id, '') AS team_id, work_scope_id, provider, day, estimated_count, unestimated_count, backlog_size, ratio,
		row_number() OVER (PARTITION BY team_id, work_scope_id ORDER BY day DESC) AS rn
	FROM estimate_coverage_metrics_daily
	WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)}
)
WHERE rn = 1`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var teamID, workScopeID, provider, day string
		var estimatedCount, unestimatedCount, backlogSize int64
		var hasRatio uint8
		var ratio float64
		if err := row.Scan(&teamID, &workScopeID, &provider, &day, &estimatedCount, &unestimatedCount, &backlogSize, &hasRatio, &ratio); err != nil {
			return err
		}
		subject, ok := bySubject[teamID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{
			// basis states, in the fact's own structure (not only in this
			// file's doc comment), exactly what slice of "readiness" this
			// value is: backlog estimate coverage, never a general
			// release/ship-readiness verdict. A synthesizer must not
			// present this value as answering a broader readiness
			// question than it does.
			"basis":             contextfabric.StringFactValue("estimate_coverage"),
			"work_scope_id":     stringOrNull(workScopeID),
			"provider":          stringOrNull(provider),
			"day":               contextfabric.StringFactValue(day),
			"estimated_count":   contextfabric.IntegerFactValue(estimatedCount),
			"unestimated_count": contextfabric.IntegerFactValue(unestimatedCount),
			"backlog_size":      contextfabric.IntegerFactValue(backlogSize),
		}
		if hasRatio != 0 {
			fields["estimate_coverage_ratio"] = contextfabric.NumberFactValue(ratio)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactReadiness, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("team", teamID)},
		})
		return nil
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query team readiness", scanErr)
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion, Truncated: rowCount >= maxFactRowsPerQuery}, nil
}
