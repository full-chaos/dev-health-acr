package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
	capability := newCapability(contextfabric.FactInvestment, "devhealthfacts.investment", []contextfabric.SubjectKind{
		contextfabric.SubjectTeam, contextfabric.SubjectProject,
	})
	capability.Tables = map[contextfabric.SubjectKind][]contextfabric.FactTableShape{
		contextfabric.SubjectProject: {contextfabric.FactTableBreakdown},
	}
	capability.EstimatedItems = 20
	return capability
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
		// CHAOS-4398 §0: the CANONICAL theme/subcategory read, a
		// deliberately SEPARATE call from readTeamInvestment above -- see
		// readTeamThemeMix's own doc comment for why this is a new
		// producer join, not a reuse of the legacy investment_metrics_daily
		// path.
		if scanErr := p.readTeamThemeMix(ctx, orgID, teamSubjects, &facts, timeBound); scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team theme mix", scanErr)
		}
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
	// CHAOS-4521b: this source has no project dimension, so an all-project
	// read that came back empty says something more specific than "no rows".
	retentionReason = explainTeamScopedProjectAbsence(timeBound, state, retentionReason, query.Subjects)
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
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, r.TeamID)},
		})
	}
	return len(rows), omittedUnrepresentableCount, nil
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
// distribution (CHAOS-4398 §0: work_unit_investments via
// readers.ReadTeamThemeMix, the CHAOS-2600 ownership-precedence majority
// vote bridge) for the given team subjects -- NEVER investment_metrics_daily,
// the deprecated legacy rule set readTeamInvestment above reads.
//
// The canonical theme_*/prior_theme_*/theme_quality_bugfix fields are
// MERGED onto an existing FactInvestment fact for the team when one already
// exists (readTeamInvestment's own legacy per-(area,stream) facts,
// appended BEFORE this call in ReadFacts), rather than always creating a
// separate fact (codex round-2 finding, fixed from an earlier "always
// separate" draft of this function): CHAOS-4355 sends every canonical
// fact's fields to the model unfiltered, and synthesis's own evidence-
// closure check (model_runtime.go's lookupCanonicalFact) resolves a claim
// to the FIRST fact matching (Kind, Subject) in the fact list -- a
// standalone canonical fact appended AFTER the legacy ones would be
// shadowed by them whenever a claim cites a theme_* field, rejecting an
// otherwise-valid claim. Merging guarantees whichever FactInvestment fact
// lookupCanonicalFact finds first for this team already carries the
// canonical fields (field-key-safe: no overlap between the legacy
// area/stream columns and this producer's columns). A team with NO legacy
// facts still gets a standalone fact, as before.
// internal/contextfabric/cohort_ranking.go's investmentMixSignal finds
// whichever fact carries the canonical fields by field PRESENCE
// (theme_feature_delivery), never by position -- unaffected by which
// physical fact object the fields ended up merged into.
//
// timeBound.neutral() bounds the CURRENT window read. When timeBound also
// carries an explicit start (never inferred -- CHAOS-4040: a window this
// producer invented on its own would be exactly the "commit under an
// inferred window" the ticket forbids), a SECOND, explicit query reads the
// prior comparable window [start-duration, start) for RankCohort's
// mix-shift sub-signal. A team with no prior-window data gets NO
// prior_theme_* fields at all -- omitted, never zero-filled -- and
// RankCohort's mix-shift sub-signal degrades gracefully (it simply never
// fires for that team), matching every other missing-signal case in this
// package.
//
// A team with zero current-window weighted effort (the reader returned no
// rows, or all its rows summed to a non-positive total -- effort_value is
// never negative in practice, but this guards the divide regardless) gets
// NO theme fields either: a fabricated 0.0 share across all five themes
// would read as "we know this team's mix is exactly nothing" rather than
// "we have no mix to report", which is the same degrade-not-fabricate
// distinction CHAOS-3781 already draws for every other signal here.
//
// Attribution key (codex round-1 AND round-2 review, source-verified):
// readers.ReadTeamThemeMix joins work_item_team_attributions on
// work_item_id ALONE, without repo_id -- a DELIBERATE match to ops's own
// reference (PRIMARY_WORK_ITEM_TEAM_ATTRIBUTION_SOURCE,
// api/queries/investment.py), which this producer exists to port
// faithfully, not to redesign unilaterally. Per-provider safety differs:
// github ("ghpr:{owner}/{repo}#{n}") and gitlab ("{group}/{project}!{n}")
// work_item_ids embed their repo (external_ingest/ids.py), so no two
// DIFFERENT repos can legitimately produce the same string. jira/linear
// do NOT ("jira:{external_key}", "linear:{external_key}" --
// external_ingest/ids.py:72-75, confirmed against source, per codex
// round-2): two DISTINCT issues in the same org sharing an external key
// (e.g. two connected Jira sites, or a workspace migration) could
// theoretically collide within one org_id (work_item_team_attributions'
// own WHERE already scopes by org_id, so no CROSS-org collision is
// possible either way). This is a PRE-EXISTING characteristic of the
// Python reference this Go port matches exactly -- not a defect this PR
// introduces or worsens -- and fixing it well requires giving
// work_item_team_attributions' own attribution key provider-instance
// awareness across BOTH the Python reference and this port together, a
// team-attribution-family change bigger than this producer. Follow-up:
// CHAOS-4404.
func (p *InvestmentProvider) readTeamThemeMix(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) error {
	ids, bySubject := subjectIndex(subjects, teamPrefix)
	if len(ids) == 0 {
		return nil
	}
	current, err := readers.ReadTeamThemeMix(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return err
	}
	var prior []readers.TeamThemeMixRow
	if timeBound.active && timeBound.hasStart {
		duration := timeBound.end.Sub(timeBound.start)
		priorBound := factTimeBound{active: true, hasStart: true, start: timeBound.start.Add(-duration), end: timeBound.start}
		prior, err = readers.ReadTeamThemeMix(ctx, p.facts.client, orgID, ids, priorBound.neutral())
		if err != nil {
			return err
		}
	}

	type teamMix struct {
		teamName      string
		currentTheme  map[string]float64
		currentBugfix float64
		priorTheme    map[string]float64
	}
	byTeam := make(map[string]*teamMix, len(ids))
	entry := func(teamID, teamName string) *teamMix {
		m, ok := byTeam[teamID]
		if !ok {
			m = &teamMix{currentTheme: map[string]float64{}, priorTheme: map[string]float64{}}
			byTeam[teamID] = m
		}
		if m.teamName == "" {
			m.teamName = teamName
		}
		return m
	}
	for _, row := range current {
		m := entry(row.TeamID, row.TeamName)
		switch row.Kind {
		case "theme":
			m.currentTheme[row.Key] = row.WeightedEffort
		case "subcategory":
			if row.Key == readers.BugfixSubcategoryKey {
				m.currentBugfix = row.WeightedEffort
			}
		}
	}
	for _, row := range prior {
		if row.Kind != "theme" {
			continue
		}
		entry(row.TeamID, row.TeamName).priorTheme[row.Key] = row.WeightedEffort
	}

	for teamID, m := range byTeam {
		subject, ok := bySubject[teamID]
		if !ok {
			continue
		}
		currentTotal := 0.0
		for _, theme := range canonicalInvestmentThemes {
			currentTotal += m.currentTheme[theme]
		}
		if currentTotal <= 0 {
			continue
		}
		fields := make(map[string]contextfabric.FactValue, 2*len(canonicalInvestmentThemes)+1)
		for _, theme := range canonicalInvestmentThemes {
			fields[contextfabric.FactFieldTheme(theme)] = contextfabric.NumberFactValue(m.currentTheme[theme] / currentTotal)
		}
		fields[contextfabric.FactFieldThemeQualityBugfix] = contextfabric.NumberFactValue(m.currentBugfix / currentTotal)

		priorTotal := 0.0
		for _, theme := range canonicalInvestmentThemes {
			priorTotal += m.priorTheme[theme]
		}
		if priorTotal > 0 {
			for _, theme := range canonicalInvestmentThemes {
				fields[contextfabric.FactFieldPriorTheme(theme)] = contextfabric.NumberFactValue(m.priorTheme[theme] / priorTotal)
			}
		}

		// Merge onto an EXISTING FactInvestment fact for this team when one
		// exists, rather than always appending a new one (codex round-2
		// finding): synthesis's own evidence-closure check
		// (model_runtime.go's lookupCanonicalFact) resolves a claim to the
		// FIRST fact matching (Kind, Subject) in the investigation's fact
		// list -- and CHAOS-4355 already sends every canonical fact's
		// fields to the model unfiltered, so a model claim citing
		// theme_feature_delivery for a team that ALSO has
		// readTeamInvestment's legacy per-(area,stream) facts (appended
		// BEFORE this call, in ReadFacts) would resolve against whichever
		// legacy fact happens to be first, find no such field, and be
		// rejected -- a live-reachable synthesis failure, not a PR2/PR3-only
		// risk. Merging these fields into the FIRST existing FactInvestment
		// fact for this team (field-key-safe: no overlap between the
		// legacy area/stream columns and this producer's theme_*/
		// prior_theme_* columns) guarantees whichever fact lookupCanonicalFact
		// finds first already carries them. A team with NO legacy facts
		// still gets a standalone one, as before.
		merged := false
		targetKey := contextfabric.FactSubjectKey(subject)
		for i := range *facts {
			if (*facts)[i].Kind != contextfabric.FactInvestment || contextfabric.FactSubjectKey((*facts)[i].Subject) != targetKey {
				continue
			}
			for field, value := range fields {
				(*facts)[i].Fields[field] = value
			}
			merged = true
			break
		}
		if !merged {
			*facts = append(*facts, contextfabric.CanonicalFact{
				Kind: contextfabric.FactInvestment, Subject: subject, Fields: fields,
				EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, teamID)},
			})
		}
	}
	return nil
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
				"cycle_p50_hours":      contextfabric.NumberFactValue(r.CycleP50Hours),
				"investment_area":      stringOrNull(r.InvestmentArea),
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
						"churn_loc", "cycle_p50_hours",
					},
					Grain: timeBound.effectiveGrain(grainDaily),
					Rows:  teamRows,
				}),
			},
			EvidenceRefIDs: evidenceRefIDs,
		})
	}
	return rowCount, omittedUnrepresentableCount, breakdownTruncated, nil
}
