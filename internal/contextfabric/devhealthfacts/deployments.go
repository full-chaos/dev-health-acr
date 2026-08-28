package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// DeploymentsProvider implements contextfabric.FactProvider for
// FactDeployments.
//
// CHAOS-3780 shipped deployment-only, reading deployments.status/environment
// -- the same columns devhealthsource/tables.go's queryDeployments already
// reads.
//
// CHAOS-4347 adds a SECOND, repository-scoped shape reading
// deploy_metrics_daily -- Dev Health Ops' own precomputed daily deployment
// rollup (deployments_count, failed_deployments_count, and nullable
// duration percentiles). This is the same "widen by a real table, not a
// proxy" shape ContinuousIntegrationProvider's own CHAOS-4347 widening
// documents, for the identical reason: deploy_metrics_daily is keyed by
// repository, one row per repo per day, a different granularity from the
// per-deployment status/environment shape above -- so it rides under a
// DISTINCT field set on the SAME FactDeployments kind rather than
// colliding with it.
type DeploymentsProvider struct{ facts clickhouseFacts }

func newDeploymentsProvider(client contextpacket.ClickHouseQueryClient) *DeploymentsProvider {
	return &DeploymentsProvider{facts: clickhouseFacts{client: client}}
}

func (p *DeploymentsProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactDeployments, "devhealthfacts.deployments", []contextfabric.SubjectKind{
		contextfabric.SubjectDeployment, contextfabric.SubjectRepository,
	})
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
	facts := make([]contextfabric.CanonicalFact, 0, len(query.Subjects))
	truncated := false
	// See ci.go's identical comment: the coarser grain wins only once a
	// repository aggregate ACTUALLY CONTRIBUTED a fact, not merely because
	// one was attempted.
	grain := grainExact

	if deploymentSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectDeployment); len(deploymentSubjects) > 0 {
		rowCount, scanErr := p.readDeploymentStatus(ctx, orgID, deploymentSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query deployments", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
	}

	if repoSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectRepository); len(repoSubjects) > 0 {
		rowCount, scanErr := p.readRepositoryAggregate(ctx, orgID, repoSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query deployment metrics", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
		if rowCount > 0 {
			grain = grainDaily
		}
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grain), Truncated: truncated}, nil
}

// readDeploymentStatus is CHAOS-3780's original deployments read. The
// SQL/scan half now lives in readers.ReadDeploymentStatus (CHAOS-4377); see
// that function's doc comment for the CHAOS-3781 Tier B status-derivation
// reasoning this method used to carry inline.
func (p *DeploymentsProvider) readDeploymentStatus(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := v2Index(subjects, identity.KindDeployment)
	if len(ids) == 0 {
		return 0, nil
	}
	rows, err := readers.ReadDeploymentStatus(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		subject, ok := bySubject[r.RepoID+":"+r.DeploymentID]
		if !ok {
			continue
		}
		fields := map[string]contextfabric.FactValue{"status": stringOrNull(r.Status)}
		if r.Environment != "" {
			fields["environment"] = contextfabric.StringFactValue(r.Environment)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactDeployments, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("deployment", r.RepoID+":"+r.DeploymentID)},
		})
	}
	return len(rows), nil
}

// readRepositoryAggregate reads deploy_metrics_daily (latest day per
// repository) -- CHAOS-4347's repository-scoped deployment aggregate. The
// SQL/scan half, including the row_number()/cityHash64 tiebreak reasoning,
// now lives in readers.ReadDeployMetricsDaily (CHAOS-4377).
func (p *DeploymentsProvider) readRepositoryAggregate(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := subjectIndex(subjects, repositoryPrefix)
	if len(ids) == 0 {
		return 0, nil
	}
	rows, err := readers.ReadDeployMetricsDaily(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		subject, ok := bySubject[r.RepoID]
		if !ok {
			continue
		}
		fields := map[string]contextfabric.FactValue{
			"day":                      contextfabric.StringFactValue(r.Day),
			"deployments_count":        contextfabric.IntegerFactValue(r.DeploymentsCount),
			"failed_deployments_count": contextfabric.IntegerFactValue(r.FailedDeploymentsCount),
		}
		if r.HasDeployTime {
			fields["deploy_time_p50_hours"] = contextfabric.NumberFactValue(r.DeployTime)
		}
		if r.HasLeadTime {
			fields["lead_time_p50_hours"] = contextfabric.NumberFactValue(r.LeadTime)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactDeployments, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("repository", r.RepoID)},
		})
	}
	return len(rows), nil
}
