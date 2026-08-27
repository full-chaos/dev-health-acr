package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ReadinessProvider implements contextfabric.FactProvider for FactReadiness
// from estimate_coverage_metrics_daily -- Dev Health Ops' precomputed daily
// backlog estimate-coverage ratio (estimated vs. unestimated backlog items)
// for one team's work scope. This is a narrow, honest slice of "readiness":
// it answers "how much of this team's backlog is estimated", not a general
// release/ship-readiness judgment -- no broader canonical readiness signal
// exists in ClickHouse today, and this provider does not invent one
// (§19.6.3). The ratio itself is read exactly as Ops computed it; this
// provider never recomputes estimated_count/backlog_size into a ratio of
// its own.
//
// estimate_coverage_metrics_daily's own sort key is
// (org_id, day, provider, work_scope_id, team_id) -- two different source
// providers (live data: gitlab, linear) can report against the same
// work_scope_id string, so provider is part of this provider's partition
// key too (Codex finding F4), not folded away. The table is
// ReplacingMergeTree(computed_at): FINAL collapses an exact-key rerun, and
// row_number() ORDER BY day DESC, computed_at DESC (not day alone) still
// resolves the case where FINAL has not yet merged a same-day recompute.
// CHAOS-4363 widens FactReadiness to add SubjectProject: a project rolls up
// through team_project_ownership -> estimate_coverage_metrics_daily, the
// same real join metrics.go's readProjectMetrics uses for FactMetrics.
// estimate_coverage_metrics_daily partitions by (team, work_scope_id,
// provider), so summing estimated_count/backlog_size across teams that track
// DIFFERENT work scopes would mix unrelated backlogs into one meaningless
// total -- the project-level fact instead carries every owning team's own
// latest per-scope coverage row verbatim in a renderable team_breakdown
// table, never a summed or averaged project-native ratio.
type ReadinessProvider struct{ facts clickhouseFacts }

func newReadinessProvider(client contextpacket.ClickHouseQueryClient) *ReadinessProvider {
	return &ReadinessProvider{facts: clickhouseFacts{client: client}}
}

func (p *ReadinessProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactReadiness, "devhealthfacts.readiness", []contextfabric.SubjectKind{
		contextfabric.SubjectTeam, contextfabric.SubjectProject,
	})
}

func (p *ReadinessProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
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
		rowCount, scanErr := p.readTeamReadiness(ctx, orgID, teamSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team readiness", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
	}

	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		rowCount, breakdownTruncated, scanErr := p.readProjectReadiness(ctx, orgID, projectSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project readiness", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery || breakdownTruncated
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: truncated}, nil
}

// readTeamReadiness is CHAOS-3780's original estimate_coverage_metrics_daily
// read, unchanged in behavior, factored out so ReadFacts can branch by
// subject kind the same way metrics.go/health.go already do.
func (p *ReadinessProvider) readTeamReadiness(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := subjectIndex(subjects, teamPrefix)
	// row_number() OVER (PARTITION BY team_id, work_scope_id, provider
	// ORDER BY day DESC, computed_at DESC) picks the single most recent
	// already-computed row for each (team, work scope, provider) triple --
	// a team can have several concurrent work scopes (e.g. sprints)
	// tracked at once, and different source providers can share a
	// work_scope_id string.
	//
	// day/computed_at is still not a TOTAL order (Codex round-2 finding
	// M1): estimate_coverage_metrics_daily has no per-row unique id beyond
	// this partition's own key, so two rows could share both. cityHash64
	// of the value columns is the final tiebreaker -- arbitrary among an
	// exact tie, but stable. Its ifNull(ratio, -1) sentinel is only
	// unambiguous while -1 is outside ratio's real domain: ratio is
	// estimated_count/backlog_size, a fraction; live data ranges [0, 1],
	// never negative. There is no ClickHouse-level CHECK constraint
	// enforcing this -- it is a domain assumption, not a type guarantee.
	statement := withRowLimit(`SELECT team_id, work_scope_id, provider, toString(day), toInt64(estimated_count), toInt64(unestimated_count), toInt64(backlog_size), toUInt8(isNotNull(ratio)), toFloat64(ifNull(ratio, 0))
FROM (
	SELECT ifNull(team_id, '') AS team_id, work_scope_id, provider, day, estimated_count, unestimated_count, backlog_size, ratio,
		row_number() OVER (PARTITION BY team_id, work_scope_id, provider ORDER BY day DESC, computed_at DESC, cityHash64(tuple(estimated_count, unestimated_count, backlog_size, ifNull(ratio, -1))) DESC) AS rn
	FROM estimate_coverage_metrics_daily FINAL
	WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
)
WHERE rn = 1`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var teamID, workScopeID, provider, day string
		var estimatedCount, unestimatedCount, backlogSize int64
		var hasRatio uint8
		var ratio float64
		if err := row.Scan(&teamID, &workScopeID, &provider, &day, &estimatedCount, &unestimatedCount, &backlogSize, &hasRatio, &ratio); err != nil {
			return err
		}
		subject, ok := bySubject[teamID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{
			// basis states, in the fact's own structure (not only in this
			// file's doc comment), exactly what slice of "readiness" this
			// value is: backlog estimate coverage, never a general
			// release/ship-readiness verdict. A synthesizer must not
			// present this value as answering a broader readiness
			// question than it does.
			"basis":             contextfabric.StringFactValue("estimate_coverage"),
			"work_scope_id":     stringOrNull(workScopeID),
			"provider":          stringOrNull(provider),
			"day":               contextfabric.StringFactValue(day),
			"estimated_count":   contextfabric.IntegerFactValue(estimatedCount),
			"unestimated_count": contextfabric.IntegerFactValue(unestimatedCount),
			"backlog_size":      contextfabric.IntegerFactValue(backlogSize),
		}
		if hasRatio != 0 {
			fields["estimate_coverage_ratio"] = contextfabric.NumberFactValue(ratio)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactReadiness, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("team", teamID)},
		})
		return nil
	}, timeBound.bindings()...)
	return rowCount, scanErr
}

