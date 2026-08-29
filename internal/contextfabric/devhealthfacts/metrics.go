package devhealthfacts

import (
	"context"
	"strconv"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

const repositoryPrefix = "repository:"

// metricsSeriesDefaultWindow (CHAOS-4418, team-lead ruling) bounds the
// repository per-day metrics series on the CURRENT axis when the caller's
// own request.Question.TimeContext.EvidenceWindow carries no EXPLICIT
// Start/End -- unlike a bounded historical query (factTimeBound.active),
// the current axis otherwise has no bound at all, and an unbounded "every
// day this repo has ever had a repo_metrics_daily row" read would pull the
// entire retained history per repository.
//
// This is NOT a new, independently-chosen number: it is the SAME width
// window.go's own windowDefaultPolicy (= WindowDefaultPolicy90D) already
// ships as this platform's one default evidence-window policy, and
// relativeWindowDurations[RelativeWindowTrailing90D] (window.go) is 90
// days too -- both unexported to package contextfabric, so this constant
// copies the WIDTH, never the derivation (relativeWindowBounds is
// documented there as "the ONLY function in this codebase that may
// [derive absolute bounds from a RelativeWindowID]" -- devhealthfacts
// must not duplicate that logic, only match its width when the caller
// gave no explicit bound at all). A caller with an ACTIVE historical bound
// (an explicit as-of/range question) or an EXPLICIT current-axis
// EvidenceWindow.Start/End uses that instead, unbounded by this constant.
const metricsSeriesDefaultWindow = 90 * 24 * time.Hour

// MetricsSeriesPerRepositoryRowCap bounds how many day-rows ANY SINGLE
// requested repository can return, via ClickHouse's `LIMIT n BY repo_id`
// (a per-group cap, evaluated before any overall LIMIT).
//
// Codex R1 (confirmed), Codex R2 (confirmed the R1 fix was still
// incomplete): before this ticket, readRepositoryMetrics returned exactly
// one row per repository regardless of how many were requested together,
// so up to maxFactRowsPerQuery (200) repositories could share that
// budget safely via the package's ordinary withRowLimit convention. This
// rescue can now return up to ~90 rows for ONE repository (the default
// window), so simply keeping withRowLimit's shared, query-WIDE LIMIT
// alongside a per-repository LIMIT BY does NOT fix cross-repository
// starvation (R1's own attempt): the per-group cap only bounds each
// repository's OWN contribution, but the trailing overall LIMIT still
// truncates the COMBINED (repo_id, day DESC)-ordered stream afterward,
// so a handful of repositories at or near the per-group cap can still
// exhaust the shared budget and leave later-sorted repositories with NO
// canonical fact at all. The fix is to drop the shared, query-wide
// LIMIT for this query ENTIRELY (this SQL is deliberately NOT built with
// withRowLimit, unlike every other statement in this package) and rely
// solely on this per-repository cap -- every requested repository then
// gets its own fair share, unconditionally, regardless of how many
// repositories are requested together. The worst-case total row count
// is len(ids) * this cap, which is acceptable: the number of repository
// subjects in one investigation is already bounded well below that by
// resolution's own candidate limits, long before this read ever runs.
// 200 mirrors maxFactRowsPerQuery's own historical per-subject headroom
// (this package's own convention, re-scoped from "per query" to "per
// subject" here) -- comfortably above metricsSeriesDefaultWindow's 90
// days, so the ordinary current-axis case never truncates a single
// repository's own series at all; capFactValueRows' own 64-row per-fact
// cap (with its own truncated flag) is what actually bounds and reports
// on the final Rows table for a caller-supplied window wider than that.
const MetricsSeriesPerRepositoryRowCap = 200

// MetricsProvider implements contextfabric.FactProvider for FactMetrics.
//
// CHAOS-3780 shipped repository-only, reading repo_metrics_daily -- the
// same nightly, precomputed-by-Ops delivery-metrics rollup devhealthsource
// would read if it needed repository metrics. This package never
// recomputes a metric: every column read here is already a finished,
// precomputed value written by Dev Health Ops.
//
// CHAOS-4347 widens FactMetrics to [repository, team, project] by REAL
// table joins, not by proxying repository facts as if they belonged to a
// team or project:
//
//   - team reads team_metrics_daily directly (a genuinely team-scoped
//     rollup -- commits, after-hours/weekend commit ratios).
//   - project has no metrics table of its own. It rolls up through
//     team_project_ownership -> team_metrics_daily: every team currently
//     (or, for a bounded historical query, AS OF the requested instant)
//     owning the project contributes its own latest team_metrics_daily
//     row. Additive counts (commits_count, after_hours_commits_count,
//     weekend_commits_count) are SUMMED across owning teams -- sound,
//     because a count is additive regardless of population size. Ratios
//     (after_hours_commit_ratio, weekend_commit_ratio) are NEVER averaged
//     across teams of different sizes -- that silently misrepresents the
//     population -- so each team's own ratio rides in a per-team Rows
//     breakdown (CHAOS-4347's renderable-table FactValue) instead.
//
// This intentionally does NOT go through FactReadScopeResolver
// (internal/contextfabric/fact_scope.go, CHAOS-4099): that mechanism
// exists for a capability with NO project/team-native source, deriving a
// READ permission onto a repository/PR/review it does not otherwise
// support (the activity-proxy semantic, always disclosed as weaker than it
// looks). Team and project metrics here are not proxies for repository
// metrics -- team_metrics_daily is genuinely about the team, and the
// project rollup is a genuine (if disclosed, rollup_basis-labelled)
// aggregation over the teams that actually own it. Widening
// SupportedSubjectKinds for a capability with its own real source is
// exactly what CHAOS-4099's design note calls "Option A" for a DIFFERENT
// situation (a capability proxying another kind's data with no central
// policy) -- see chaos4099_capability_kinds_test.go's updated pin for the
// citation. TestChaos4099_NoCanonicalFactCapabilityAnswersForAProject is
// updated accordingly: FactMetrics is the one capability that answers for
// a project directly, by a real join, and that is deliberate.
//
// repo_metrics_daily and team_metrics_daily are both plain, append-only
// MergeTree tables: live data shows up to 85-86 rows sharing one
// (repo_id|team_id, day) key (intraday reruns), and those reruns carry
// genuinely different values, not no-op repeats (Codex finding F2,
// confirmed against real ClickHouse data for repo_metrics_daily;
// compounding_risk_daily's health.go documents the identical shape).
// row_number() OVER (PARTITION BY <id> ORDER BY day DESC, computed_at
// DESC), picking rn=1 and scanning every field off that ONE row, is
// required here -- GROUP BY + independent per-field argMax(field, day)
// calls have no guarantee of breaking a day tie the same way across
// fields, so on a day with several reruns they can stitch a fact together
// from different rows, fabricating a combination that was never actually
// true at any single point in time.
//
// day DESC, computed_at DESC is still not a TOTAL order: neither table has
// a per-row unique id column, so two rows can share the exact same
// computed_at too (Codex round-2 finding M1 -- the same tie
// compounding_risk_daily's health.go documents). Without a final
// tiebreaker, row_number() can pick a different one of those tied rows on
// different executions of the SAME query, which is itself a correctness
// defect (a fact that flaps between two truths with no data change).
// cityHash64 of the row's own value columns is the last ORDER BY term: it
// is arbitrary (there is no "more correct" row among an exact tie) but
// STABLE -- the same tied inputs always hash to the same value, so the
// same row wins every time.
type MetricsProvider struct{ facts clickhouseFacts }

func newMetricsProvider(client contextpacket.ClickHouseQueryClient) *MetricsProvider {
	return &MetricsProvider{facts: clickhouseFacts{client: client}}
}

func (p *MetricsProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactMetrics, "devhealthfacts.metrics", []contextfabric.SubjectKind{
		contextfabric.SubjectRepository, contextfabric.SubjectTeam, contextfabric.SubjectProject,
	})
}

