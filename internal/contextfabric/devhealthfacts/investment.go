package devhealthfacts

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// InvestmentProvider implements contextfabric.FactProvider for FactInvestment.
// A TEAM's investment is only the canonical theme mix over the repositories it
// owns (work_unit_investments, CHAOS-6559); the remainder of this comment
// describes the investment_metrics_daily read that the PROJECT roll-up still
// uses -- Dev Health Ops' precomputed daily
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
	capability := newCapability(contextfabric.FactInvestment, "devhealthfacts.investment", []contextfabric.SubjectKind{
		contextfabric.SubjectTeam, contextfabric.SubjectProject, contextfabric.SubjectRepository, contextfabric.SubjectOrganization,
	})
	capability.Tables = map[contextfabric.SubjectKind][]contextfabric.FactTableShape{
		contextfabric.SubjectTeam:         {contextfabric.FactTableBreakdown},
		contextfabric.SubjectRepository:   {contextfabric.FactTableBreakdown},
		contextfabric.SubjectProject:      {contextfabric.FactTableBreakdown},
		contextfabric.SubjectOrganization: {contextfabric.FactTableBreakdown},
	}
	capability.EstimatedItems = 20
	return capability
}

func (p *InvestmentProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ctx, span := withInvestmentSpan(ctx)
	facts := make([]contextfabric.CanonicalFact, 0, len(query.Subjects))
	truncated := false
	omittedUnrepresentableCount := 0
	rejectedCount := 0
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note. A team/project subject
	// named without its "team:"/project v2 shape reads no differently from
	// a subject with genuinely no investment data unless this runs -- see
	// subjectIndex/v2Index's own doc comments.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.investment", contextfabric.FactInvestment, rejectedCount)
		}
	}()

	var mixUnavailable string
	if teamSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectTeam); len(teamSubjects) > 0 {
		// CHAOS-6559: a team's investment is ONLY the canonical theme mix
		// (work_unit_investments via owned repositories). The deprecated
		// investment_metrics_daily per-day rows are never served for a team:
		// beside the mix they buried it (the model answered from the day rows
		// and said no shares existed), and without a mix they stood in for
		// it. A team with no mix is disclosed as unavailable instead.
		teamRejected, unavailable, scanErr := p.readTeamThemeMix(ctx, orgID, teamSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team theme mix", scanErr)
		}
		rejectedCount += teamRejected
		if unavailable > 0 {
			watermark, watermarkErr := p.readInvestmentWatermark(ctx, orgID)
			if watermarkErr != nil {
				return contextfabric.FactProviderResult{}, readFailure("query investment watermark", watermarkErr)
			}
			mixUnavailable = investmentMixUnavailableReason(unavailable, len(teamSubjects)-teamRejected, watermark)
		}
	}

	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		rowCount, omitted, rejected, breakdownTruncated, scanErr := p.readProjectInvestment(ctx, orgID, projectSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project investment", scanErr)
		}
		omittedUnrepresentableCount += omitted
		rejectedCount += rejected
		truncated = truncated || rowCount >= maxFactRowsPerQuery || breakdownTruncated
		// The canonical theme-mix roll-up is a deliberately SEPARATE call
		// from readProjectInvestment above -- see readProjectThemeMix's own
		// doc comment for why this is a new join, not a reuse of the legacy
		// investment_metrics_daily path. It shares projectSubjects with
		// readProjectInvestment above, so only that call's own rejectedCount
		// is folded in -- counting it twice here would double it.
		// unusableRollup carries the unit population of a roll-up that had
		// units but no positive effort, so a native mix that replaces it can
		// still disclose that population.
		unusableRollup := map[string]int64{}
		themeRowCount, themeScanErr := p.readProjectThemeMix(ctx, orgID, projectSubjects, &facts, timeBound, unusableRollup)
		if themeScanErr != nil {
			return contextfabric.FactProviderResult{}, mixReadFailure("query project theme mix", themeScanErr)
		}
		truncated = truncated || themeRowCount > maxFactRowsPerQuery
		// The project's OWN attribution replaces the roll-up's shares for a
		// project that has any, and leaves the roll-up in place for one that
		// has none. It reads after the roll-up so it can merge onto the same
		// fact and move the roll-up's population beside it.
		nativeRowCount, nativeScanErr := p.readProjectNativeThemeMix(ctx, orgID, projectSubjects, &facts, timeBound, unusableRollup)
		if nativeScanErr != nil {
			return contextfabric.FactProviderResult{}, mixReadFailure("query project native theme mix", nativeScanErr)
		}
		truncated = truncated || nativeRowCount > maxFactRowsPerQuery
	}

	if repoSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectRepository); len(repoSubjects) > 0 {
		rejected, scanErr := p.readRepositoryThemeMix(ctx, orgID, repoSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query repository theme mix", scanErr)
		}
		rejectedCount += rejected
	}

	orgRestricted := false
	if orgSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectOrganization); len(orgSubjects) > 0 {
		rejected, restricted, scanErr := p.readOrganizationThemeMix(ctx, principal, orgID, orgSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query organization theme mix", scanErr)
		}
		rejectedCount += rejected
		orgRestricted = restricted
	}

	unitsCut := false
	if unitsRequest, wanted := contextfabric.InvestmentUnitsFrom(ctx); wanted {
		for _, subject := range query.Subjects {
			prefix := teamPrefix
			if subject.Kind == contextfabric.SubjectRepository {
				prefix = repositoryPrefix
			} else if subject.Kind != contextfabric.SubjectTeam {
				continue
			}
			raw := strings.TrimPrefix(subject.CanonicalID, prefix)
			if raw == "" || raw == subject.CanonicalID || !bindingSafeKey(raw) {
				continue
			}
			unitFacts, more, unitErr := p.readInvestmentUnits(ctx, orgID, subject, raw, unitsRequest, timeBound)
			if unitErr != nil {
				return contextfabric.FactProviderResult{}, readFailure("query investment units", unitErr)
			}
			facts = append(facts, unitFacts...)
			unitsCut = unitsCut || more
		}
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	// CHAOS-4521b: this source has no project dimension, so an all-project
	// read that came back empty says something more specific than "no rows".
	retentionReason = explainTeamScopedProjectAbsence(timeBound, state, retentionReason, query.Subjects)
	if omittedUnrepresentableCount > 0 && retentionReason == "" {
		retentionReason = unrepresentableValueReason
	}
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: truncated || omittedUnrepresentableCount > 0, OmittedCount: omittedUnrepresentableCount}
	if mixUnavailable != "" {
		mergeFactReadReason(&result, mixUnavailable)
	}
	// Every requested subject is checked, with or without a fact: a window
	// with no overlapping unit serves no fact and must still say the window
	// starts before the stored history.
	for _, subject := range query.Subjects {
		if reason := span.reasonFor(subject.CanonicalID, subject.Kind == contextfabric.SubjectProject, timeBound); reason != "" {
			mergeFactReadReason(&result, reason)
		}
	}
	if orgRestricted {
		if len(facts) == 0 {
			result.State = contextfabric.SourceNotApplicable
			result.Reason = investmentOrgRestrictedReason
		} else {
			mergeFactReadReason(&result, investmentOrgRestrictedReason)
		}
	}
	if unitsCut {
		result.Truncated = true
		mergeFactReadReason(&result, unitFactReasonCut)
	}
	return result, nil
}

