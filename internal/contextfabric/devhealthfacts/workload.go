package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// teamPrefix is the CanonicalID prefix this package uses for team subjects,
// mirroring workitems.go's workItemPrefix convention. No provider in this
// repository minted this convention before CHAOS-3780 (devhealthsource has
// no canonical team producer yet -- teams_projects.go's TeamsProjectsSource
// is a documented stub), so this is a new but consistent choice.
const teamPrefix = "team:"

// WorkloadProvider implements contextfabric.FactProvider for FactWorkload
// from capacity_forecasts -- Dev Health Ops' precomputed, team-level Monte
// Carlo capacity forecast (throughput, backlog burn-down percentiles). This
// provider reads the finished forecast only; it never re-runs the
// simulation or derives a workload judgment itself (§19.6.3: Ops stays the
// authority for workload semantics). The table has no person-level column
// at all -- every row is already team-aggregated -- so this satisfies the
// "no person-level workload output" constraint structurally, not by
// filtering.
//
// A team can be forecast under several distinct work_scope_id values at
// once -- live data shows one team with 12 concurrent scopes, computed
// within the same batch, with wildly different throughput/percentile
// values (Codex finding F3, confirmed against real ClickHouse data).
// Grouping by team_id alone silently keeps one scope's forecast and
// discards the other 11 with no record they existed. This provider instead
// partitions by (team_id, work_scope_id) and emits one CanonicalFact per
// scope, naming the scope in the payload, up to the row cap.
type WorkloadProvider struct{ facts clickhouseFacts }

func newWorkloadProvider(client contextpacket.ClickHouseQueryClient) *WorkloadProvider {
	return &WorkloadProvider{facts: clickhouseFacts{client: client}}
}

func (p *WorkloadProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactWorkload, "devhealthfacts.workload", []contextfabric.SubjectKind{contextfabric.SubjectTeam})
}

func (p *WorkloadProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	if result, unsupported := checkCurrentTimeOnly(query); unsupported {
		return result, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, teamPrefix)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// row_number() OVER (PARTITION BY team_id, work_scope_id ORDER BY
	// computed_at DESC) picks the single most recently computed forecast
	// for EACH scope a team has been forecast under, never collapsing
	// distinct scopes into one another. FINAL is defensive: capacity_forecasts
	// is ReplacingMergeTree(computed_at) sorted on (org_id, forecast_id), so
	// FINAL only collapses a re-emitted identical forecast_id, not distinct
	// scopes -- the row_number() partition is what actually resolves F3.
	//
	// computed_at DESC alone is not a TOTAL order (Codex round-2 finding
	// M1): two forecasts for the same scope could share a computed_at.
	// Unlike the other providers in this package, capacity_forecasts DOES
	// carry a real per-row unique id -- forecast_id -- so this provider uses
	// that as the final tiebreaker instead of a value hash.
	statement := withRowLimit(`SELECT team_id, ifNull(work_scope_id, ''), throughput_mean, throughput_stddev, toUInt8(isNotNull(p50_days)), toInt64(ifNull(p50_days, 0)), insufficient_history, high_variance, toInt64(backlog_size), toString(computed_at)
FROM (
	SELECT ifNull(team_id, '') AS team_id, work_scope_id, throughput_mean, throughput_stddev, p50_days, insufficient_history, high_variance, backlog_size, computed_at,
		row_number() OVER (PARTITION BY team_id, work_scope_id ORDER BY computed_at DESC, forecast_id DESC) AS rn
	FROM capacity_forecasts FINAL
	WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)}
)
WHERE rn = 1`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var teamID, workScopeID, computedAt string
		var throughputMean, throughputStddev float64
		var hasP50 uint8
		var p50Days int64
		var insufficientHistory, highVariance uint8
		var backlogSize int64
		if err := row.Scan(&teamID, &workScopeID, &throughputMean, &throughputStddev, &hasP50, &p50Days, &insufficientHistory, &highVariance, &backlogSize, &computedAt); err != nil {
			return err
		}
		subject, ok := bySubject[teamID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{
			// basis states, in the fact's own structure (not only in this
			// file's doc comment), exactly what slice of "workload" this
			// value is: a precomputed Monte Carlo capacity forecast, never
			// a current/real-time load reading. A synthesizer must not
			// present this value as today's workload.
			"basis":                contextfabric.StringFactValue("capacity_forecast"),
			"throughput_mean":      contextfabric.NumberFactValue(throughputMean),
			"throughput_stddev":    contextfabric.NumberFactValue(throughputStddev),
			"insufficient_history": contextfabric.BooleanFactValue(insufficientHistory != 0),
			"high_variance":        contextfabric.BooleanFactValue(highVariance != 0),
			"backlog_size":         contextfabric.IntegerFactValue(backlogSize),
			"computed_at":          contextfabric.StringFactValue(computedAt),
		}
		if workScopeID != "" {
			fields["work_scope_id"] = contextfabric.StringFactValue(workScopeID)
		}
		if hasP50 != 0 {
			fields["forecast_p50_days"] = contextfabric.IntegerFactValue(p50Days)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactWorkload, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("team", teamID)},
		})
		return nil
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query team workload", scanErr)
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion, Truncated: rowCount >= maxFactRowsPerQuery}, nil
}
