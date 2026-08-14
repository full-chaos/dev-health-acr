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
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, "ci_pipeline_run:")
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// CHAOS-3781 Tier B: a run's FINAL status only becomes true when the
	// run finishes. Reporting it for an instant while the run was still
	// executing would report an outcome that had not happened yet, so a
	// run unfinished at the requested time reports 'running' instead.
	// A run that had not started is excluded outright (AC-3781-3).
	statusExpression := "ifNull(c.status, '')"
	if timeBound.active {
		statusExpression = "if(c.finished_at IS NOT NULL AND c.finished_at <= " + timeBound.asOfExpression() +
			", ifNull(c.status, ''), 'running')"
	}
	statement := withRowLimit(`SELECT c.run_id, ` + statusExpression + `
FROM ci_pipeline_runs AS c FINAL
WHERE c.org_id = {org_id:String} AND c.run_id IN {ids:Array(String)}` + timeBound.existencePredicate("c.started_at"))
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
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
	}, timeBound.bindings()...)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query ci pipeline runs", scanErr)
	}
	state, retentionReason := timeBound.retentionState(rowCount)
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: queryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: rowCount >= maxFactRowsPerQuery}, nil
}