// canonicalInvestmentThemes is the fixed 5-theme taxonomy
// (ops/src/dev_health_ops/investment_taxonomy.py's THEMES; AGENTS.md: "no
// synonyms/overrides") in a stable iteration order, so readTeamThemeMix's
// normalization is deterministic regardless of map iteration order.
var canonicalInvestmentThemes = [...]string{
	contextfabric.ThemeFeatureDelivery, contextfabric.ThemeOperational,
	contextfabric.ThemeMaintenance, contextfabric.ThemeQuality, contextfabric.ThemeRisk,
}

// readTeamThemeMix reads the CANONICAL investment theme/subcategory
// distribution (CHAOS-4398 §0: work_unit_investments) for the given team
// subjects -- NEVER investment_metrics_daily, the deprecated legacy rule set.
// A team's mix is the sum of the mixes of the repositories the team owns
// (CHAOS-6559, chris ruling 2026-09-24; see investment_repo_mix.go).
//
// Each team with a mix gets ONE standalone FactInvestment fact carrying the
// theme_*/prior_theme_*/theme_quality_bugfix scalars and the theme_breakdown
// table. It is never merged onto another fact: a team's window mix is not an
// attribute of any single day row, and a mix riding on a per-day fact was read
// by the model as that day's attribute and reported as "no shares".
// internal/contextfabric/cohort_ranking.go's investmentMixSignal finds the
// fact by field PRESENCE (theme_feature_delivery), never by position.
//
// timeBound.neutral() bounds the CURRENT window read. When timeBound also
// carries an explicit start (never inferred -- CHAOS-4040: a window this
// producer invented on its own would be exactly the "commit under an
// inferred window" the ticket forbids), the same statement also reads the
// prior comparable window [start-duration, start) for RankCohort's mix-shift
// sub-signal. A team with no prior-window data gets NO prior_theme_* fields
// at all -- omitted, never zero-filled.
//
// A team with zero current-window weighted effort (no owned repository, or
// owned repositories with no persisted work) gets NO fact: a fabricated 0.0
// share across all five themes would read as "we know this team's mix is
// exactly nothing" rather than "we have no mix to report". Such teams are
// counted in unavailable so the caller can say so; shapeRejected counts
// subjects whose id did not have the team shape (CHAOS-5026).
func (p *InvestmentProvider) readTeamThemeMix(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (shapeRejected, unavailable int, err error) {
	ids, bySubject, shapeRejected := subjectIndex(subjects, teamPrefix)
	if len(ids) == 0 {
		return shapeRejected, 0, nil
	}
	// CHAOS-6559 (chris ruling 2026-09-24): a team's mix is the SUM of the
	// mixes of the repositories the team OWNS (team_repo_ownership), each
	// repository's mix being the PR-ref-share partition of persisted work
	// unit distributions (investment_repo_mix.go). Never a member vote and
	// never a work-item majority vote.
	var priorBound *factTimeBound
	if timeBound.active && timeBound.hasStart {
		duration := timeBound.end.Sub(timeBound.start)
		priorBound = &factTimeBound{active: true, hasStart: true, start: timeBound.start.Add(-duration), end: timeBound.start}
	}
	current, prior, err := p.teamOwnedRepoMix(ctx, orgID, ids, timeBound, priorBound)
	if err != nil {
		return shapeRejected, 0, err
	}

	teamIDs := make([]string, 0, len(ids))
	for _, teamID := range ids {
		if _, ok := bySubject[teamID]; ok {
			teamIDs = append(teamIDs, teamID)
		}
	}
	sort.Strings(teamIDs)
	for _, teamID := range teamIDs {
		subject := bySubject[teamID]
		m, ok := current[teamID]
		if !ok || m.total() <= 0 {
			unavailable++
			continue
		}
		currentTotal := m.total()
		fields := make(map[string]contextfabric.FactValue, 2*len(canonicalInvestmentThemes)+5)
		for _, theme := range canonicalInvestmentThemes {
			fields[contextfabric.FactFieldTheme(theme)] = contextfabric.NumberFactValue(roundMixEffort(m.theme[theme] / currentTotal))
		}
		fields[contextfabric.FactFieldThemeQualityBugfix] = contextfabric.NumberFactValue(roundMixEffort(m.bugfix / currentTotal))
		fields["theme_breakdown"] = themeBreakdownTable(m, timeBound.effectiveGrain(grainDaily))
		fields["owned_repository_count"] = contextfabric.IntegerFactValue(m.repos)
		fields["mix_source"] = contextfabric.StringFactValue(repoMixSource)
		fields["attribution_basis"] = contextfabric.StringFactValue(repoMixBasis + "_over_team_repo_ownership")

		if priorMix, ok := prior[teamID]; ok && priorMix.total() > 0 {
			priorTotal := priorMix.total()
			for _, theme := range canonicalInvestmentThemes {
				fields[contextfabric.FactFieldPriorTheme(theme)] = contextfabric.NumberFactValue(roundMixEffort(priorMix.theme[theme] / priorTotal))
			}
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactInvestment, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, teamID)},
		})
	}
	return shapeRejected, unavailable, nil
}