func (p *MetricsProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
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

	if repoSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectRepository); len(repoSubjects) > 0 {
		// rowCount deliberately NOT compared against maxFactRowsPerQuery
		// here (unlike every other branch in this file): readRepositoryMetrics'
		// own statement has no query-wide LIMIT (MetricsSeriesPerRepositoryRowCap's
		// own doc comment explains why), so a large rowCount across many
		// legitimately-wide repository series is expected and NOT evidence
		// of dropped data -- breakdownTruncated (capFactValueRows' own
		// per-fact signal) is the accurate truncation report here.
		_, breakdownTruncated, scanErr := p.readRepositoryMetrics(ctx, orgID, repoSubjects, &facts, timeBound, query.Time.EvidenceWindow)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query repository metrics", scanErr)
		}
		truncated = truncated || breakdownTruncated
	}

	if teamSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectTeam); len(teamSubjects) > 0 {
		rowCount, scanErr := p.readTeamMetrics(ctx, orgID, teamSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team metrics", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
	}

	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		rowCount, scanErr := p.readProjectMetrics(ctx, orgID, projectSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project metrics", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: truncated}, nil
}

// repositoryMetricsDayRow is one repo_metrics_daily row within the series
// window, scanned before Go-side per-repository grouping.
type repositoryMetricsDayRow struct {
	day                                                      string
	commitsCount, prsMerged, busFactor                       int64
	medianPRCycleHours, changeFailureRate, codeOwnershipGini float64
	hasMTTRHours                                             bool
	mttrHours                                                float64
}

// readRepositoryMetrics (CHAOS-4418 widening of CHAOS-3780's original
// single-row read) builds ONE CanonicalFact per repository subject carrying
// a real per-day series, not a flat scalar snapshot.
//
// Deliberately NOT readers.ReadRepositoryMetrics (CHAOS-4377): that shared
// reader's own row_number() PARTITION BY repo_id collapses to exactly ONE
// (the latest) row per repository by construction -- there is no multi-day
// result to reshape into a series without querying differently. This
// function instead builds its own raw, parameterized statement directly
// against repo_metrics_daily, the SAME pattern HealthProvider.readScope
// already uses for compounding_risk_daily (raw SQL through
// clickhouseFacts.query is an established provider-level pattern in this
// package, not a acr/AGENTS.md "scattered SQL through handlers"
// violation -- this is the provider's own read, not a route handler) --
// mirroring readers.ReadRepositoryMetrics' own row_number()/cityHash64
// intraday-rerun tiebreak, but PARTITION BY (repo_id, day) instead of
// repo_id alone, so every distinct day survives instead of only the
// latest.
func (p *MetricsProvider) readRepositoryMetrics(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound, evidenceWindow *contractsv1.ContextFabricRequestedEvidenceWindow) (rowCount int, breakdownTruncated bool, err error) {
	ids, bySubject := subjectIndex(subjects, repositoryPrefix)
	if len(ids) == 0 {
		return 0, false, nil
	}
	var dayPredicate string
	var extra []readers.Binding
	switch {
	case timeBound.active:
		// An explicit historical (as-of/range) axis question: the SAME
		// bound every other Tier A provider in this package already
		// applies, never widened or narrowed for this series specifically.
		dayPredicate = timeBound.dayPredicate("day")
		for _, binding := range timeBound.bindings() {
			extra = append(extra, readers.Binding{Name: binding.Name, Value: binding.Value})
		}
	case evidenceWindow != nil && evidenceWindow.RelativeID == contractsv1.ContextFabricRelativeWindowAllTime:
		// codex R4 finding 2: the all_time sentinel, which carries no
		// bounds BY DEFINITION (window.go's relativeWindowBounds returns
		// ok=false for it), reaches here now that the engine threads its
		// own canonical effective window into every fact read
		// (contextfabric.factReadQuestion). All of history is no day
		// predicate at all -- falling through to the 90-day default below
		// would answer a question about all of history with one quarter of
		// it. The read stays bounded by the per-repository
		// MetricsSeriesPerRepositoryRowCap and the 64-row per-fact cap,
		// never by a window nobody asked for.
		dayPredicate = ""
	case evidenceWindow != nil && evidenceWindow.Start != nil && evidenceWindow.End != nil:
		// CHAOS-4418 (team-lead ruling): the CURRENT axis's own
		// server-canonicalized evidence window, read verbatim, never
		// re-derived -- resolving a RelativeWindowID to absolute bounds is
		// exclusively window.go's relativeWindowBounds' own job ("the ONLY
		// function in this codebase that may" do so), and it has already
		// run by the time this value arrives. A RelativeID rides along
		// with the bounds; the BOUNDS are what this query uses, so a
		// second derivation against a different clock is impossible.
		//
		// toDate({...:DateTime64(6,'UTC')}), not a bare :Date parameter --
		// mirrors readers.TimeBound.DayPredicate's own convention exactly
		// (dev-health-go's timebound.go): the Go ClickHouse driver binds a
		// time.Time value against a DateTime64 parameter natively, and the
		// SQL-side toDate() narrows it to the day column's own grain.
		dayPredicate = " AND day >= toDate({series_window_start:DateTime64(6,'UTC')}) AND day <= toDate({series_window_end:DateTime64(6,'UTC')})"
		extra = []readers.Binding{
			{Name: "series_window_start", Value: evidenceWindow.Start.UTC()},
			{Name: "series_window_end", Value: evidenceWindow.End.UTC()},
		}
	default:
		// No historical bound and no explicit current-axis window --
		// metricsSeriesDefaultWindow's own doc comment explains why this
		// matches the platform's own default evidence-window policy
		// width, rather than a devhealthfacts-invented number.
		now := time.Now().UTC()
		dayPredicate = " AND day >= toDate({series_window_start:DateTime64(6,'UTC')}) AND day <= toDate({series_window_end:DateTime64(6,'UTC')})"
		extra = []readers.Binding{
			{Name: "series_window_start", Value: now.Add(-metricsSeriesDefaultWindow)},
			{Name: "series_window_end", Value: now},
		}
	}
	// Deliberately NOT withRowLimit (Codex R2, confirmed): that helper's
	// shared, query-WIDE LIMIT would still truncate the combined
	// (repo_id, day DESC)-ordered stream AFTER the per-repository `LIMIT
	// ... BY repo_id` below already ran, reintroducing the exact
	// cross-repository starvation MetricsSeriesPerRepositoryRowCap's own
	// doc comment explains -- a query-wide cap and a per-group cap do not
	// compose into "each group gets its fair share"; only dropping the
	// query-wide cap for this one query does.
	statement := `SELECT toString(repo_id), toString(day), toInt64(commits_count), toInt64(prs_merged), toFloat64(median_pr_cycle_hours), toFloat64(change_failure_rate), toUInt8(isNotNull(mttr_hours)), toFloat64(ifNull(mttr_hours, 0)), toInt64(bus_factor), toFloat64(code_ownership_gini)
FROM (
	-- CHAOS-4418: PARTITION BY (repo_id, day), NOT repo_id alone --
	-- every distinct day survives its own row_number()/cityHash64
	-- intraday-rerun dedup (the identical tiebreak reasoning
	-- readers.ReadRepositoryMetrics' own repo_id-only partition already
	-- uses, one level finer), instead of collapsing the whole repository
	-- down to its single latest day the way that shared reader does.
	SELECT repo_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini,
		row_number() OVER (PARTITION BY repo_id, day ORDER BY computed_at DESC, cityHash64(tuple(commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, ifNull(mttr_hours, -1), bus_factor, code_ownership_gini)) DESC) AS rn
	FROM repo_metrics_daily
	WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)}` + dayPredicate + `
)
WHERE rn = 1
ORDER BY repo_id, day DESC
LIMIT ` + strconv.Itoa(MetricsSeriesPerRepositoryRowCap) + ` BY repo_id`
	byRepo := make(map[string][]repositoryMetricsDayRow)
	var repoOrder []string
	// readers.QueryOrgScopedNamed (CHAOS-4418), not p.facts.query -- this
	// is genuinely raw SQL (not a readers.ReadXxx call), but it must still
	// report through the SAME readers.Instrumentation hook
	// NewInstrumentedProviders wires into ctx (instrumentation.go's own
	// doc comment): p.facts.query has no instrumentation hook of its own
	// (it is devhealthfacts's own acr-side mirror of this exact function,
	// per QueryOrgScopedNamed's own doc comment, deliberately without the
	// readers-package instrumentation piece), so using it here would
	// silently drop this read out of the same slog/span/counter coverage
	// readers.ReadRepositoryMetrics used to carry before this ticket
	// replaced it. "ReadRepositoryMetricsSeries" names the reader for
	// attribution -- a distinct name from "ReadRepositoryMetrics" because
	// this is a genuinely different query shape (a series, not one
	// collapsed row), never conflated with that reader's own metrics.
	scanErr := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadRepositoryMetricsSeries", statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var repoID string
		var r repositoryMetricsDayRow
		var hasMTTR uint8
		if scanErr := row.Scan(&repoID, &r.day, &r.commitsCount, &r.prsMerged, &r.medianPRCycleHours, &r.changeFailureRate, &hasMTTR, &r.mttrHours, &r.busFactor, &r.codeOwnershipGini); scanErr != nil {
			return scanErr
		}
		r.hasMTTRHours = hasMTTR != 0
		if _, ok := bySubject[repoID]; !ok {
			return nil
		}
		if _, seen := byRepo[repoID]; !seen {
			repoOrder = append(repoOrder, repoID)
		}
		byRepo[repoID] = append(byRepo[repoID], r)
		return nil
	}, extra...)
	if scanErr != nil {
		return rowCount, false, scanErr
	}
	for _, repoID := range repoOrder {
		subject := bySubject[repoID]
		days := byRepo[repoID]
		dayRows := make([]contextfabric.FactValueRow, 0, len(days))
		for _, d := range days {
			rowFields := map[string]contextfabric.FactValue{
				"day":                   contextfabric.StringFactValue(d.day),
				"commits_count":         contextfabric.IntegerFactValue(d.commitsCount),
				"prs_merged":            contextfabric.IntegerFactValue(d.prsMerged),
				"median_pr_cycle_hours": contextfabric.NumberFactValue(d.medianPRCycleHours),
				"change_failure_rate":   contextfabric.NumberFactValue(d.changeFailureRate),
				"bus_factor":            contextfabric.IntegerFactValue(d.busFactor),
				"code_ownership_gini":   contextfabric.NumberFactValue(d.codeOwnershipGini),
			}
			if d.hasMTTRHours {
				rowFields["mttr_hours"] = contextfabric.NumberFactValue(d.mttrHours)
			}
			dayRows = append(dayRows, contextfabric.FactValueRow{Fields: rowFields})
		}
		var omitted int
		// capFactValueRows keeps the FIRST maxFactValueRows entries and
		// drops the rest -- the SQL's own `ORDER BY repo_id, day DESC`
		// (not ascending) is what makes "first" mean "most recent" here,
		// so a series wider than the cap loses its OLDEST days, never
		// its freshest ones, on truncation.
		dayRows, omitted = capFactValueRows(dayRows)
		breakdownTruncated = breakdownTruncated || omitted > 0
		fields := map[string]contextfabric.FactValue{
			// day_count is the SERIES' own row count before any
			// 64-row cap -- distinguishable from
			// len(daily_metrics) so a truncated series is still
			// diagnosable (CanonicalRows' own Truncated flag already
			// reports the cap fired; this says how much was behind
			// it).
			"day_count":     contextfabric.IntegerFactValue(int64(len(days))),
			"daily_metrics": contextfabric.RowsFactValue(dayRows),
		}
		// codex R4 finding 1: the latest day's values, under the SAME
		// field names the pre-CHAOS-4418 reader emitted, as scalar
		// siblings of the series. genkitruntime.modelFacingFacts
		// (runtime.go) drops every Rows-shaped field before the fact set
		// reaches synthesis, so a value living only inside daily_metrics
		// is invisible to the model and cannot be grounded in -- which is
		// what these scalars were for. dayRows[0] is the freshest day
		// (the statement's own ORDER BY day DESC), so these speak for the
		// same "latest day per repository" instant the reader this
		// widened already did, and an unrecorded mttr_hours stays absent
		// here exactly as it is absent from its own row.
		for name, value := range dayRows[0].Fields {
			fields[name] = value
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactMetrics, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("repository", repoID)},
		})
	}
	return rowCount, breakdownTruncated, nil
}

