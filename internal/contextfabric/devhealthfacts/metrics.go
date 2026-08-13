package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const repositoryPrefix = "repository:"

// MetricsProvider implements contextfabric.FactProvider for FactMetrics from
// repo_metrics_daily -- the same nightly, precomputed-by-Ops delivery-metrics
// rollup devhealthsource would read if it needed repository metrics. This
// package never recomputes a metric: every column read here is already a
// finished, precomputed value written by Dev Health Ops; the query only ever
// selects the most recent day's row for each requested repository
// (argMax(..., day) -- CHAOS-3780's "current" time axis is "the latest known
// value", never a derived one).
type MetricsProvider struct{ facts clickhouseFacts }

func newMetricsProvider(client contextpacket.ClickHouseQueryClient) *MetricsProvider {
	return &MetricsProvider{facts: clickhouseFacts{client: client}}
}

func (p *MetricsProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactMetrics, "devhealthfacts.metrics", []contextfabric.SubjectKind{contextfabric.SubjectRepository})
}

func (p *MetricsProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	if result, unsupported := checkCurrentTimeOnly(query); unsupported {
		return result, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, repositoryPrefix)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// GROUP BY + argMax(..., day) picks the single most recent day's already-
	// computed row for each repository, never an ACR-side aggregate of the
	// underlying facts (H6: "current" means "latest known", not "derived").
	statement := withRowLimit(`SELECT toString(m.repo_id),
	toString(max(m.day)),
	toInt64(argMax(m.commits_count, m.day)),
	toInt64(argMax(m.prs_merged, m.day)),
	toFloat64(argMax(m.median_pr_cycle_hours, m.day)),
	toFloat64(argMax(m.change_failure_rate, m.day)),
	toUInt8(argMax(isNotNull(m.mttr_hours), m.day)),
	toFloat64(argMax(ifNull(m.mttr_hours, 0), m.day)),
	toInt64(argMax(m.bus_factor, m.day)),
	toFloat64(argMax(m.code_ownership_gini, m.day))
FROM repo_metrics_daily AS m
WHERE m.org_id = {org_id:String} AND toString(m.repo_id) IN {ids:Array(String)}
GROUP BY m.repo_id`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var repoID, day string
		var commitsCount, prsMerged, busFactor int64
		var medianPRCycleHours, changeFailureRate, ownershipGini, mttrHours float64
		var hasMTTR uint8
		if err := row.Scan(&repoID, &day, &commitsCount, &prsMerged, &medianPRCycleHours, &changeFailureRate, &hasMTTR, &mttrHours, &busFactor, &ownershipGini); err != nil {
			return err
		}
		subject, ok := bySubject[repoID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{
			"day":                   contextfabric.StringFactValue(day),
			"commits_count":         contextfabric.IntegerFactValue(commitsCount),
			"prs_merged":            contextfabric.IntegerFactValue(prsMerged),
			"median_pr_cycle_hours": contextfabric.NumberFactValue(medianPRCycleHours),
			"change_failure_rate":   contextfabric.NumberFactValue(changeFailureRate),
			"bus_factor":            contextfabric.IntegerFactValue(busFactor),
			"code_ownership_gini":   contextfabric.NumberFactValue(ownershipGini),
		}
		if hasMTTR != 0 {
			fields["mttr_hours"] = contextfabric.NumberFactValue(mttrHours)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactMetrics, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("repository", repoID)},
		})
		return nil
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query repository metrics", scanErr)
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion, Truncated: rowCount >= maxFactRowsPerQuery}, nil
}
