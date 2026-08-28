package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// InvestmentProvider implements contextfabric.FactProvider for FactInvestment
// from investment_metrics_daily -- Dev Health Ops' precomputed daily
// investment-area/project-stream breakdown (delivery_units, work items,
// PRs merged, churn, cycle time). This provider is a pure passthrough of the
// most recent day's already-published rows; it never sums, ranks, or
// classifies -- investment_area/project_stream are read exactly as Ops
// assigned them (§19.6.3: Ops stays the authority for investment
// semantics). One team can have several (investment_area, project_stream)
// rows, so this provider -- like blockers.go's BlockersProvider -- returns
// zero or more CanonicalFacts per requested subject, not exactly one.
//
// investment_metrics_daily is a plain, append-only MergeTree: live data
// shows up to 25 rows sharing one (team_id, investment_area, project_stream,
// day) key (intraday reruns, Codex finding F4, confirmed against real
// ClickHouse data). ORDER BY day DESC alone leaves that same-day tie
// unresolved -- computed_at DESC breaks it deterministically, and because
// row_number() (not per-field argMax) is used, the winning row is always one
// whole row, never a stitched combination.
//
// CHAOS-4363 widens FactInvestment to add SubjectProject: a project rolls up
// through team_project_ownership -> investment_metrics_daily, the same real
// join metrics.go's readProjectMetrics uses for FactMetrics (never the
// CHAOS-4099 activity-proxy route -- see that function's doc comment for
// why). Unlike FactMetrics' commit counts, delivery_units/work_items_completed/
// prs_merged/churn_loc are NOT summed across owning teams here: a team's
// investment breakdown is partitioned by (investment_area, project_stream),
// and summing across teams that report against DIFFERENT areas would mix
// unrelated categories into one meaningless total (worse than metrics.go's
// ratio-averaging problem, because there is no shared unit across areas at
// all). The project-level fact instead carries every owning team's own
// (area, stream, day) rows verbatim in a renderable team_breakdown table,
// disclosed via rollup_basis -- never a project-native aggregate.
//
// investment_classifications_daily (the ticket's proposed "classification
// breakdown... where keyed by team") is deliberately NOT read here: its live
// production schema (verified against the kiac trial ClickHouse,
// system.columns, 2026-08-27) carries repo_id/artifact_id/artifact_type, no
// team_id column at all. There is no honest team-keyed join for it, the
// same gap CHAOS-4347's disposition inventory found for cognitive load
// (user_metrics_daily) -- inventing one would be exactly the "stub data for
// a kind with no canonical source" §19.6.3 forbids.
type InvestmentProvider struct{ facts clickhouseFacts }

func newInvestmentProvider(client contextpacket.ClickHouseQueryClient) *InvestmentProvider {
	return &InvestmentProvider{facts: clickhouseFacts{client: client}}
}

func (p *InvestmentProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactInvestment, "devhealthfacts.investment", []contextfabric.SubjectKind{
		contextfabric.SubjectTeam, contextfabric.SubjectProject,
	})
}

func (p *InvestmentProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
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
	omittedUnrepresentableCount := 0

	if teamSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectTeam); len(teamSubjects) > 0 {
		rowCount, omitted, scanErr := p.readTeamInvestment(ctx, orgID, teamSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team investment", scanErr)
		}
		omittedUnrepresentableCount += omitted
		truncated = truncated || rowCount >= maxFactRowsPerQuery
	}

	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		rowCount, omitted, breakdownTruncated, scanErr := p.readProjectInvestment(ctx, orgID, projectSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project investment", scanErr)
		}
		omittedUnrepresentableCount += omitted
		truncated = truncated || rowCount >= maxFactRowsPerQuery || breakdownTruncated
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	if omittedUnrepresentableCount > 0 && retentionReason == "" {
		retentionReason = unrepresentableValueReason
	}
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: truncated || omittedUnrepresentableCount > 0, OmittedCount: omittedUnrepresentableCount}, nil
}

// readTeamInvestment is CHAOS-3780's original investment_metrics_daily read.
// The query itself (row_number() tiebreak over day/computed_at/cityHash64
// for the F4 intraday-rerun shape) now lives in
// readers.ReadTeamInvestment -- see that function's doc comment for the
// full tiebreak reasoning. This adapter keeps the CanonicalFact-building
// half, factored out so ReadFacts can branch by subject kind the same way
// metrics.go/health.go already do.
func (p *InvestmentProvider) readTeamInvestment(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, int, error) {
	ids, bySubject := subjectIndex(subjects, teamPrefix)
	rows, err := readers.ReadTeamInvestment(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, 0, err
	}
	omittedUnrepresentableCount := 0
	for _, r := range rows {
		// churn_loc is UInt64 and is NOT wrapped with toInt64 in SQL
		// (round-3 F2): the wrap turned a value above MaxInt64 negative,
		// and FactValue accepts negatives, so it would have reached a
		// public answer as a wrong number. Range-checked here instead.
		churnLOC, representable := representableInt64(r.ChurnLOC)
		if !representable {
			omittedUnrepresentableCount++
			continue
		}
		subject, ok := bySubject[r.TeamID]
		if !ok {
			continue
		}
		fields := map[string]contextfabric.FactValue{
			"investment_area":      stringOrNull(r.InvestmentArea),
			"day":                  contextfabric.StringFactValue(r.Day),
			"delivery_units":       contextfabric.IntegerFactValue(r.DeliveryUnits),
			"work_items_completed": contextfabric.IntegerFactValue(r.WorkItemsCompleted),
			"prs_merged":           contextfabric.IntegerFactValue(r.PRsMerged),
			"churn_loc":            contextfabric.IntegerFactValue(churnLOC),
			"cycle_p50_hours":      contextfabric.NumberFactValue(r.CycleP50Hours),
		}
		if r.ProjectStream != "" {
			fields["project_stream"] = contextfabric.StringFactValue(r.ProjectStream)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactInvestment, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("team", r.TeamID)},
		})
	}
	return len(rows), omittedUnrepresentableCount, nil
}