// investmentWatermarkStatement reads the newest computed_at of the org's
// persisted work unit distributions: how fresh the data behind a mix is.
const investmentWatermarkStatement = `SELECT toString(max(computed_at)), count() FROM work_unit_investments WHERE org_id = {org_id:String}`

// readInvestmentWatermark returns the newest computed_at of the org's
// work_unit_investments, or "" when the table holds no row for the org.
func (p *InvestmentProvider) readInvestmentWatermark(ctx context.Context, orgID string) (string, error) {
	watermark := ""
	err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadInvestmentWatermark", withRowLimit(investmentWatermarkStatement), orgID, []string{}, func(row contextpacket.ClickHouseRowScanner) error {
		var maxComputedAt string
		var rows uint64
		if err := row.Scan(&maxComputedAt, &rows); err != nil {
			return err
		}
		if rows > 0 {
			watermark = maxComputedAt
		}
		return nil
	})
	return watermark, err
}

// investmentMixUnavailableReason is the disclosure for teams that have no
// canonical mix: unknown is not healthy and is not a zero mix (North Star
// check 12), and the watermark says how fresh the data that was searched is.
func investmentMixUnavailableReason(unavailable, requested int, watermark string) string {
	reason := fmt.Sprintf("investment mix unavailable for %d of %d requested teams: no repository the team owns has persisted work unit effort in the requested window", unavailable, requested)
	if watermark == "" {
		return reason + "; work_unit_investments holds no rows for this organization"
	}
	return reason + "; work_unit_investments last computed at " + watermark
}

