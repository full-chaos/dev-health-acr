package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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

// readTeamInvestment is CHAOS-3780's original investment_metrics_daily read,
// unchanged in behavior, factored out so ReadFacts can branch by subject
// kind the same way metrics.go/health.go already do.
func (p *InvestmentProvider) readTeamInvestment(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, int, error) {
	ids, bySubject := subjectIndex(subjects, teamPrefix)
	// row_number() OVER (PARTITION BY team_id, investment_area,
	// project_stream ORDER BY day DESC, computed_at DESC) picks the single
	// most recent already-computed row for each (team, area, stream)
	// triple -- a selection, never an aggregation, of Ops' own published
	// rows. computed_at breaks same-day reruns deterministically (F4).
	//
	// day/computed_at is still not a TOTAL order (Codex round-2 finding
	// M1): investment_metrics_daily has no per-row unique id, so two rows
	// could share both. cityHash64 of the value columns is the final
	// tiebreaker -- arbitrary among an exact tie, but stable.
	statement := withRowLimit(`SELECT team_id, investment_area, project_stream, toString(day), toInt64(delivery_units), toInt64(work_items_completed), toInt64(prs_merged), churn_loc, cycle_p50_hours
FROM (
	SELECT team_id, investment_area, project_stream, day, delivery_units, work_items_completed, prs_merged, churn_loc, cycle_p50_hours,
		row_number() OVER (PARTITION BY team_id, investment_area, project_stream ORDER BY day DESC, computed_at DESC, cityHash64(tuple(delivery_units, work_items_completed, prs_merged, churn_loc, cycle_p50_hours)) DESC) AS rn
	FROM investment_metrics_daily
	WHERE org_id = {org_id:String} AND team_id IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
)
WHERE rn = 1`)
	rowCount := 0
	omittedUnrepresentableCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var teamID, investmentArea, projectStream, day string
		var deliveryUnits, workItemsCompleted, prsMerged int64
		// churn_loc is UInt64 and is NOT wrapped with toInt64 in SQL
		// (round-3 F2): the wrap turned a value above MaxInt64 negative,
		// and FactValue accepts negatives, so it would have reached a
		// public answer as a wrong number. Range-checked here instead.
		var rawChurnLOC uint64
		var cycleP50Hours float64
		if err := row.Scan(&teamID, &investmentArea, &projectStream, &day, &deliveryUnits, &workItemsCompleted, &prsMerged, &rawChurnLOC, &cycleP50Hours); err != nil {
			return err
		}
		churnLOC, representable := representableInt64(rawChurnLOC)
		if !representable {
			omittedUnrepresentableCount++
			return nil
		}
		subject, ok := bySubject[teamID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{
			"investment_area":      stringOrNull(investmentArea),
			"day":                  contextfabric.StringFactValue(day),
			"delivery_units":       contextfabric.IntegerFactValue(deliveryUnits),
			"work_items_completed": contextfabric.IntegerFactValue(workItemsCompleted),
			"prs_merged":           contextfabric.IntegerFactValue(prsMerged),
			"churn_loc":            contextfabric.IntegerFactValue(churnLOC),
			"cycle_p50_hours":      contextfabric.NumberFactValue(cycleP50Hours),
		}
		if projectStream != "" {
			fields["project_stream"] = contextfabric.StringFactValue(projectStream)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactInvestment, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("team", teamID)},
		})
		return nil
	}, timeBound.bindings()...)
	return rowCount, omittedUnrepresentableCount, scanErr
}

// investmentRollupRow is one (project, team) pair's contribution to a
// project's investment rollup, scanned off the team_project_ownership join
// before Go-side grouping. Investment rows are never summed across teams
// (see readProjectInvestment's doc comment) so this only needs to carry
// enough to render one team_breakdown row.
type investmentRollupRow struct {
	teamID, teamName, investmentArea, projectStream, day string
	deliveryUnits, workItemsCompleted, prsMerged         int64
	churnLOC                                             int64
	cycleP50Hours                                        float64
}

