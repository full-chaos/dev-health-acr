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
	// argMax(..., computed_at) picks each team's single most recently
	// computed forecast, across every work_scope_id that team has been
	// forecast for -- never an ACR-side re-simulation.
	statement := withRowLimit(`SELECT ifNull(c.team_id, ''),
	toFloat64(argMax(c.throughput_mean, c.computed_at)),
	toFloat64(argMax(c.throughput_stddev, c.computed_at)),
	toUInt8(argMax(isNotNull(c.p50_days), c.computed_at)),
	toInt64(argMax(ifNull(c.p50_days, 0), c.computed_at)),
	toUInt8(argMax(c.insufficient_history, c.computed_at)),
	toUInt8(argMax(c.high_variance, c.computed_at)),
	toInt64(argMax(c.backlog_size, c.computed_at)),
	toString(max(c.computed_at))
FROM capacity_forecasts AS c
WHERE c.org_id = {org_id:String} AND c.team_id IN {ids:Array(String)}
GROUP BY c.team_id`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var teamID, computedAt string
		var throughputMean, throughputStddev float64
		var hasP50 uint8
		var p50Days int64
		var insufficientHistory, highVariance uint8
		var backlogSize int64
		if err := row.Scan(&teamID, &throughputMean, &throughputStddev, &hasP50, &p50Days, &insufficientHistory, &highVariance, &backlogSize, &computedAt); err != nil {
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
