package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ContinuousIntegrationProvider implements contextfabric.FactProvider for
// FactContinuousIntegration from ci_pipeline_runs.status -- the same column
// devhealthsource/tables.go's queryCIRuns already reads.
type ContinuousIntegrationProvider struct{ facts clickhouseFacts }

func newContinuousIntegrationProvider(client contextpacket.ClickHouseQueryClient) *ContinuousIntegrationProvider {
	return &ContinuousIntegrationProvider{facts: clickhouseFacts{client: client}}
}

func (p *ContinuousIntegrationProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactContinuousIntegration, "devhealthfacts.continuous_integration", []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectCIRun})
}

func (p *ContinuousIntegrationProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, "ci_pipeline_run:")
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	statement := `SELECT c.run_id, ifNull(c.status, '')
FROM ci_pipeline_runs AS c FINAL
WHERE c.org_id = {org_id:String} AND c.run_id IN {ids:Array(String)}`
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		var runID, status string
		if err := row.Scan(&runID, &status); err != nil {
			return err
		}
		subject, ok := bySubject[runID]
		if !ok {
			return nil
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactContinuousIntegration, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"status": stringOrNull(status)},
			EvidenceRefIDs: []string{evidenceRefID("ci", runID)},
		})
		return nil
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query ci pipeline runs", scanErr)
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion}, nil
}