// readProjectInvestment rolls FactInvestment up for a project through
// projects -> team_project_ownership -> investment_metrics_daily: every
// team owning the project contributes its own latest (area, stream) rows,
// verbatim, into one renderable team_breakdown table. The query itself now
// lives in readers.ReadProjectInvestment; this adapter does the Go-side
// grouping/breakdown-table construction the reader deliberately leaves to
// its caller. See this file's package-level doc comment for why counts are
// never summed across teams here (unlike metrics.go's commit counts).
func (p *InvestmentProvider) readProjectInvestment(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount, omittedUnrepresentableCount int, breakdownTruncated bool, err error) {
	ids, bySubject := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, 0, false, nil
	}
	scanned, err := readers.ReadProjectInvestment(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, 0, false, err
	}
	rowCount = len(scanned)
	byProject := make(map[string][]readers.InvestmentProjectRow)
	var projectOrder []string
	for _, r := range scanned {
		if _, ok := bySubject[r.ProjectSubjectKey]; !ok {
			continue
		}
		// churn_loc is UInt64 and is NOT wrapped with toInt64 in SQL
		// (round-3 F2): the wrap turned a value above MaxInt64 negative,
		// and FactValue accepts negatives, so it would have reached a
		// public answer as a wrong number. Range-checked here instead.
		if _, representable := representableInt64(r.ChurnLOC); !representable {
			// Round-1 P2: counted, not silently dropped -- the team-level
			// readTeamInvestment path already does this; the project rollup
			// must not report complete coverage while omitting a source row.
			omittedUnrepresentableCount++
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
		seenTeamAreaStream := make(map[string]bool, len(rows))
		seenTeams := make(map[string]bool, len(rows))
		teamRows := make([]contextfabric.FactValueRow, 0, len(rows))
		evidenceRefIDs := make([]string, 0, len(rows)+1)
		evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("project", projectKey))
		for _, r := range rows {
			dedupeKey := r.TeamID + "\x00" + r.InvestmentArea + "\x00" + r.ProjectStream
			if dedupeTeamRow(seenTeamAreaStream, dedupeKey) {
				continue
			}
			if !dedupeTeamRow(seenTeams, r.TeamID) {
				evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("team", r.TeamID))
			}
			// churnLOC's representability was already verified in the scan
			// loop above (non-representable rows never reach byProject), so
			// the conversion here cannot fail.
			churnLOC, _ := representableInt64(r.ChurnLOC)
			rowFields := map[string]contextfabric.FactValue{
				"team_id":              contextfabric.StringFactValue(r.TeamID),
				"team_name":            stringOrNull(r.TeamName),
				"day":                  contextfabric.StringFactValue(r.Day),
				"delivery_units":       contextfabric.IntegerFactValue(r.DeliveryUnits),
				"work_items_completed": contextfabric.IntegerFactValue(r.WorkItemsCompleted),
				"prs_merged":           contextfabric.IntegerFactValue(r.PRsMerged),
				"churn_loc":            contextfabric.IntegerFactValue(churnLOC),
				"cycle_p50_hours":      contextfabric.NumberFactValue(r.CycleP50Hours),
				"investment_area":      stringOrNull(r.InvestmentArea),
			}
			if r.ProjectStream != "" {
				rowFields["project_stream"] = contextfabric.StringFactValue(r.ProjectStream)
			}
			teamRows = append(teamRows, contextfabric.FactValueRow{Fields: rowFields})
		}
		if len(teamRows) == 0 {
			continue
		}
		// Round-1 P1: cap before RowsFactValue -- FactValue.Validate rejects
		// a table over 64 rows outright (model.go), which would turn a
		// large project's fact into a hard read error instead of an
		// honestly truncated answer.
		var omitted int
		teamRows, omitted = capFactValueRows(teamRows)
		breakdownTruncated = breakdownTruncated || omitted > 0
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactInvestment, Subject: subject,
			Fields: map[string]contextfabric.FactValue{
				// rollup_basis states, in the fact's own structure, that this
				// project-level fact is a per-team BREAKDOWN, never a summed
				// or averaged project-native total -- see the package doc
				// comment for why investment counts are not additive across
				// (investment_area, project_stream) the way metrics.go's
				// commit counts are.
				"rollup_basis":   contextfabric.StringFactValue("team_project_ownership_breakdown"),
				"team_count":     contextfabric.IntegerFactValue(int64(len(seenTeams))),
				"team_breakdown": contextfabric.RowsFactValue(teamRows),
			},
			EvidenceRefIDs: evidenceRefIDs,
		})
	}
	return rowCount, omittedUnrepresentableCount, breakdownTruncated, nil
}
