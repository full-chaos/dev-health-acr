package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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

func (p *ContinuousIntegrationProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
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
	// grain starts at the run-status shape's exact precision and widens to
	// daily the moment a repository aggregate is attempted -- an answer is
	// only as precise as its least precise contributing source
	// (timebound.go's effectiveGrain doc comment), and this provider is
	// now two different grains depending on which subject kinds a query
	// actually named.
	grain := grainExact

	if runSubjects := subjectsOfKind(query.Subjects, contractsv1.ContextFabricSubjectCIRun); len(runSubjects) > 0 {
		rowCount, scanErr := p.readRunStatus(ctx, orgID, runSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query ci pipeline runs", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
	}

	if repoSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectRepository); len(repoSubjects) > 0 {
		rowCount, scanErr := p.readRepositoryAggregate(ctx, orgID, repoSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query ci metrics", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
		grain = grainDaily
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grain), Truncated: truncated}, nil
}

// readRunStatus is CHAOS-3780's original ci_pipeline_runs read, unchanged.
func (p *ContinuousIntegrationProvider) readRunStatus(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := v2Index(subjects, identity.KindCIPipelineRun)
	if len(ids) == 0 {
		return 0, nil
	}
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
	statement := withRowLimit(`SELECT c.run_id, ` + statusExpression + `, toString(c.repo_id)
FROM ci_pipeline_runs AS c FINAL
WHERE c.org_id = {org_id:String} AND concat(toString(c.repo_id), ':', c.run_id) IN {ids:Array(String)}` + timeBound.existencePredicate("c.started_at"))
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var runID, status, repoID string
		if err := row.Scan(&runID, &status, &repoID); err != nil {
			return err
		}
		subject, ok := bySubject[repoID+":"+runID]
		if !ok {
			return nil
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactContinuousIntegration, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"status": stringOrNull(status)},
			EvidenceRefIDs: []string{evidenceRefID("ci", repoID+":"+runID)},
		})
		return nil
	}, timeBound.bindings()...)
	return rowCount, scanErr
}

// readRepositoryAggregate reads cicd_metrics_daily (latest day per
// repository) -- CHAOS-4347's repository-scoped CI aggregate.
//
// cicd_metrics_daily is a plain, append-only MergeTree table, the same
// intraday-rerun shape metrics.go's package doc comment documents for
// repo_metrics_daily/team_metrics_daily: row_number() OVER (PARTITION BY
// repo_id ORDER BY day DESC, computed_at DESC, cityHash64(...) DESC),
// picking rn=1, is required for the identical reason -- not verified
// separately against this specific table's live data, but the shape (plain
// MergeTree, no per-row unique id, populated by a daily batch job per
// ops/src/dev_health_ops/metrics/compute_cicd.py) is the same one that
// produced confirmed reruns and ties on every other table in this family,
// so the same defensive tiebreak is applied rather than assumed safe by a
// difference that isn't actually there.
func (p *ContinuousIntegrationProvider) readRepositoryAggregate(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := subjectIndex(subjects, repositoryPrefix)
	if len(ids) == 0 {
		return 0, nil
	}
	statement := withRowLimit(`SELECT toString(repo_id), toString(day), toInt64(pipelines_count), toFloat64(success_rate), toUInt8(isNotNull(avg_duration_minutes)), toFloat64(ifNull(avg_duration_minutes, 0)), toUInt8(isNotNull(p90_duration_minutes)), toFloat64(ifNull(p90_duration_minutes, 0)), toUInt8(isNotNull(avg_queue_minutes)), toFloat64(ifNull(avg_queue_minutes, 0))
FROM (
	SELECT repo_id, day, pipelines_count, success_rate, avg_duration_minutes, p90_duration_minutes, avg_queue_minutes,
		row_number() OVER (PARTITION BY repo_id ORDER BY day DESC, computed_at DESC, cityHash64(tuple(pipelines_count, success_rate, ifNull(avg_duration_minutes, -1), ifNull(p90_duration_minutes, -1), ifNull(avg_queue_minutes, -1))) DESC) AS rn
	FROM cicd_metrics_daily
	WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
)
WHERE rn = 1`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var repoID, day string
		var pipelinesCount int64
		var successRate float64
		var hasAvgDuration, hasP90Duration, hasAvgQueue uint8
		var avgDuration, p90Duration, avgQueue float64
		if err := row.Scan(&repoID, &day, &pipelinesCount, &successRate, &hasAvgDuration, &avgDuration, &hasP90Duration, &p90Duration, &hasAvgQueue, &avgQueue); err != nil {
			return err
		}
		subject, ok := bySubject[repoID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{
			"day":             contextfabric.StringFactValue(day),
			"pipelines_count": contextfabric.IntegerFactValue(pipelinesCount),
			"success_rate":    contextfabric.NumberFactValue(successRate),
		}
		if hasAvgDuration != 0 {
			fields["avg_duration_minutes"] = contextfabric.NumberFactValue(avgDuration)
		}
		if hasP90Duration != 0 {
			fields["p90_duration_minutes"] = contextfabric.NumberFactValue(p90Duration)
		}
		if hasAvgQueue != 0 {
			fields["avg_queue_minutes"] = contextfabric.NumberFactValue(avgQueue)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactContinuousIntegration, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("repository", repoID)},
		})
		return nil
	}, timeBound.bindings()...)
	return rowCount, scanErr
}
