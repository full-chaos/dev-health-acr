package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// ContinuousIntegrationProvider implements contextfabric.FactProvider for
// FactContinuousIntegration.
//
// CHAOS-3780 shipped ci_run-only, reading ci_pipeline_runs.status -- the
// same column devhealthsource/tables.go's queryCIRuns already reads.
//
// CHAOS-4347 adds a SECOND, repository-scoped shape reading
// cicd_metrics_daily -- Dev Health Ops' own precomputed daily CI rollup
// (pipelines_count, success_rate, and nullable duration/queue percentiles).
// This is genuinely a different granularity from the per-run status above
// (one row per repo per day, not per run), so it rides under a DISTINCT
// field set on the SAME FactContinuousIntegration kind -- the exact
// "widen by a real table, not a proxy" shape MetricsProvider's own
// CHAOS-4347 widening documents, applied here because cicd_metrics_daily
// is keyed by repository, so no project/team rollup question arises the
// way it did for FactMetrics.
type ContinuousIntegrationProvider struct{ facts clickhouseFacts }

func newContinuousIntegrationProvider(client contextpacket.ClickHouseQueryClient) *ContinuousIntegrationProvider {
	return &ContinuousIntegrationProvider{facts: clickhouseFacts{client: client}}
}

func (p *ContinuousIntegrationProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactContinuousIntegration, "devhealthfacts.continuous_integration", []contextfabric.SubjectKind{
		contractsv1.ContextFabricSubjectCIRun, contextfabric.SubjectRepository,
	})
}

func (p *ContinuousIntegrationProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
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
	rejectedCount := 0
	// CHAOS-5026: a defer, not a final-line call, so EVERY return path in
	// this function -- including one a future edit adds -- passes through
	// the disclosure, not just the one return statement someone remembered
	// to add it to (exactly the shape source_health.go's ReadFacts got
	// wrong: a 3rd success-shaped return with no call of its own). Skipped
	// when err != nil: an error return's FactProviderResult is never read
	// by the caller (Go convention), and the raw error already reports a
	// state at least as severe as SourceTruncated would.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.continuous_integration", contextfabric.FactContinuousIntegration, rejectedCount)
		}
	}()
	// grain starts at the run-status shape's exact precision and widens to
	// daily only once a repository aggregate ACTUALLY CONTRIBUTED a fact
	// (rowCount > 0) -- widening merely because a repository subject was
	// ATTEMPTED would mislabel a historical query that named both an
	// exact-grain ci_run and a repository with no retained aggregate row
	// for that day: the run-status fact that actually answered the
	// question would be reported at a coarser grain than it earned. An
	// answer is only as precise as its least precise CONTRIBUTING source
	// (timebound.go's effectiveGrain doc comment) -- "contributing" is the
	// operative word, not "queried".
	grain := grainExact

	if runSubjects := subjectsOfKind(query.Subjects, contractsv1.ContextFabricSubjectCIRun); len(runSubjects) > 0 {
		rowCount, rejected, scanErr := p.readRunStatus(ctx, orgID, runSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query ci pipeline runs", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
		rejectedCount += rejected
	}

	if repoSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectRepository); len(repoSubjects) > 0 {
		rowCount, rejected, scanErr := p.readRepositoryAggregate(ctx, orgID, repoSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query ci metrics", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
		rejectedCount += rejected
		if rowCount > 0 {
			grain = grainDaily
		}
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grain), Truncated: truncated}
	return result, nil
}

// readRunStatus is CHAOS-3780's original ci_pipeline_runs read. The SQL/scan
// half now lives in readers.ReadRunStatus (CHAOS-4377); see that function's
// doc comment for the CHAOS-3781 Tier B status-derivation reasoning this
// method used to carry inline.
func (p *ContinuousIntegrationProvider) readRunStatus(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, int, error) {
	ids, bySubject, rejected := v2Index(subjects, identity.KindCIPipelineRun)
	if len(ids) == 0 {
		return 0, rejected, nil
	}
	rows, err := readers.ReadRunStatus(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, rejected, err
	}
	for _, r := range rows {
		subject, ok := bySubject[r.RepoID+":"+r.RunID]
		if !ok {
			continue
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactContinuousIntegration, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"status": stringOrNull(r.Status)},
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityCI, r.RepoID+":"+r.RunID)},
		})
	}
	return len(rows), rejected, nil
}

// readRepositoryAggregate reads cicd_metrics_daily (latest day per
// repository) -- CHAOS-4347's repository-scoped CI aggregate. The SQL/scan
// half, including the row_number()/cityHash64 tiebreak reasoning, now lives
// in readers.ReadCICDMetricsDaily (CHAOS-4377).
func (p *ContinuousIntegrationProvider) readRepositoryAggregate(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, int, error) {
	ids, bySubject, rejected := subjectIndex(subjects, repositoryPrefix)
	if len(ids) == 0 {
		return 0, rejected, nil
	}
	rows, err := readers.ReadCICDMetricsDaily(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, rejected, err
	}
	for _, r := range rows {
		subject, ok := bySubject[r.RepoID]
		if !ok {
			continue
		}
		fields := map[string]contextfabric.FactValue{
			"day":             contextfabric.StringFactValue(r.Day),
			"pipelines_count": contextfabric.IntegerFactValue(r.PipelinesCount),
			"success_rate":    contextfabric.NumberFactValue(r.SuccessRate),
		}
		if r.HasAvgDuration {
			fields["avg_duration_minutes"] = contextfabric.NumberFactValue(r.AvgDuration)
		}
		if r.HasP90Duration {
			fields["p90_duration_minutes"] = contextfabric.NumberFactValue(r.P90Duration)
		}
		if r.HasAvgQueue {
			fields["avg_queue_minutes"] = contextfabric.NumberFactValue(r.AvgQueue)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactContinuousIntegration, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, r.RepoID)},
		})
	}
	return len(rows), rejected, nil
}
