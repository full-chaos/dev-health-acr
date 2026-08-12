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
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, "deployment:")
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	statement := `SELECT d.deployment_id, ifNull(d.status, ''), ifNull(d.environment, '')
FROM deployments AS d FINAL
WHERE d.org_id = {org_id:String} AND d.deployment_id IN {ids:Array(String)}`
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
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
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query deployments", scanErr)
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion}, nil
}