// readProjectInvestment rolls FactInvestment up for a project through
// projects -> team_project_ownership -> investment_metrics_daily: every
// team owning the project contributes its own latest (area, stream) rows,
// verbatim, into one renderable team_breakdown table. The query itself now
// lives in readers.ReadProjectInvestment; this adapter does the Go-side
// grouping/breakdown-table construction the reader deliberately leaves to
// its caller. The reader sums the newest row of each repository inside one
// (team, area, stream) key. See this file's package-level doc comment for why
// counts are never summed across teams here (unlike metrics.go's commit counts).
func (p *InvestmentProvider) readProjectInvestment(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount, omittedUnrepresentableCount, rejected int, breakdownTruncated bool, err error) {
	ids, bySubject, rejected := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, 0, rejected, false, nil
	}
	scanned, err := readers.ReadProjectInvestment(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, 0, rejected, false, err
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
			// Round-1 P2: counted, not silently dropped -- the project rollup
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
		evidenceRefIDs = append(evidenceRefIDs, evidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, projectKey))
		for _, r := range rows {
			dedupeKey := r.TeamID + "\x00" + r.InvestmentArea + "\x00" + r.ProjectStream
			if dedupeTeamRow(seenTeamAreaStream, dedupeKey) {
				continue
			}
			if !dedupeTeamRow(seenTeams, r.TeamID) {
				evidenceRefIDs = append(evidenceRefIDs, evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, r.TeamID))
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
				"investment_area":      stringOrNull(r.InvestmentArea),
			}
			// A key spanning several repositories has no exact median: the
			// reader marks cycle_p50_hours known only for a single repository,
			// and the weighted mean is an approximation served under its own
			// name. A row with no completed work item has no median, so an
			// unknown value is an absent cell, never 0.
			if r.CycleP50Known && r.WorkItemsCompleted > 0 {
				rowFields["cycle_p50_hours"] = contextfabric.NumberFactValue(r.CycleP50Hours)
			}
			if r.CycleP50HoursWeightedMeanKnown {
				rowFields["cycle_p50_hours_weighted_mean"] = contextfabric.NumberFactValue(r.CycleP50HoursWeightedMean)
			}
			// CHAOS-4633: normalized to always-present (null when absent)
			// rather than conditionally omitted -- project_stream is part
			// of this row's declared Key (dedupeKey above already keys on
			// it), and a Key column must be present on every row.
			rowFields["project_stream"] = stringOrNull(r.ProjectStream)
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
				"rollup_basis": contextfabric.StringFactValue("team_project_ownership_breakdown"),
				"team_count":   contextfabric.IntegerFactValue(int64(len(seenTeams))),
				// CHAOS-4633 P1: Key = [team_id, team_name, day,
				// investment_area, project_stream] -- dedupeKey above
				// already partitions rows on (team_id, investment_area,
				// project_stream); day rides along as an identity column
				// (each team's own latest day), never a Measure.
				"team_breakdown": contextfabric.TableFactValue(contextfabric.FactTable{
					Shape: contextfabric.FactTableBreakdown,
					Key:   []string{"team_id", "team_name", "day", "investment_area", "project_stream"},
					Measures: []string{
						"delivery_units", "work_items_completed", "prs_merged",
						"churn_loc", "cycle_p50_hours", "cycle_p50_hours_weighted_mean",
					},
					Grain: timeBound.effectiveGrain(grainDaily),
					Rows:  teamRows,
				}),
			},
			EvidenceRefIDs: evidenceRefIDs,
		})
	}
	return rowCount, omittedUnrepresentableCount, rejected, breakdownTruncated, nil
}

