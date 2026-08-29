package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
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
//
// CHAOS-4363 widens FactWorkload to add SubjectProject: a project rolls up
// through team_project_ownership -> capacity_forecasts, the same real join
// metrics.go's readProjectMetrics uses for FactMetrics. Monte Carlo
// throughput/percentile stats are never additive across teams (summing two
// independent forecasts' throughput_mean is not a meaningful number), so the
// project-level fact carries every owning team's own latest per-scope
// forecast verbatim in a renderable team_breakdown table, never a summed or
// averaged project-native forecast.
type WorkloadProvider struct{ facts clickhouseFacts }

func newWorkloadProvider(client contextpacket.ClickHouseQueryClient) *WorkloadProvider {
	return &WorkloadProvider{facts: clickhouseFacts{client: client}}
}

func (p *WorkloadProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactWorkload, "devhealthfacts.workload", []contextfabric.SubjectKind{
		contextfabric.SubjectTeam, contextfabric.SubjectProject,
	})
}

func (p *WorkloadProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
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

	if teamSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectTeam); len(teamSubjects) > 0 {
		rowCount, scanErr := p.readTeamWorkload(ctx, orgID, teamSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team workload", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
	}

	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		rowCount, breakdownTruncated, scanErr := p.readProjectWorkload(ctx, orgID, projectSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project workload", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery || breakdownTruncated
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: truncated}, nil
}

// readTeamWorkload is CHAOS-3780's original capacity_forecasts read. The
// query itself (row_number() tiebreak over computed_at/forecast_id for the
// F3 multi-scope-per-team shape) now lives in readers.ReadTeamWorkload --
// see that function's doc comment for the full tiebreak reasoning. This
// adapter keeps the CanonicalFact-building half, factored out so ReadFacts
// can branch by subject kind the same way metrics.go/health.go already do.
func (p *WorkloadProvider) readTeamWorkload(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := subjectIndex(subjects, teamPrefix)
	rows, err := readers.ReadTeamWorkload(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		subject, ok := bySubject[r.TeamID]
		if !ok {
			continue
		}
		fields := map[string]contextfabric.FactValue{
			// basis states, in the fact's own structure (not only in this
			// file's doc comment), exactly what slice of "workload" this
			// value is: a precomputed Monte Carlo capacity forecast, never
			// a current/real-time load reading. A synthesizer must not
			// present this value as today's workload.
			"basis":                contextfabric.StringFactValue("capacity_forecast"),
			"throughput_mean":      contextfabric.NumberFactValue(r.ThroughputMean),
			"throughput_stddev":    contextfabric.NumberFactValue(r.ThroughputStddev),
			"insufficient_history": contextfabric.BooleanFactValue(r.InsufficientHistory != 0),
			"high_variance":        contextfabric.BooleanFactValue(r.HighVariance != 0),
			"backlog_size":         contextfabric.IntegerFactValue(r.BacklogSize),
			"computed_at":          contextfabric.StringFactValue(r.ComputedAt),
		}
		if r.WorkScopeID != "" {
			fields["work_scope_id"] = contextfabric.StringFactValue(r.WorkScopeID)
		}
		if r.HasP50Days != 0 {
			fields["forecast_p50_days"] = contextfabric.IntegerFactValue(r.P50Days)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactWorkload, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("team", r.TeamID)},
		})
	}
	return len(rows), nil
}

// readProjectWorkload rolls FactWorkload up for a project through
// projects -> team_project_ownership -> capacity_forecasts: every team
// owning the project contributes its own latest per-scope forecast,
// verbatim, into one renderable team_breakdown table -- Monte Carlo
// throughput/percentile stats are never summed or averaged across teams
// (see the package doc comment). The query itself now lives in
// readers.ReadProjectWorkload; this adapter does the Go-side
// grouping/breakdown-table construction the reader deliberately leaves to
// its caller.
func (p *WorkloadProvider) readProjectWorkload(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount int, breakdownTruncated bool, err error) {
	ids, bySubject := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, false, nil
	}
	scanned, err := readers.ReadProjectWorkload(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, false, err
	}
	rowCount = len(scanned)
	byProject := make(map[string][]readers.WorkloadProjectRow)
	var projectOrder []string
	for _, r := range scanned {
		if _, ok := bySubject[r.ProjectSubjectKey]; !ok {
			continue
		}
		if _, seen := byProject[r.ProjectSubjectKey]; !seen {
			projectOrder = append(projectOrder, r.ProjectSubjectKey)
		}
		byProject[r.ProjectSubjectKey] = append(byProject[r.ProjectSubjectKey], r)
	}
	for _, projectKey := range projectOrder {
		rows := byProject[projectKey]
		subject := bySubject[projectKey]
		seenTeamScope := make(map[string]bool, len(rows))
		seenTeams := make(map[string]bool, len(rows))
		teamRows := make([]contextfabric.FactValueRow, 0, len(rows))
		evidenceRefIDs := make([]string, 0, len(rows)+1)
		evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("project", projectKey))
		for _, r := range rows {
			dedupeKey := r.TeamID + "\x00" + r.WorkScopeID
			if dedupeTeamRow(seenTeamScope, dedupeKey) {
				continue
			}
			if !dedupeTeamRow(seenTeams, r.TeamID) {
				evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("team", r.TeamID))
			}
			rowFields := map[string]contextfabric.FactValue{
				"basis":                contextfabric.StringFactValue("capacity_forecast"),
				"team_id":              contextfabric.StringFactValue(r.TeamID),
				"team_name":            stringOrNull(r.TeamName),
				"throughput_mean":      contextfabric.NumberFactValue(r.ThroughputMean),
				"throughput_stddev":    contextfabric.NumberFactValue(r.ThroughputStddev),
				"insufficient_history": contextfabric.BooleanFactValue(r.InsufficientHistory != 0),
				"high_variance":        contextfabric.BooleanFactValue(r.HighVariance != 0),
				"backlog_size":         contextfabric.IntegerFactValue(r.BacklogSize),
				"computed_at":          contextfabric.StringFactValue(r.ComputedAt),
			}
			if r.WorkScopeID != "" {
				rowFields["work_scope_id"] = contextfabric.StringFactValue(r.WorkScopeID)
			}
			if r.HasP50Days != 0 {
				rowFields["forecast_p50_days"] = contextfabric.IntegerFactValue(r.P50Days)
			}
			teamRows = append(teamRows, contextfabric.FactValueRow{Fields: rowFields})
		}
		if len(teamRows) == 0 {
			continue
		}
		var omitted int
		teamRows, omitted = capFactValueRows(teamRows)
		breakdownTruncated = breakdownTruncated || omitted > 0
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactWorkload, Subject: subject,
			Fields: map[string]contextfabric.FactValue{
				"rollup_basis":   contextfabric.StringFactValue("project_work_scope_breakdown"),
				"team_count":     contextfabric.IntegerFactValue(int64(len(seenTeams))),
				"team_breakdown": contextfabric.RowsFactValue(teamRows),
			},
			EvidenceRefIDs: evidenceRefIDs,
		})
	}
	return rowCount, breakdownTruncated, nil
}