// readTeamMetrics reads team_metrics_daily directly -- a genuinely
// team-scoped rollup, not a proxy through any repository the team touches.
// The SQL/scan half now lives in readers.ReadTeamMetrics (CHAOS-4377).
func (p *MetricsProvider) readTeamMetrics(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := subjectIndex(subjects, teamPrefix)
	if len(ids) == 0 {
		return 0, nil
	}
	rows, err := readers.ReadTeamMetrics(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		subject, ok := bySubject[r.TeamID]
		if !ok {
			continue
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactMetrics, Subject: subject,
			Fields: map[string]contextfabric.FactValue{
				"day":                       contextfabric.StringFactValue(r.Day),
				"commits_count":             contextfabric.IntegerFactValue(r.CommitsCount),
				"after_hours_commits_count": contextfabric.IntegerFactValue(r.AfterHoursCommitsCount),
				"weekend_commits_count":     contextfabric.IntegerFactValue(r.WeekendCommitsCount),
				"after_hours_commit_ratio":  contextfabric.NumberFactValue(r.AfterHoursCommitRatio),
				"weekend_commit_ratio":      contextfabric.NumberFactValue(r.WeekendCommitRatio),
			},
			EvidenceRefIDs: []string{evidenceRefID("team", r.TeamID)},
		})
	}
	return len(rows), nil
}

