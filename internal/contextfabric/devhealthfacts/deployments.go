package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// DeploymentsProvider implements contextfabric.FactProvider for
// FactDeployments from deployments.status/environment -- the same columns
// devhealthsource/tables.go's queryDeployments already reads.
type DeploymentsProvider struct{ facts clickhouseFacts }

func newDeploymentsProvider(client contextpacket.ClickHouseQueryClient) *DeploymentsProvider {
	return &DeploymentsProvider{facts: clickhouseFacts{client: client}}
}

func (p *DeploymentsProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactDeployments, "devhealthfacts.deployments", []contextfabric.SubjectKind{contextfabric.SubjectDeployment})
}

func (p *DeploymentsProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, "deployment:")
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// CHAOS-3781 Tier B: same shape as a CI run -- a deployment's final
	// status is only true once it finished. environment is an immutable
	// attribute of the deployment, so it needs no temporal treatment.
	statusExpression := "ifNull(d.status, '')"
	if timeBound.active {
		statusExpression = "if(d.finished_at IS NOT NULL AND d.finished_at <= " + timeBound.asOfExpression() +
			", ifNull(d.status, ''), 'in_progress')"
	}
	statement := withRowLimit(`SELECT d.deployment_id, ` + statusExpression + `, ifNull(d.environment, '')
FROM deployments AS d FINAL
WHERE d.org_id = {org_id:String} AND d.deployment_id IN {ids:Array(String)}` + timeBound.existencePredicate("coalesce(d.started_at, d.deployed_at)"))
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var deploymentID, status, environment string
		if err := row.Scan(&deploymentID, &status, &environment); err != nil {
			return err
		}
		subject, ok := bySubject[deploymentID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{"status": stringOrNull(status)}
		if environment != "" {
			fields["environment"] = contextfabric.StringFactValue(environment)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactDeployments, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("deployment", deploymentID)},
		})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query deployments", scanErr)
	}
	state, retentionReason := timeBound.retentionState(rowCount)
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: rowCount >= maxFactRowsPerQuery}, nil
}
