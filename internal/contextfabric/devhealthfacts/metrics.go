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
// finished, precomputed value written by Dev Health Ops.
//
// repo_metrics_daily is a plain, append-only MergeTree: live data shows up
// to 85 rows sharing one (repo_id, day) key (intraday reruns), and those
// reruns carry genuinely different values, not no-op repeats (Codex finding
// F2, confirmed against real ClickHouse data). row_number() OVER (PARTITION
// BY repo_id ORDER BY day DESC, computed_at DESC), picking rn=1 and scanning
// every field off that ONE row, is required here -- GROUP BY + independent
// per-field argMax(field, day) calls have no guarantee of breaking a day tie
// the same way across fields, so on a day with several reruns they can
// stitch a fact together from different rows, fabricating a combination
// that was never actually true at any single point in time.
//
// day DESC, computed_at DESC is still not a TOTAL order: repo_metrics_daily
// has no per-row unique id column, so two rows can share the exact same
// computed_at too (Codex round-2 finding M1 -- the same 86-way identical-
// computed_at tie this package found in compounding_risk_daily is possible
// here). Without a final tiebreaker, row_number() can pick a different one
// of those tied rows on different executions of the SAME query, which is
// itself a correctness defect (a fact that flaps between two truths with no
// data change). cityHash64 of the row's own value columns is the last
// ORDER BY term: it is arbitrary (there is no "more correct" row among an
// exact tie) but STABLE -- the same tied inputs always hash to the same
// value, so the same row wins every time.
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
	// The hash tiebreak's ifNull(mttr_hours, -1) sentinel is only
	// unambiguous while -1 is outside mttr_hours' real domain. mttr_hours
	// is a mean-time-to-recovery DURATION in hours, so it is semantically
	// always >= 0 (verified against live data: no negative value
	// observed). There is no ClickHouse-level UInt/CHECK constraint
	// enforcing this -- it is a domain assumption, not a type guarantee.
	statement := withRowLimit(`SELECT toString(repo_id), toString(day), toInt64(commits_count), toInt64(prs_merged), toFloat64(median_pr_cycle_hours), toFloat64(change_failure_rate), toUInt8(isNotNull(mttr_hours)), toFloat64(ifNull(mttr_hours, 0)), toInt64(bus_factor), toFloat64(code_ownership_gini)
FROM (
	SELECT repo_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini,
		row_number() OVER (PARTITION BY repo_id ORDER BY day DESC, computed_at DESC, cityHash64(tuple(commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, ifNull(mttr_hours, -1), bus_factor, code_ownership_gini)) DESC) AS rn
	FROM repo_metrics_daily
	WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)}
)
WHERE rn = 1`)
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
