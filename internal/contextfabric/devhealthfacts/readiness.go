package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
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
// read. The query itself (row_number() tiebreak over day/computed_at/
// cityHash64 for the multi-(work_scope, provider)-per-team shape) now lives
// in readers.ReadTeamReadiness -- see that function's doc comment for the
// full tiebreak reasoning. This adapter keeps the CanonicalFact-building
// half, factored out so ReadFacts can branch by subject kind the same way
// metrics.go/health.go already do.
func (p *ReadinessProvider) readTeamReadiness(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := subjectIndex(subjects, teamPrefix)
	rows, err := readers.ReadTeamReadiness(ctx, p.facts.client, orgID, ids, timeBound.neutral())
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
			// file's doc comment), exactly what slice of "readiness" this
			// value is: backlog estimate coverage, never a general
			// release/ship-readiness verdict. A synthesizer must not
			// present this value as answering a broader readiness
			// question than it does.
			"basis":             contextfabric.StringFactValue("estimate_coverage"),
			"work_scope_id":     stringOrNull(r.WorkScopeID),
			"provider":          stringOrNull(r.Provider),
			"day":               contextfabric.StringFactValue(r.Day),
			"estimated_count":   contextfabric.IntegerFactValue(r.EstimatedCount),
			"unestimated_count": contextfabric.IntegerFactValue(r.UnestimatedCount),
			"backlog_size":      contextfabric.IntegerFactValue(r.BacklogSize),
		}
		if r.HasRatio != 0 {
			fields["estimate_coverage_ratio"] = contextfabric.NumberFactValue(r.Ratio)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactReadiness, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("team", r.TeamID)},
		})
	}
	return len(rows), nil
}

// readProjectReadiness rolls FactReadiness up for a project through
// projects -> team_project_ownership -> estimate_coverage_metrics_daily:
// every team owning the project contributes its own latest per-(work_scope,
// provider) coverage row, verbatim, into one renderable team_breakdown
// table -- see the package doc comment for why estimate/backlog counts are
// never summed across teams here. The query itself now lives in
// readers.ReadProjectReadiness; this adapter does the Go-side
// grouping/breakdown-table construction the reader deliberately leaves to
// its caller.
func (p *ReadinessProvider) readProjectReadiness(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount int, breakdownTruncated bool, err error) {
	ids, bySubject := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, false, nil
	}
	scanned, err := readers.ReadProjectReadiness(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, false, err
	}
	rowCount = len(scanned)
	byProject := make(map[string][]readers.ReadinessProjectRow)
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
			dedupeKey := r.TeamID + "\x00" + r.WorkScopeID + "\x00" + r.Provider
			if dedupeTeamRow(seenTeamScope, dedupeKey) {
				continue
			}
			if !dedupeTeamRow(seenTeams, r.TeamID) {
				evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("team", r.TeamID))
			}
			rowFields := map[string]contextfabric.FactValue{
				"basis":             contextfabric.StringFactValue("estimate_coverage"),
				"team_id":           contextfabric.StringFactValue(r.TeamID),
				"team_name":         stringOrNull(r.TeamName),
				"work_scope_id":     stringOrNull(r.WorkScopeID),
				"provider":          stringOrNull(r.Provider),
				"day":               contextfabric.StringFactValue(r.Day),
				"estimated_count":   contextfabric.IntegerFactValue(r.EstimatedCount),
				"unestimated_count": contextfabric.IntegerFactValue(r.UnestimatedCount),
				"backlog_size":      contextfabric.IntegerFactValue(r.BacklogSize),
			}
			if r.HasRatio != 0 {
				rowFields["estimate_coverage_ratio"] = contextfabric.NumberFactValue(r.Ratio)
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
