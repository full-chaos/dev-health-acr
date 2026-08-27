package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
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

// readTeamWorkload is CHAOS-3780's original capacity_forecasts read,
// unchanged in behavior, factored out so ReadFacts can branch by subject
// kind the same way metrics.go/health.go already do.
func (p *WorkloadProvider) readTeamWorkload(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := subjectIndex(subjects, teamPrefix)
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
	WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)}` + timeBound.timestampPredicate("computed_at") + `
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
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactWorkload, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("team", teamID)},
		})
		return nil
	}, timeBound.bindings()...)
	return rowCount, scanErr
}

// workloadRollupRow is one (project, team, work_scope) triple's contribution
// to a project's workload rollup, scanned off the team_project_ownership
// join before Go-side grouping.
type workloadRollupRow struct {
	teamID, teamName, workScopeID, computedAt string
	throughputMean, throughputStddev          float64
	insufficientHistory, highVariance         bool
	backlogSize                               int64
	hasP50                                    bool
	p50Days                                   int64
}

// readProjectWorkload rolls FactWorkload up for a project through
// projects -> team_project_ownership -> capacity_forecasts: every team
// owning the project contributes its own latest per-scope forecast,
// verbatim, into one renderable team_breakdown table -- Monte Carlo
// throughput/percentile stats are never summed or averaged across teams
// (see the package doc comment).
func (p *WorkloadProvider) readProjectWorkload(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount int, breakdownTruncated bool, err error) {
	ids, bySubject := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, false, nil
	}
	ownershipPredicate := ownershipValidityPredicate(timeBound)
	statement := withRowLimit(`SELECT concat(p.provider, ':', p.id), tpo.team_id, ifNull(t.name, ''), ifNull(cf.work_scope_id, ''), cf.throughput_mean, cf.throughput_stddev, toUInt8(isNotNull(cf.p50_days)), toInt64(ifNull(cf.p50_days, 0)), cf.insufficient_history, cf.high_variance, toInt64(cf.backlog_size), toString(cf.computed_at)
FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
INNER JOIN (
	SELECT ifNull(team_id, '') AS team_id, work_scope_id, throughput_mean, throughput_stddev, p50_days, insufficient_history, high_variance, backlog_size, computed_at,
		row_number() OVER (PARTITION BY team_id, work_scope_id ORDER BY computed_at DESC, forecast_id DESC) AS rn
	FROM capacity_forecasts FINAL
	WHERE org_id = {org_id:String}` + timeBound.timestampPredicate("computed_at") + `
) AS cf ON cf.team_id = tpo.team_id AND cf.rn = 1
LEFT JOIN (SELECT id, name FROM teams FINAL WHERE org_id = {org_id:String}) AS t ON t.id = tpo.team_id
ORDER BY p.id, tpo.team_id, cf.work_scope_id`)
	byProject := make(map[string][]workloadRollupRow)
	var projectOrder []string
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var projectSubjectKey, teamID, teamName, workScopeID, computedAt string
		var throughputMean, throughputStddev float64
		var hasP50 uint8
		var p50Days int64
		var insufficientHistory, highVariance uint8
		var backlogSize int64
		if err := row.Scan(&projectSubjectKey, &teamID, &teamName, &workScopeID, &throughputMean, &throughputStddev, &hasP50, &p50Days, &insufficientHistory, &highVariance, &backlogSize, &computedAt); err != nil {
			return err
		}
		if _, ok := bySubject[projectSubjectKey]; !ok {
			return nil
		}
		if _, seen := byProject[projectSubjectKey]; !seen {
			projectOrder = append(projectOrder, projectSubjectKey)
		}
		byProject[projectSubjectKey] = append(byProject[projectSubjectKey], workloadRollupRow{
			teamID: teamID, teamName: teamName, workScopeID: workScopeID, computedAt: computedAt,
			throughputMean: throughputMean, throughputStddev: throughputStddev,
			insufficientHistory: insufficientHistory != 0, highVariance: highVariance != 0,
			backlogSize: backlogSize, hasP50: hasP50 != 0, p50Days: p50Days,
		})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return rowCount, false, scanErr
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
			dedupeKey := r.teamID + "\x00" + r.workScopeID
			if dedupeTeamRow(seenTeamScope, dedupeKey) {
				continue
			}
			if !dedupeTeamRow(seenTeams, r.teamID) {
				evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("team", r.teamID))
			}
			rowFields := map[string]contextfabric.FactValue{
				"basis":                contextfabric.StringFactValue("capacity_forecast"),
				"team_id":              contextfabric.StringFactValue(r.teamID),
				"team_name":            stringOrNull(r.teamName),
				"throughput_mean":      contextfabric.NumberFactValue(r.throughputMean),
				"throughput_stddev":    contextfabric.NumberFactValue(r.throughputStddev),
				"insufficient_history": contextfabric.BooleanFactValue(r.insufficientHistory),
				"high_variance":        contextfabric.BooleanFactValue(r.highVariance),
				"backlog_size":         contextfabric.IntegerFactValue(r.backlogSize),
				"computed_at":          contextfabric.StringFactValue(r.computedAt),
			}
			if r.workScopeID != "" {
				rowFields["work_scope_id"] = contextfabric.StringFactValue(r.workScopeID)
			}
			if r.hasP50 {
				rowFields["forecast_p50_days"] = contextfabric.IntegerFactValue(r.p50Days)
			}
			teamRows = append(teamRows, contextfabric.FactValueRow{Fields: rowFields})
		}
		if len(teamRows) == 0 {
			continue
		}
		var capped bool
		teamRows, capped = capFactValueRows(teamRows)
		breakdownTruncated = breakdownTruncated || capped
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactWorkload, Subject: subject,
			Fields: map[string]contextfabric.FactValue{
				"rollup_basis":   contextfabric.StringFactValue("team_project_ownership_breakdown"),
				"team_count":     contextfabric.IntegerFactValue(int64(len(seenTeams))),
				"team_breakdown": contextfabric.RowsFactValue(teamRows),
			},
			EvidenceRefIDs: evidenceRefIDs,
		})
	}
	return rowCount, breakdownTruncated, nil
}