// evidenceVoteAttributedPredicate is the ONE "does this evidence-vote row
// carry a real team attribution" check for a nullable/empty-able join key --
// a LEFT JOIN miss against wita leaves t.team_id NULL, and this expression
// must stay NULL (not fall back to a concrete value) for that row, or an
// unmatched row reads as attributed. nullIf(t.team_id, ”) alone has this
// property: NULL stays NULL, an empty-string team_id also collapses to
// NULL, and only a real, non-empty team_id survives to compare NOT NULL.
// readers.ReadTeamThemeMix's own analogous count expression has this same
// no-fallback shape; this is the one definition for this producer's
// evidence vote, so a second call site reuses it rather than re-deriving
// an equivalent-looking expression that quietly adds a fallback and stops
// discriminating.
const evidenceVoteAttributedPredicate = "nullIf(t.team_id, '') IS NOT NULL"

// themeInvestmentRangePredicate mirrors dev-health-go's
// readers.TimeBound.rangePredicate (investment_theme.go, unexported there):
// a work_unit_investments row is included whenever any part of its own
// [from_ts, to_ts] evidence window overlaps the requested [start, end) --
// to_ts is the INCLUSIVE last-evidence instant, so a unit whose to_ts equals
// the window start is inside it (ops api/queries/investment.py:518,555,593
// use the same `to_ts >= start_ts`; ops is the authority for investment
// semantics) --
// an overlap test against a row's own two-sided range, unlike
// factTimeBound's dayPredicate/timestampPredicate (a single row-level
// column compared to one instant or a half-open range). An inactive bound
// (the current axis) applies no predicate.
func themeInvestmentRangePredicate(b factTimeBound, fromColumn, toColumn string) string {
	if !b.active {
		return ""
	}
	predicate := " AND " + fromColumn + " < {" + boundEndParam + ":DateTime64(6,'UTC')}"
	if b.hasStart {
		predicate += " AND " + toColumn + " >= {" + boundStartParam + ":DateTime64(6,'UTC')}"
	}
	return predicate
}

