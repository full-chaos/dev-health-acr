package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
	// See ci.go's identical comment: the provider now spans two grains, and
	// the coarser one wins the moment a repository aggregate is attempted.
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
		grain = grainDaily
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grain), Truncated: truncated}, nil
}

// readDeploymentStatus is CHAOS-3780's original deployments read, unchanged.
func (p *DeploymentsProvider) readDeploymentStatus(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := v2Index(subjects, identity.KindDeployment)
	if len(ids) == 0 {
		return 0, nil
	}
	// CHAOS-3781 Tier B: same shape as a CI run -- a deployment's final
	// status is only true once it finished. environment is an immutable
	// attribute of the deployment, so it needs no temporal treatment.
	statusExpression := "ifNull(d.status, '')"
	if timeBound.active {
		statusExpression = "if(d.finished_at IS NOT NULL AND d.finished_at <= " + timeBound.asOfExpression() +
			", ifNull(d.status, ''), 'in_progress')"
	}
	statement := withRowLimit(`SELECT d.deployment_id, ` + statusExpression + `, ifNull(d.environment, ''), toString(d.repo_id)
FROM deployments AS d FINAL
WHERE d.org_id = {org_id:String} AND concat(toString(d.repo_id), ':', d.deployment_id) IN {ids:Array(String)}` + timeBound.existencePredicate("coalesce(d.started_at, d.deployed_at)"))
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var deploymentID, status, environment, repoID string
		if err := row.Scan(&deploymentID, &status, &environment, &repoID); err != nil {
			return err
		}
		subject, ok := bySubject[repoID+":"+deploymentID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{"status": stringOrNull(status)}
		if environment != "" {
			fields["environment"] = contextfabric.StringFactValue(environment)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactDeployments, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("deployment", repoID+":"+deploymentID)},
		})
		return nil
	}, timeBound.bindings()...)
	return rowCount, scanErr
}

// readRepositoryAggregate reads deploy_metrics_daily (latest day per
// repository) -- CHAOS-4347's repository-scoped deployment aggregate. Same
// tiebreak discipline as ci.go's readRepositoryAggregate, for the identical
// reason (plain append-only MergeTree, no per-row unique id, populated by
// ops/src/dev_health_ops/metrics/compute_deployments.py's daily batch job).
func (p *DeploymentsProvider) readRepositoryAggregate(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := subjectIndex(subjects, repositoryPrefix)
	if len(ids) == 0 {
		return 0, nil
	}
	statement := withRowLimit(`SELECT toString(repo_id), toString(day), toInt64(deployments_count), toInt64(failed_deployments_count), toUInt8(isNotNull(deploy_time_p50_hours)), toFloat64(ifNull(deploy_time_p50_hours, 0)), toUInt8(isNotNull(lead_time_p50_hours)), toFloat64(ifNull(lead_time_p50_hours, 0))
FROM (
	SELECT repo_id, day, deployments_count, failed_deployments_count, deploy_time_p50_hours, lead_time_p50_hours,
		row_number() OVER (PARTITION BY repo_id ORDER BY day DESC, computed_at DESC, cityHash64(tuple(deployments_count, failed_deployments_count, ifNull(deploy_time_p50_hours, -1), ifNull(lead_time_p50_hours, -1))) DESC) AS rn
	FROM deploy_metrics_daily
	WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
)
WHERE rn = 1`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var repoID, day string
		var deploymentsCount, failedDeploymentsCount int64
		var hasDeployTime, hasLeadTime uint8
		var deployTime, leadTime float64
		if err := row.Scan(&repoID, &day, &deploymentsCount, &failedDeploymentsCount, &hasDeployTime, &deployTime, &hasLeadTime, &leadTime); err != nil {
			return err
		}
		subject, ok := bySubject[repoID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{
			"day":                      contextfabric.StringFactValue(day),
			"deployments_count":        contextfabric.IntegerFactValue(deploymentsCount),
			"failed_deployments_count": contextfabric.IntegerFactValue(failedDeploymentsCount),
		}
		if hasDeployTime != 0 {
			fields["deploy_time_p50_hours"] = contextfabric.NumberFactValue(deployTime)
		}
		if hasLeadTime != 0 {
			fields["lead_time_p50_hours"] = contextfabric.NumberFactValue(leadTime)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactDeployments, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("repository", repoID)},
		})
		return nil
	}, timeBound.bindings()...)
	return rowCount, scanErr
}
