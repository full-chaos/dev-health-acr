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

func (p *InvestmentProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
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

	if teamSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectTeam); len(teamSubjects) > 0 {
		rowCount, omitted, rejected, scanErr := p.readTeamInvestment(ctx, orgID, teamSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team investment", scanErr)
		}
		omittedUnrepresentableCount += omitted
		rejectedCount += rejected
		truncated = truncated || rowCount >= maxFactRowsPerQuery
		// CHAOS-4398 §0: the CANONICAL theme/subcategory read, a
		// deliberately SEPARATE call from readTeamInvestment above -- see
		// readTeamThemeMix's own doc comment for why this is a new
		// producer join, not a reuse of the legacy investment_metrics_daily
		// path.
		//
		// It shares readTeamInvestment's own teamSubjects, so it would
		// double-count the SAME rejected subjects if it also reported them;
		// only readTeamInvestment's count is folded in above (CHAOS-5026).
		if scanErr := p.readTeamThemeMix(ctx, orgID, teamSubjects, &facts, timeBound); scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team theme mix", scanErr)
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
		themeRowCount, themeScanErr := p.readProjectThemeMix(ctx, orgID, projectSubjects, &facts, timeBound)
		if themeScanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project theme mix", themeScanErr)
		}
		truncated = truncated || themeRowCount > maxFactRowsPerQuery
		// The project's OWN attribution replaces the roll-up's shares for a
		// project that has any, and leaves the roll-up in place for one that
		// has none. It reads after the roll-up so it can merge onto the same
		// fact and move the roll-up's population beside it.
		nativeRowCount, nativeScanErr := p.readProjectNativeThemeMix(ctx, orgID, projectSubjects, &facts, timeBound)
		if nativeScanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project native theme mix", nativeScanErr)
		}
		truncated = truncated || nativeRowCount > maxFactRowsPerQuery
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	// CHAOS-4521b: this source has no project dimension, so an all-project
	// read that came back empty says something more specific than "no rows".
	retentionReason = explainTeamScopedProjectAbsence(timeBound, state, retentionReason, query.Subjects)
	if omittedUnrepresentableCount > 0 && retentionReason == "" {
		retentionReason = unrepresentableValueReason
	}
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: truncated || omittedUnrepresentableCount > 0, OmittedCount: omittedUnrepresentableCount}
	return result, nil
}