// readProjectThemeMix rolls FactInvestment's canonical theme_*/
// theme_quality_bugfix fields up for a PROJECT (CHAOS-5930): the top-level
// scalar cohort_ranking.go's investmentMixSignal reads
// (theme_feature_delivery, subject-kind-blind) off ANY FactInvestment fact,
// promoted here for project subjects.
//
// Population/join: a team-weighted roll-up via the project's owning teams'
// repositories, into work_unit_investments -- projects ->
// team_project_ownership (the project's owning teams,
// deduplicated the same way readProjectHealth/readProjectInvestment already
// dedupe a team owning a project through more than one ownership `source`
// row) -> team_repo_ownership (those teams' owned repos, deduplicated per
// (project, repo) so a repo owned through more than one source, or by more
// than one of the project's owning teams, is never counted twice) ->
// work_unit_investments, joined DIRECTLY on its own repo_id column.
//
// This is deliberately NOT readTeamThemeMix's join. That reader resolves a
// work unit's structural_evidence_json (issue/PR refs) to a repo and then
// to ONE team via work_item_team_attributions' majority vote, because it
// must decide which single team a work unit belongs to. A project's
// roll-up asks a narrower, repo-membership question instead -- "is this
// work unit's own repo one this project's owning teams own" -- answerable
// directly off work_unit_investments.repo_id (declared in devhealthschema's
// read set; live-schema verified against the trial ClickHouse,
// acr-trial-data/dh_0906), without parsing structural_evidence_json or the
// team-attribution vote at all. A work unit whose repo_id IS NULL is
// invisible to this roll-up -- disclosed by omission (work_unit_count
// counts only what was reachable), never fabricated into a share.
//
// One row per (project, work_unit_id) by construction: a work unit carries
// exactly one repo_id, and project_repo below is already deduplicated to
// one row per (project, repo), so the join cannot fan a work unit out more
// than once for the same project. A repo (or team) belonging to more than
// one project legitimately contributes to EACH project's own roll-up -- a
// shared repo or team is a coverage filter, never a weight -- and a work
// unit is never double-attributed WITHIN one project's own total.
//
// A project with zero attributed work units, or whose attributed work
// units sum to zero effort across the five canonical themes, gets NO theme
// fields at all (never a fabricated 0.0 share) -- the same
// degrade-not-fabricate rule readTeamThemeMix's own doc comment states for
// the team subject.
//
// Fields MERGE onto the project's existing FactInvestment fact
// (readProjectInvestment's team_breakdown fact, appended BEFORE this call in
// ReadFacts) when one exists. That fact is ONE per project and carries only
// the Rows-shaped team_breakdown table (which the model-facing projection
// drops) plus rollup_basis/team_count scalars, so the merged fact stays the
// project's single investment fact and the theme scalars are model-visible
// on it. A project with no such fact still gets a standalone one. (A team is
// different: it never merges, see readTeamThemeMix.)
//
// The statement returns at most ONE row per requested project (every
// aggregate collapses to project_key), probed at maxFactRowsProbe
// (withRowProbeLimit) rather than a plain LIMIT, so a project population
// sitting exactly at the served cap is distinguishable from one that
// overflowed it -- the same discipline workItemProjectCompletionStatement
// documents for its own project-grain aggregate.
func (p *InvestmentProvider) readProjectThemeMix(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound, unusableRollup map[string]int64) (rowCount int, err error) {
	ids, bySubject, _ := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, nil
	}
	// readers.QueryOrgScopedNamed, never p.facts.query: this is genuinely
	// raw SQL (not a readers.ReadXxx call), but it must still report
	// through the SAME readers.Instrumentation hook NewInstrumentedProviders
	// wires into ctx (instrumentation.go's own doc comment) -- p.facts.query
	// is devhealthfacts's own acr-side mirror of this exact function,
	// deliberately without the readers-package instrumentation piece, so
	// using it here would silently drop this read out of the slog/span/
	// counter coverage every other reader-backed read in this package
	// carries (readRepositoryMetricsSeries's identical raw-SQL shape
	// already makes this same choice, for the same reason).
	// contextpacket.ClickHouseQueryClient and readers.QueryClient share the
	// identical underlying method signature (both alias dev-health-go/
	// clickhouse's own Binding/RowScanner types), so p.facts.client
	// satisfies readers.QueryClient directly, no adapter needed.
	// "ReadProjectThemeMix" names the reader for attribution -- distinct
	// from "ReadTeamThemeMix" (dev-health-go's own reader for the team
	// subject), never conflated with that reader's own instrumentation.
	// CHAOS-7271: phased reads (investment_project_mix_phased.go); every
	// phase reports through readers.QueryOrgScopedNamed.
	mixRows, readErr := readProjectRollupMixRows(ctx, p.facts.client, orgID, ids, timeBound)
	if readErr != nil {
		return 0, readErr
	}
	serve := func(mix projectRollupMixRow) error {
		rowCount++
		projectKey, featureDelivery, operational, maintenance, quality, risk, bugfixWeighted := mix.ProjectKey, mix.FeatureDelivery, mix.Operational, mix.Maintenance, mix.Quality, mix.Risk, mix.BugfixWeighted
		workUnits, repoCount, teamCount, excludedNoRepoLink := mix.WorkUnits, mix.Repos, mix.Teams, mix.ExcludedNoRepoLink
		// The probe row (maxFactRowsProbe = maxFactRowsPerQuery+1) is
		// counted toward rowCount, so ReadFacts' rowCount>maxFactRowsPerQuery
		// check reports Truncated, but it must not itself be served -- an
		// unbounded LIMIT+1 read still needs the SAME served-output cap
		// every other provider here enforces.
		if rowCount > maxFactRowsPerQuery {
			return nil
		}
		subject, ok := bySubject[projectKey]
		if !ok {
			return nil
		}
		currentTotal := featureDelivery + operational + maintenance + quality + risk
		if currentTotal <= 0 {
			unusableRollup[projectKey] = int64(workUnits)
			return nil
		}
		themeValues := map[string]float64{
			contextfabric.ThemeFeatureDelivery: featureDelivery,
			contextfabric.ThemeOperational:     operational,
			contextfabric.ThemeMaintenance:     maintenance,
			contextfabric.ThemeQuality:         quality,
			contextfabric.ThemeRisk:            risk,
		}
		fields := make(map[string]contextfabric.FactValue, 2*len(canonicalInvestmentThemes)+4)
		for _, theme := range canonicalInvestmentThemes {
			fields[contextfabric.FactFieldTheme(theme)] = contextfabric.NumberFactValue(roundMixEffort(themeValues[theme] / currentTotal))
		}
		fields[contextfabric.FactFieldThemeQualityBugfix] = contextfabric.NumberFactValue(roundMixEffort(bugfixWeighted / currentTotal))
		// rollup_basis names both hops (owning teams, then their owned
		// repos) so a synthesizer never presents this as a project-native
		// attribution computed directly from the project's own work items.
		fields["rollup_basis"] = contextfabric.StringFactValue("team_project_ownership_via_owned_repos_work_unit_investments")
		fields[contextfabric.FactFieldInvestmentMixSource] = contextfabric.StringFactValue(contextfabric.InvestmentMixSourceOwningTeamRollup)
		fields["team_count"] = contextfabric.IntegerFactValue(int64(teamCount))
		fields["repo_count"] = contextfabric.IntegerFactValue(int64(repoCount))
		fields["work_unit_count"] = contextfabric.IntegerFactValue(int64(workUnits))
		// work_units_without_repo_link is the SAME population partition as
		// work_unit_count, not a separate estimate: it counts work units the
		// project's owning teams' own evidence-attribution vote (the SAME
		// mechanism readTeamThemeMix uses for the team subject) assigns to
		// one of those teams, but whose work_unit_investments row carries no
		// repo_id at all -- so this producer's repo-keyed join (this
		// function's own doc comment) never reaches them. counted
		// (work_unit_count) and excluded (work_units_without_repo_link) are
		// disjoint by construction (repo_id IS NOT NULL vs IS NULL on the
		// SAME row) and their sum is this project's disclosed work-unit
		// population for the theme mix -- never silently folded into the
		// counted share. A work unit reachable by neither path (evidence
		// vote assigns it to a DIFFERENT team than any repo-ownership match,
		// a rarer disagreement between the two attribution mechanisms) is
		// outside this roll-up's disclosed partition; not claimed as counted
		// or excluded.
		fields["work_units_without_repo_link"] = contextfabric.IntegerFactValue(int64(excludedNoRepoLink))
		// population_window states, in the fact's own structure, which axis
		// the disclosed population/basis fields above were computed over --
		// this producer supports both current and an explicit requested
		// range (themeInvestmentRangePredicate), unlike the sibling
		// completion roll-up's current-only scope.
		if timeBound.active {
			fields["population_window"] = contextfabric.StringFactValue("requested_range")
		} else {
			fields["population_window"] = contextfabric.StringFactValue("current")
		}

		// Merge onto the project's existing FactInvestment fact when one
		// exists -- see this function's own doc comment for why.
		mergeProjectInvestmentFact(facts, subject, projectKey, fields, nil)
		return nil
	}
	for _, mix := range mixRows {
		if serveErr := serve(mix); serveErr != nil {
			return rowCount, serveErr
		}
	}
	return rowCount, nil
}