// readProjectInvestment rolls FactInvestment up for a project through
// projects -> team_project_ownership -> investment_metrics_daily: every
// team owning the project contributes its own latest (area, stream) rows,
// verbatim, into one renderable team_breakdown table. See this file's
// package-level doc comment for why counts are never summed across teams
// here (unlike metrics.go's commit counts).
func (p *InvestmentProvider) readProjectInvestment(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount, omittedUnrepresentableCount int, breakdownTruncated bool, err error) {
	ids, bySubject := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, 0, false, nil
	}
	ownershipPredicate := ownershipValidityPredicate(timeBound)
	statement := withRowLimit(`SELECT concat(p.provider, ':', p.id), tpo.team_id, ifNull(t.name, ''), im.investment_area, im.project_stream, toString(im.day), toInt64(im.delivery_units), toInt64(im.work_items_completed), toInt64(im.prs_merged), im.churn_loc, im.cycle_p50_hours
FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
INNER JOIN (
	SELECT team_id, investment_area, project_stream, day, delivery_units, work_items_completed, prs_merged, churn_loc, cycle_p50_hours,
		row_number() OVER (PARTITION BY team_id, investment_area, project_stream ORDER BY day DESC, computed_at DESC, cityHash64(tuple(delivery_units, work_items_completed, prs_merged, churn_loc, cycle_p50_hours)) DESC) AS rn
	FROM investment_metrics_daily
	WHERE org_id = {org_id:String}` + timeBound.dayPredicate("day") + `
) AS im ON im.team_id = tpo.team_id AND im.rn = 1
LEFT JOIN (SELECT id, name FROM teams FINAL WHERE org_id = {org_id:String}) AS t ON t.id = tpo.team_id
ORDER BY p.id, tpo.team_id, im.investment_area, im.project_stream`)
	byProject := make(map[string][]investmentRollupRow)
	var projectOrder []string
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var projectSubjectKey, teamID, teamName, investmentArea, projectStream, day string
		var deliveryUnits, workItemsCompleted, prsMerged int64
		var rawChurnLOC uint64
		var cycleP50Hours float64
		if err := row.Scan(&projectSubjectKey, &teamID, &teamName, &investmentArea, &projectStream, &day, &deliveryUnits, &workItemsCompleted, &prsMerged, &rawChurnLOC, &cycleP50Hours); err != nil {
			return err
		}
		if _, ok := bySubject[projectSubjectKey]; !ok {
			return nil
		}
		churnLOC, representable := representableInt64(rawChurnLOC)
		if !representable {
			// Round-1 P2: counted, not silently dropped -- the team-level
			// readTeamInvestment path already does this; the project rollup
			// must not report complete coverage while omitting a source row.
			omittedUnrepresentableCount++
			return nil
		}
		if _, seen := byProject[projectSubjectKey]; !seen {
			projectOrder = append(projectOrder, projectSubjectKey)
		}
		byProject[projectSubjectKey] = append(byProject[projectSubjectKey], investmentRollupRow{
			teamID: teamID, teamName: teamName, investmentArea: investmentArea, projectStream: projectStream, day: day,
			deliveryUnits: deliveryUnits, workItemsCompleted: workItemsCompleted, prsMerged: prsMerged,
			churnLOC: churnLOC, cycleP50Hours: cycleP50Hours,
		})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return rowCount, omittedUnrepresentableCount, false, scanErr
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
			dedupeKey := r.teamID + "\x00" + r.investmentArea + "\x00" + r.projectStream
			if dedupeTeamRow(seenTeamAreaStream, dedupeKey) {
				continue
			}
			if !dedupeTeamRow(seenTeams, r.teamID) {
				evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("team", r.teamID))
			}
			rowFields := map[string]contextfabric.FactValue{
				"team_id":              contextfabric.StringFactValue(r.teamID),
				"team_name":            stringOrNull(r.teamName),
				"day":                  contextfabric.StringFactValue(r.day),
				"delivery_units":       contextfabric.IntegerFactValue(r.deliveryUnits),
				"work_items_completed": contextfabric.IntegerFactValue(r.workItemsCompleted),
				"prs_merged":           contextfabric.IntegerFactValue(r.prsMerged),
				"churn_loc":            contextfabric.IntegerFactValue(r.churnLOC),
				"cycle_p50_hours":      contextfabric.NumberFactValue(r.cycleP50Hours),
				"investment_area":      stringOrNull(r.investmentArea),
			}
			if r.projectStream != "" {
				rowFields["project_stream"] = contextfabric.StringFactValue(r.projectStream)
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
		var capped bool
		teamRows, capped = capFactValueRows(teamRows)
		breakdownTruncated = breakdownTruncated || capped
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