// readTeamInvestment is CHAOS-3780's original investment_metrics_daily read.
// The query itself (row_number() tiebreak over day/computed_at/cityHash64
// for the F4 intraday-rerun shape) now lives in
// readers.ReadTeamInvestment -- see that function's doc comment for the
// full tiebreak reasoning. This adapter keeps the CanonicalFact-building
// half, factored out so ReadFacts can branch by subject kind the same way
// metrics.go/health.go already do.
func (p *InvestmentProvider) readTeamInvestment(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount, omittedUnrepresentableCount, rejected int, err error) {
	ids, bySubject, rejected := subjectIndex(subjects, teamPrefix)
	rows, err := readers.ReadTeamInvestment(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, 0, rejected, err
	}
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
	return len(rows), omittedUnrepresentableCount, rejected, nil
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
	// rejected is intentionally discarded here: subjects is the SAME
	// teamSubjects slice ReadFacts already passed to readTeamInvestment,
	// whose own subjectIndex call already counted and reported every
	// shape-rejected id (CHAOS-5026) -- counting it again here would
	// double it.
	ids, bySubject, _ := subjectIndex(subjects, teamPrefix)
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
// [from_ts, to_ts) validity window overlaps the requested [start, end) --
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
// Fields MERGE onto an existing FactInvestment fact for the project
// (readProjectInvestment's own legacy team_breakdown fact, appended BEFORE
// this call in ReadFacts) when one exists, for the SAME reason
// readTeamThemeMix merges onto readTeamInvestment's fact: every canonical
// fact's fields go to the model unfiltered, and synthesis's
// lookupCanonicalFact resolves a claim to the FIRST fact matching (Kind,
// Subject) in the fact list -- a standalone fact appended after the legacy
// one would be shadowed whenever a claim cites a theme_* field. A project
// with no legacy breakdown fact still gets a standalone one.
//
// The statement returns at most ONE row per requested project (every
// aggregate collapses to project_key), probed at maxFactRowsProbe
// (withRowProbeLimit) rather than a plain LIMIT, so a project population
// sitting exactly at the served cap is distinguishable from one that
// overflowed it -- the same discipline workItemProjectCompletionStatement
// documents for its own project-grain aggregate.
func (p *InvestmentProvider) readProjectThemeMix(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount int, err error) {
	ids, bySubject, _ := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, nil
	}
	ownershipPredicate := ownershipValidityPredicate(timeBound)
	rangePredicate := themeInvestmentRangePredicate(timeBound, "from_ts", "to_ts")
	// The whole CTE chain is wrapped in an outer `SELECT * FROM (WITH ...
	// SELECT ...)` shell -- readTeamThemeMix's own statement uses the
	// identical shape -- because the query CLIENT'S read-only validator
	// (dev-health-go clickhouse.validateReadOnlyStatement) requires the
	// statement's OWN first token to be literally SELECT; a bare `WITH ...`
	// statement (valid ClickHouse, invalid by this guard) is refused as
	// ErrUnsafeStatement before it ever reaches the server.
	statement := withRowProbeLimit(`SELECT * FROM (
WITH latest AS (
	SELECT work_unit_id,
		argMax(repo_id, computed_at) AS repo_id,
		argMax(from_ts, computed_at) AS from_ts,
		argMax(to_ts, computed_at) AS to_ts,
		argMax(effort_value, computed_at) AS effort_value,
		argMax(theme_distribution_json, computed_at) AS theme_distribution_json,
		argMax(subcategory_distribution_json, computed_at) AS subcategory_distribution_json,
		argMax(structural_evidence_json, computed_at) AS structural_evidence_json
	FROM work_unit_investments
	WHERE org_id = {org_id:String}
	GROUP BY work_unit_id
),
windowed AS (
	SELECT * FROM latest WHERE 1` + rangePredicate + `
),
repo_linked AS (
	SELECT * FROM windowed WHERE repo_id IS NOT NULL
),
project_team AS (
	SELECT DISTINCT provider AS project_provider, id AS project_id, team_id
	FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
),
project_repo_team AS (
	SELECT DISTINCT pt.project_provider AS project_provider, pt.project_id AS project_id, tro.repo_id AS repo_id, pt.team_id AS team_id
	FROM project_team AS pt
	INNER JOIN team_repo_ownership AS tro FINAL ON tro.team_id = pt.team_id
	WHERE tro.org_id = {org_id:String} AND tro.repo_id IS NOT NULL` + ownershipPredicate + `
),
project_repo AS (
	SELECT DISTINCT project_provider, project_id, repo_id FROM project_repo_team
),
attributed AS (
	SELECT pr.project_provider AS project_provider, pr.project_id AS project_id, w.work_unit_id AS work_unit_id, w.repo_id AS repo_id, w.effort_value AS effort_value, w.theme_distribution_json AS theme_distribution_json, w.subcategory_distribution_json AS subcategory_distribution_json
	FROM repo_linked AS w
	INNER JOIN project_repo AS pr ON pr.repo_id = w.repo_id
),
per_theme AS (
	SELECT concat(project_provider, ':', project_id) AS project_key, theme_kv.1 AS theme, sum(theme_kv.2 * effort_value) AS weighted_effort
	FROM attributed
	ARRAY JOIN CAST(theme_distribution_json AS Array(Tuple(String, Float64))) AS theme_kv
	GROUP BY project_key, theme
),
per_project AS (
	SELECT concat(project_provider, ':', project_id) AS project_key,
		sum(ifNull(subcategory_distribution_json['` + readers.BugfixSubcategoryKey + `'], 0.0) * effort_value) AS bugfix_weighted,
		uniqExact(work_unit_id) AS work_units,
		uniqExact(repo_id) AS repos
	FROM attributed
	GROUP BY project_provider, project_id
),
contributing_repo AS (
	SELECT DISTINCT project_provider, project_id, repo_id FROM attributed
),
team_coverage AS (
	SELECT concat(prt.project_provider, ':', prt.project_id) AS project_key, uniqExact(prt.team_id) AS team_count
	FROM project_repo_team AS prt
	INNER JOIN contributing_repo AS cr ON cr.project_provider = prt.project_provider AND cr.project_id = prt.project_id AND cr.repo_id = prt.repo_id
	GROUP BY prt.project_provider, prt.project_id
),
repo_lookup AS (
	SELECT toString(id) AS repo_uuid,
		argMax(repo, last_synced) AS repo,
		if(uniqExact(provider) = 1, argMax(provider, last_synced), '') AS provider
	FROM repos
	WHERE org_id = {org_id:String}
	GROUP BY id
),
wita AS (
	SELECT work_item_id, team_id
	FROM work_item_team_attributions FINAL
	WHERE org_id = {org_id:String} AND is_primary = 1
	  AND (work_item_id, computed_at) IN (
		  SELECT work_item_id, max(computed_at)
		  FROM work_item_team_attributions
		  WHERE org_id = {org_id:String}
		  GROUP BY work_item_id
	  )
),
evidence_resolved AS (
	SELECT windowed.work_unit_id AS work_unit_id,
		multiIf(
			NOT match(evidence_ref, '^[0-9a-fA-F-]{36}#pr[0-9]+$'), evidence_ref,
			evidence_repo.repo = '' OR evidence_repo.provider = '', '',
			concat(if(evidence_repo.provider = 'gitlab', 'gitlab:', 'ghpr:'), evidence_repo.repo,
				if(evidence_repo.provider = 'gitlab', '!', '#'), splitByString('#pr', evidence_ref)[2])
		) AS resolved_wi_id
	FROM windowed
	ARRAY JOIN arrayDistinct(arrayConcat(
		JSONExtract(structural_evidence_json, 'issues', 'Array(String)'),
		JSONExtract(structural_evidence_json, 'prs', 'Array(String)')
	)) AS evidence_ref
	LEFT JOIN repo_lookup AS evidence_repo ON evidence_repo.repo_uuid = splitByString('#pr', evidence_ref)[1]
),
votes AS (
	SELECT work_unit_id, argMax(vote_team_id, (cnt, vote_team_id)) AS team_id
	FROM (
		SELECT evidence_resolved.work_unit_id AS work_unit_id,
			ifNull(nullIf(t.team_id, ''), '') AS vote_team_id,
			uniqExactIf(evidence_resolved.resolved_wi_id, ` + evidenceVoteAttributedPredicate + `) AS cnt
		FROM evidence_resolved
		LEFT JOIN wita AS t ON t.work_item_id = evidence_resolved.resolved_wi_id
		GROUP BY work_unit_id, vote_team_id
	)
	GROUP BY work_unit_id
),
project_evidence_attributed AS (
	SELECT DISTINCT pt.project_provider AS project_provider, pt.project_id AS project_id, votes.work_unit_id AS work_unit_id
	FROM votes
	INNER JOIN project_team AS pt ON pt.team_id = votes.team_id
	WHERE votes.team_id != ''
),
excluded_no_repo_link AS (
	SELECT concat(project_provider, ':', project_id) AS project_key, uniqExact(work_unit_id) AS excluded_count
	FROM (
		SELECT pea.project_provider AS project_provider, pea.project_id AS project_id, pea.work_unit_id AS work_unit_id
		FROM project_evidence_attributed AS pea
		INNER JOIN windowed AS w ON w.work_unit_id = pea.work_unit_id
		WHERE w.repo_id IS NULL
	)
	GROUP BY project_provider, project_id
)
SELECT
	pp.project_key AS project_key,
	sumIf(pt.weighted_effort, pt.theme = '` + contextfabric.ThemeFeatureDelivery + `') AS feature_delivery,
	sumIf(pt.weighted_effort, pt.theme = '` + contextfabric.ThemeOperational + `') AS operational,
	sumIf(pt.weighted_effort, pt.theme = '` + contextfabric.ThemeMaintenance + `') AS maintenance,
	sumIf(pt.weighted_effort, pt.theme = '` + contextfabric.ThemeQuality + `') AS quality,
	sumIf(pt.weighted_effort, pt.theme = '` + contextfabric.ThemeRisk + `') AS risk,
	any(pp.bugfix_weighted) AS bugfix_weighted,
	any(pp.work_units) AS work_units,
	any(pp.repos) AS repos,
	any(ifNull(tc.team_count, 0)) AS team_count,
	any(ifNull(excl.excluded_count, 0)) AS excluded_no_repo_link
FROM per_project AS pp
LEFT JOIN per_theme AS pt ON pt.project_key = pp.project_key
LEFT JOIN team_coverage AS tc ON tc.project_key = pp.project_key
LEFT JOIN excluded_no_repo_link AS excl ON excl.project_key = pp.project_key
GROUP BY pp.project_key
)
ORDER BY project_key`)

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
	extraBindings := make([]readers.Binding, 0, 2)
	for _, b := range timeBound.bindings() {
		extraBindings = append(extraBindings, readers.Binding{Name: b.Name, Value: b.Value})
	}
	scanErr := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadProjectThemeMix", statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var projectKey string
		var featureDelivery, operational, maintenance, quality, risk, bugfixWeighted float64
		var workUnits, repoCount, teamCount, excludedNoRepoLink uint64
		if scanErr := row.Scan(&projectKey, &featureDelivery, &operational, &maintenance, &quality, &risk, &bugfixWeighted, &workUnits, &repoCount, &teamCount, &excludedNoRepoLink); scanErr != nil {
			return scanErr
		}
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
			fields[contextfabric.FactFieldTheme(theme)] = contextfabric.NumberFactValue(themeValues[theme] / currentTotal)
		}
		fields[contextfabric.FactFieldThemeQualityBugfix] = contextfabric.NumberFactValue(bugfixWeighted / currentTotal)
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

		// Merge onto an existing FactInvestment fact for this project when
		// one exists (readProjectInvestment's own legacy breakdown fact,
		// appended before this call in ReadFacts) -- see this function's
		// own doc comment for why (lookupCanonicalFact first-match
		// shadowing, the same reason readTeamThemeMix merges for the team
		// subject).
		mergeProjectInvestmentFact(facts, subject, projectKey, fields, nil)
		return nil
	}, extraBindings...)
	if scanErr != nil {
		return rowCount, scanErr
	}
	return rowCount, nil
}