// mergeProjectInvestmentFact merges fields onto the project's existing
// FactInvestment fact, or appends a standalone one when none exists. A field
// named in remove is deleted from an existing fact first, so a project keeps
// ONE investment fact rather than a mix fact beside a breakdown fact.
func mergeProjectInvestmentFact(facts *[]contextfabric.CanonicalFact, subject contextfabric.SubjectRef, projectKey string, fields map[string]contextfabric.FactValue, remove []string) {
	targetKey := contextfabric.FactSubjectKey(subject)
	for i := range *facts {
		if (*facts)[i].Kind != contextfabric.FactInvestment || contextfabric.FactSubjectKey((*facts)[i].Subject) != targetKey {
			continue
		}
		for _, field := range remove {
			delete((*facts)[i].Fields, field)
		}
		for field, value := range fields {
			(*facts)[i].Fields[field] = value
		}
		return
	}
	*facts = append(*facts, contextfabric.CanonicalFact{
		Kind: contextfabric.FactInvestment, Subject: subject, Fields: fields,
		EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, projectKey)},
	})
}

// projectNativeMixBasis names the attribution behind a project_native mix:
// the project's own issue evidence, never pull requests and never the owning
// teams' repositories.
const projectNativeMixBasis = "project_work_items_issue_evidence_work_unit_investments"

