package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

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
type HealthProvider struct{ facts clickhouseFacts }

func newHealthProvider(client contextpacket.ClickHouseQueryClient) *HealthProvider {
	return &HealthProvider{facts: clickhouseFacts{client: client}}
}

func (p *HealthProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactHealth, "devhealthfacts.health", []contextfabric.SubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectTeam})
}

func (p *HealthProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	if result, unsupported := checkCurrentTimeOnly(query); unsupported {
		return result, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	facts := make([]contextfabric.CanonicalFact, 0, len(query.Subjects))
	truncated := false

	repoIDs, repoBySubject := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectRepository), repositoryPrefix)
	if len(repoIDs) > 0 {
		rowCount, scanErr := p.readScope(ctx, orgID, "repo", repoIDs, repoBySubject, "repository", &facts)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query repository health", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
	}

	teamIDs, teamBySubject := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectTeam), teamPrefix)
	if len(teamIDs) > 0 {
		rowCount, scanErr := p.readScope(ctx, orgID, "team", teamIDs, teamBySubject, "team", &facts)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team health", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
	}

	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion, Truncated: truncated}, nil
}

// readScope runs the compounding_risk_daily query for one scope ('repo' or
// 'team'), appending a CanonicalFact per matched subject into facts. scope is
// an internal Go string literal (never caller-supplied), so it is safe to
// inline into the statement the same way withRowLimit's maxFactRowsPerQuery
// is.
func (p *HealthProvider) readScope(ctx context.Context, orgID, scope string, ids []string, bySubject map[string]contextfabric.SubjectRef, evidenceEntityType string, facts *[]contextfabric.CanonicalFact) (int, error) {
	statement := withRowLimit(`SELECT c.scope_id,
	toString(argMax(c.severity, c.computed_at)),
	toUInt8(argMax(isNotNull(c.compounding_risk), c.computed_at)),
	toFloat64(argMax(ifNull(c.compounding_risk, 0), c.computed_at)),
	toString(max(c.computed_at))
FROM compounding_risk_daily AS c
WHERE c.org_id = {org_id:String} AND c.scope = '` + scope + `' AND c.scope_id IN {ids:Array(String)}
GROUP BY c.scope_id`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var scopeID, severity, computedAt string
		var hasRisk uint8
		var risk float64
		if err := row.Scan(&scopeID, &severity, &hasRisk, &risk, &computedAt); err != nil {
			return err
		}
		subject, ok := bySubject[scopeID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{
			"severity":    stringOrNull(severity),
			"computed_at": contextfabric.StringFactValue(computedAt),
		}
		if hasRisk != 0 {
			fields["compounding_risk"] = contextfabric.NumberFactValue(risk)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactHealth, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(evidenceEntityType, scopeID)},
		})
		return nil
	})
	return rowCount, scanErr
}