// readProjectMetrics rolls FactMetrics up for a project through
// projects -> team_project_ownership -> team_metrics_daily: every team
// owning the project (as of the requested instant, or currently on the
// current axis) contributes its own latest team_metrics_daily row. See the
// package doc comment above for why counts are summed but ratios are never
// averaged.
//
// The SQL/scan half -- including the CHAOS-4108 id-space join reasoning and
// the ownership-validity predicate -- and the count-summing/team-dedup
// aggregation now live in readers.ReadProjectMetricsBreakdown and
// readers.RollupProjectMetrics respectively (CHAOS-4377). This method only
// builds the CanonicalFact/FactValue shape on top.
func (p *MetricsProvider) readProjectMetrics(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, nil
	}
	breakdown, err := readers.ReadProjectMetricsBreakdown(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, err
	}
	rowCount := len(breakdown)
	for _, rollup := range readers.RollupProjectMetrics(breakdown) {
		subject, ok := bySubject[rollup.ProjectKey]
		if !ok {
			continue
		}
		teamRows := make([]contextfabric.FactValueRow, 0, len(rollup.TeamBreakdown))
		// evidenceRefIDs preserves readers.RollupProjectMetrics' own
		// first-seen, deduplicated team order -- the same determinism
		// invariant 8 requires for every fact-producing read in this
		// package.
		evidenceRefIDs := make([]string, 0, len(rollup.TeamBreakdown)+1)
		evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("project", rollup.ProjectKey))
		for _, r := range rollup.TeamBreakdown {
			teamRows = append(teamRows, contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
				"team_id":                   contextfabric.StringFactValue(r.TeamID),
				"team_name":                 contextfabric.StringFactValue(r.TeamName),
				"day":                       contextfabric.StringFactValue(r.Day),
				"commits_count":             contextfabric.IntegerFactValue(r.CommitsCount),
				"after_hours_commits_count": contextfabric.IntegerFactValue(r.AfterHoursCommitsCount),
				"weekend_commits_count":     contextfabric.IntegerFactValue(r.WeekendCommitsCount),
				"after_hours_commit_ratio":  contextfabric.NumberFactValue(r.AfterHoursCommitRatio),
				"weekend_commit_ratio":      contextfabric.NumberFactValue(r.WeekendCommitRatio),
			}})
			evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("team", r.TeamID))
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactMetrics, Subject: subject,
			Fields: map[string]contextfabric.FactValue{
				// rollup_basis states, in the fact's own structure, exactly
				// how this project-level number was derived: summed team
				// counts, never a repository proxy and never an averaged
				// rate. A synthesizer must not present this as a
				// project-native metrics table the way team/repository
				// facts are.
				"rollup_basis":              contextfabric.StringFactValue("team_project_ownership_sum"),
				"team_count":                contextfabric.IntegerFactValue(int64(rollup.TeamCount)),
				"commits_count":             contextfabric.IntegerFactValue(rollup.CommitsCount),
				"after_hours_commits_count": contextfabric.IntegerFactValue(rollup.AfterHoursCommitsCount),
				"weekend_commits_count":     contextfabric.IntegerFactValue(rollup.WeekendCommitsCount),
				// team_breakdown carries each contributing team's OWN
				// ratio, never averaged -- see the package doc comment.
				"team_breakdown": contextfabric.RowsFactValue(teamRows),
			},
			EvidenceRefIDs: evidenceRefIDs,
		})
	}
	return rowCount, nil
}