// readinessRollupRow is one (project, team, work_scope, provider) tuple's
// contribution to a project's readiness rollup, scanned off the
// team_project_ownership join before Go-side grouping.
type readinessRollupRow struct {
	teamID, teamName, workScopeID, provider, day  string
	estimatedCount, unestimatedCount, backlogSize int64
	hasRatio                                      bool
	ratio                                         float64
}

// readProjectReadiness rolls FactReadiness up for a project through
// projects -> team_project_ownership -> estimate_coverage_metrics_daily:
// every team owning the project contributes its own latest per-(work_scope,
// provider) coverage row, verbatim, into one renderable team_breakdown
// table -- see the package doc comment for why estimate/backlog counts are
// never summed across teams here.
func (p *ReadinessProvider) readProjectReadiness(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount int, breakdownTruncated bool, err error) {
	ids, bySubject := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, false, nil
	}
	ownershipPredicate := ownershipValidityPredicate(timeBound)
	statement := withRowLimit(`SELECT concat(p.provider, ':', p.id), tpo.team_id, ifNull(t.name, ''), ec.work_scope_id, ec.provider, toString(ec.day), toInt64(ec.estimated_count), toInt64(ec.unestimated_count), toInt64(ec.backlog_size), toUInt8(isNotNull(ec.ratio)), toFloat64(ifNull(ec.ratio, 0))
FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
INNER JOIN (
	SELECT ifNull(team_id, '') AS team_id, work_scope_id, provider, day, estimated_count, unestimated_count, backlog_size, ratio,
		row_number() OVER (PARTITION BY team_id, work_scope_id, provider ORDER BY day DESC, computed_at DESC, cityHash64(tuple(estimated_count, unestimated_count, backlog_size, ifNull(ratio, -1))) DESC) AS rn
	FROM estimate_coverage_metrics_daily FINAL
	WHERE org_id = {org_id:String}` + timeBound.dayPredicate("day") + `
) AS ec ON ec.team_id = tpo.team_id AND ec.rn = 1
LEFT JOIN (SELECT id, name FROM teams FINAL WHERE org_id = {org_id:String}) AS t ON t.id = tpo.team_id
ORDER BY p.id, tpo.team_id, ec.work_scope_id, ec.provider`)
	byProject := make(map[string][]readinessRollupRow)
	var projectOrder []string
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var projectSubjectKey, teamID, teamName, workScopeID, provider, day string
		var estimatedCount, unestimatedCount, backlogSize int64
		var hasRatio uint8
		var ratio float64
		if err := row.Scan(&projectSubjectKey, &teamID, &teamName, &workScopeID, &provider, &day, &estimatedCount, &unestimatedCount, &backlogSize, &hasRatio, &ratio); err != nil {
			return err
		}
		if _, ok := bySubject[projectSubjectKey]; !ok {
			return nil
		}
		if _, seen := byProject[projectSubjectKey]; !seen {
			projectOrder = append(projectOrder, projectSubjectKey)
		}
		byProject[projectSubjectKey] = append(byProject[projectSubjectKey], readinessRollupRow{
			teamID: teamID, teamName: teamName, workScopeID: workScopeID, provider: provider, day: day,
			estimatedCount: estimatedCount, unestimatedCount: unestimatedCount, backlogSize: backlogSize,
			hasRatio: hasRatio != 0, ratio: ratio,
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
			dedupeKey := r.teamID + "\x00" + r.workScopeID + "\x00" + r.provider
			if dedupeTeamRow(seenTeamScope, dedupeKey) {
				continue
			}
			if !dedupeTeamRow(seenTeams, r.teamID) {
				evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("team", r.teamID))
			}
			rowFields := map[string]contextfabric.FactValue{
				"basis":             contextfabric.StringFactValue("estimate_coverage"),
				"team_id":           contextfabric.StringFactValue(r.teamID),
				"team_name":         stringOrNull(r.teamName),
				"work_scope_id":     stringOrNull(r.workScopeID),
				"provider":          stringOrNull(r.provider),
				"day":               contextfabric.StringFactValue(r.day),
				"estimated_count":   contextfabric.IntegerFactValue(r.estimatedCount),
				"unestimated_count": contextfabric.IntegerFactValue(r.unestimatedCount),
				"backlog_size":      contextfabric.IntegerFactValue(r.backlogSize),
			}
			if r.hasRatio {
				rowFields["estimate_coverage_ratio"] = contextfabric.NumberFactValue(r.ratio)
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
			Kind: contextfabric.FactReadiness, Subject: subject,
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