// readProjectNativeThemeMix attributes FactInvestment's canonical theme
// fields to a project through the project's OWN work items
// (readers.ReadProjectThemeMix), and makes that the project's mix when it has
// any weight. The owning-team roll-up (readProjectThemeMix) stays for a
// project with no native weight; the two are never blended, and the fact says
// which one it carries in investment_mix_source.
//
// A project with native work units but no positive effort keeps the roll-up
// and gains nothing: a zero total is not a mix. When the native mix replaces
// the roll-up, the roll-up's population is not carried as the native
// population: team_count, repo_count and work_units_without_repo_link are
// removed because they describe a different attribution, and the roll-up's
// work_unit_count moves to owning_team_rollup_work_unit_count so both
// populations stay visible.
//
// A work unit that touches several projects counts in full for each;
// spanning_unit_count discloses how many of the project's units do. A work
// item id that project membership places under more than one repository
// counts for each project it is placed in, like a spanning unit;
// native_multi_placed_unit_count discloses how many of the project's units
// were counted through such an id (a data defect shows as a non-zero count),
// on the mix fact when there is a native mix and on the project's fact (the
// roll-up's, or a fact carrying only that count) when there is not.
func (p *InvestmentProvider) readProjectNativeThemeMix(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound, unusableRollup map[string]int64) (rowCount int, err error) {
	ids, bySubject, _ := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, nil
	}
	rows, err := readProjectNativeThemeMixRows(ctx, p.facts.client, orgID, ids, timeBound, maxFactRowsProbe)
	if err != nil {
		return 0, err
	}
	rowCount = len(rows)
	for i, row := range rows {
		// The probe row is evidence of truncation, never served.
		if i >= maxFactRowsPerQuery {
			break
		}
		subject, ok := bySubject[row.ProjectSubjectKey]
		if !ok {
			continue
		}
		currentTotal := row.FeatureDelivery + row.Operational + row.Maintenance + row.Quality + row.Risk
		if row.EffortUnits == 0 || currentTotal <= 0 {
			// No native mix. A project whose only counted units carry no
			// effort still discloses its multi-placed units.
			if row.MultiPlacedUnits > 0 {
				mergeProjectInvestmentFact(facts, subject, row.ProjectSubjectKey, map[string]contextfabric.FactValue{
					"native_multi_placed_unit_count": contextfabric.IntegerFactValue(int64(row.MultiPlacedUnits)),
				}, nil)
			}
			continue
		}
		themeValues := map[string]float64{
			contextfabric.ThemeFeatureDelivery: row.FeatureDelivery,
			contextfabric.ThemeOperational:     row.Operational,
			contextfabric.ThemeMaintenance:     row.Maintenance,
			contextfabric.ThemeQuality:         row.Quality,
			contextfabric.ThemeRisk:            row.Risk,
		}
		fields := make(map[string]contextfabric.FactValue, 2*len(canonicalInvestmentThemes)+8)
		for _, theme := range canonicalInvestmentThemes {
			fields[contextfabric.FactFieldTheme(theme)] = contextfabric.NumberFactValue(roundMixEffort(themeValues[theme] / currentTotal))
		}
		fields[contextfabric.FactFieldThemeQualityBugfix] = contextfabric.NumberFactValue(roundMixEffort(row.BugfixWeighted / currentTotal))
		fields[contextfabric.FactFieldInvestmentMixSource] = contextfabric.StringFactValue(contextfabric.InvestmentMixSourceProjectNative)
		fields["rollup_basis"] = contextfabric.StringFactValue(projectNativeMixBasis)
		fields["work_unit_count"] = contextfabric.IntegerFactValue(int64(row.WorkUnits))
		fields["effort_unit_count"] = contextfabric.IntegerFactValue(int64(row.EffortUnits))
		fields["spanning_unit_count"] = contextfabric.IntegerFactValue(int64(row.SpanningUnits))
		fields["native_multi_placed_unit_count"] = contextfabric.IntegerFactValue(int64(row.MultiPlacedUnits))
		if timeBound.active {
			fields["population_window"] = contextfabric.StringFactValue("requested_range")
		} else {
			fields["population_window"] = contextfabric.StringFactValue("current")
		}
		// The roll-up's unit population moves aside rather than being
		// overwritten by a count of a different population. A roll-up with no
		// positive effort served no fact, so its population comes from the
		// side channel instead.
		if count, unusable := unusableRollup[row.ProjectSubjectKey]; unusable {
			fields["owning_team_rollup_work_unit_count"] = contextfabric.IntegerFactValue(count)
		}
		targetKey := contextfabric.FactSubjectKey(subject)
		for j := range *facts {
			existing := &(*facts)[j]
			if existing.Kind != contextfabric.FactInvestment || contextfabric.FactSubjectKey(existing.Subject) != targetKey {
				continue
			}
			// Only the roll-up writes work_unit_count onto the project's fact.
			if count, hasCount := existing.Fields["work_unit_count"]; hasCount {
				fields["owning_team_rollup_work_unit_count"] = count
			}
			break
		}
		mergeProjectInvestmentFact(facts, subject, row.ProjectSubjectKey, fields, []string{"team_count", "repo_count", "work_units_without_repo_link"})
	}
	return rowCount, nil
}