// mergeProjectInvestmentFact merges fields onto the project's existing
// FactInvestment fact, or appends a standalone one when none exists. A field
// named in remove is deleted from an existing fact first. Merging rather than
// appending is what keeps synthesis's first-match lookup from resolving a
// theme claim to a fact without the theme fields.
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
// spanning_unit_count discloses how many of the project's units do.
func (p *InvestmentProvider) readProjectNativeThemeMix(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount int, err error) {
	ids, bySubject, _ := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, nil
	}
	rows, err := readers.ReadProjectThemeMixWithRowLimit(ctx, p.facts.client, orgID, ids, timeBound.neutral(), maxFactRowsProbe)
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
			fields[contextfabric.FactFieldTheme(theme)] = contextfabric.NumberFactValue(themeValues[theme] / currentTotal)
		}
		fields[contextfabric.FactFieldThemeQualityBugfix] = contextfabric.NumberFactValue(row.BugfixWeighted / currentTotal)
		fields[contextfabric.FactFieldInvestmentMixSource] = contextfabric.StringFactValue(contextfabric.InvestmentMixSourceProjectNative)
		fields["rollup_basis"] = contextfabric.StringFactValue(projectNativeMixBasis)
		fields["work_unit_count"] = contextfabric.IntegerFactValue(int64(row.WorkUnits))
		fields["effort_unit_count"] = contextfabric.IntegerFactValue(int64(row.EffortUnits))
		fields["spanning_unit_count"] = contextfabric.IntegerFactValue(int64(row.SpanningUnits))
		if timeBound.active {
			fields["population_window"] = contextfabric.StringFactValue("requested_range")
		} else {
			fields["population_window"] = contextfabric.StringFactValue("current")
		}
		// The roll-up's unit population moves aside rather than being
		// overwritten by a count of a different population.
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
