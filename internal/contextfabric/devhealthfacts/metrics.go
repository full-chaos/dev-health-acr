package devhealthfacts

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const repositoryPrefix = "repository:"

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
		rowCount, scanErr := p.readRepositoryMetrics(ctx, orgID, repoSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query repository metrics", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
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

// readRepositoryMetrics is CHAOS-3780's original repo_metrics_daily read,
// unchanged in behavior, factored out so ReadFacts can branch by subject
// kind the same way HealthProvider already does for repo+team.
func (p *MetricsProvider) readRepositoryMetrics(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := subjectIndex(subjects, repositoryPrefix)
	if len(ids) == 0 {
		return 0, nil
	}
	// The hash tiebreak's ifNull(mttr_hours, -1) sentinel is only
	// unambiguous while -1 is outside mttr_hours' real domain. mttr_hours
	// is a mean-time-to-recovery DURATION in hours, so it is semantically
	// always >= 0 (verified against live data: no negative value
	// observed). There is no ClickHouse-level UInt/CHECK constraint
	// enforcing this -- it is a domain assumption, not a type guarantee.
	statement := withRowLimit(`SELECT toString(repo_id), toString(day), toInt64(commits_count), toInt64(prs_merged), toFloat64(median_pr_cycle_hours), toFloat64(change_failure_rate), toUInt8(isNotNull(mttr_hours)), toFloat64(ifNull(mttr_hours, 0)), toInt64(bus_factor), toFloat64(code_ownership_gini)
FROM (
	SELECT repo_id, day, commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, mttr_hours, bus_factor, code_ownership_gini,
		row_number() OVER (PARTITION BY repo_id ORDER BY day DESC, computed_at DESC, cityHash64(tuple(commits_count, prs_merged, median_pr_cycle_hours, change_failure_rate, ifNull(mttr_hours, -1), bus_factor, code_ownership_gini)) DESC) AS rn
	FROM repo_metrics_daily
	WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
)
WHERE rn = 1`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var repoID, day string
		var commitsCount, prsMerged, busFactor int64
		var medianPRCycleHours, changeFailureRate, ownershipGini, mttrHours float64
		var hasMTTR uint8
		if err := row.Scan(&repoID, &day, &commitsCount, &prsMerged, &medianPRCycleHours, &changeFailureRate, &hasMTTR, &mttrHours, &busFactor, &ownershipGini); err != nil {
			return err
		}
		subject, ok := bySubject[repoID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{
			"day":                   contextfabric.StringFactValue(day),
			"commits_count":         contextfabric.IntegerFactValue(commitsCount),
			"prs_merged":            contextfabric.IntegerFactValue(prsMerged),
			"median_pr_cycle_hours": contextfabric.NumberFactValue(medianPRCycleHours),
			"change_failure_rate":   contextfabric.NumberFactValue(changeFailureRate),
			"bus_factor":            contextfabric.IntegerFactValue(busFactor),
			"code_ownership_gini":   contextfabric.NumberFactValue(ownershipGini),
		}
		if hasMTTR != 0 {
			fields["mttr_hours"] = contextfabric.NumberFactValue(mttrHours)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactMetrics, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("repository", repoID)},
		})
		return nil
	}, timeBound.bindings()...)
	return rowCount, scanErr
}

// readTeamMetrics reads team_metrics_daily directly -- a genuinely
// team-scoped rollup, not a proxy through any repository the team touches.
func (p *MetricsProvider) readTeamMetrics(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := subjectIndex(subjects, teamPrefix)
	if len(ids) == 0 {
		return 0, nil
	}
	statement := withRowLimit(`SELECT toString(team_id), toString(day), toInt64(commits_count), toInt64(after_hours_commits_count), toInt64(weekend_commits_count), toFloat64(after_hours_commit_ratio), toFloat64(weekend_commit_ratio)
FROM (
	SELECT team_id, day, commits_count, after_hours_commits_count, weekend_commits_count, after_hours_commit_ratio, weekend_commit_ratio,
		row_number() OVER (PARTITION BY team_id ORDER BY day DESC, computed_at DESC, cityHash64(tuple(team_name, commits_count, after_hours_commits_count, weekend_commits_count, after_hours_commit_ratio, weekend_commit_ratio)) DESC) AS rn
	FROM team_metrics_daily
	WHERE org_id = {org_id:String} AND toString(team_id) IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
)
WHERE rn = 1`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var teamID, day string
		var commitsCount, afterHoursCommits, weekendCommits int64
		var afterHoursRatio, weekendRatio float64
		if err := row.Scan(&teamID, &day, &commitsCount, &afterHoursCommits, &weekendCommits, &afterHoursRatio, &weekendRatio); err != nil {
			return err
		}
		subject, ok := bySubject[teamID]
		if !ok {
			return nil
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactMetrics, Subject: subject,
			Fields: map[string]contextfabric.FactValue{
				"day":                       contextfabric.StringFactValue(day),
				"commits_count":             contextfabric.IntegerFactValue(commitsCount),
				"after_hours_commits_count": contextfabric.IntegerFactValue(afterHoursCommits),
				"weekend_commits_count":     contextfabric.IntegerFactValue(weekendCommits),
				"after_hours_commit_ratio":  contextfabric.NumberFactValue(afterHoursRatio),
				"weekend_commit_ratio":      contextfabric.NumberFactValue(weekendRatio),
			},
			EvidenceRefIDs: []string{evidenceRefID("team", teamID)},
		})
		return nil
	}, timeBound.bindings()...)
	return rowCount, scanErr
}

// projectMetricsRow is one (project, team) pair's contribution to a
// project's metrics rollup, scanned off the team_project_ownership join
// before Go-side aggregation.
type projectMetricsRow struct {
	projectKey                                                string
	teamID, teamName, day                                     string
	commitsCount, afterHoursCommitsCount, weekendCommitsCount int64
	afterHoursCommitRatio, weekendCommitRatio                 float64
}

// readProjectMetrics rolls FactMetrics up for a project through
// projects -> team_project_ownership -> team_metrics_daily: every team
// owning the project (as of the requested instant, or currently on the
// current axis) contributes its own latest team_metrics_daily row. See the
// package doc comment above for why counts are summed but ratios are never
// averaged.
//
// The join is NOT team_project_ownership.project_id -- that would repeat
// this codebase's own CHAOS-4108 id-space defect, documented in exhaustive
// detail by devhealthsource/teams_projects_edges.go's queryProjectTeams
// (its "SECOND -- the id-space trap" comment): team_project_ownership's own
// project_id column is NOT projects.id for every provider -- for gitlab
// rows it holds the project KEY, and only 1 of 3 distinct live values
// resolved via project_id where project_key resolved 3 of 3. A join on
// project_id would silently drop exactly the ownership edges this feature
// exists to surface, for the providers most likely to need it -- a false
// "no owning teams" instead of an honest one, which is worse than an error.
// This mirrors queryProjectTeams' own fix exactly: resolve the requested
// project's project_key from `projects` FIRST (canonical id still comes
// from `projects`, per identity.KindProject's own (provider, id) shape --
// never re-derived from the ownership table), then join
// team_project_ownership on (provider, project_key).
func (p *MetricsProvider) readProjectMetrics(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, nil
	}
	// The ownership edge is a slowly-changing dimension (valid_from/valid_to),
	// not a daily rollup -- it needs its own predicate, not dayPredicate.
	// "Currently active" on the current axis (valid_from already elapsed,
	// valid_to not yet reached -- a future-dated valid_from must not
	// contribute, the same as an already-lapsed valid_to must not);
	// "active AT THE END of the requested window" otherwise, the same
	// convention timebound.go's asOfExpression documents for every other
	// derived-state read in this package. now64(3) is a literal ClickHouse
	// function call, never caller-supplied text, so it carries no
	// injection surface -- the same class as maxFactRowsPerQuery's own
	// inlining (shared.go's withRowLimit doc comment).
	ownershipPredicate := " AND valid_from <= now64(3) AND valid_to IS NULL"
	if timeBound.active {
		ownershipPredicate = fmt.Sprintf(" AND valid_from <= {%s:DateTime64(6,'UTC')} AND (valid_to IS NULL OR valid_to > {%s:DateTime64(6,'UTC')})", boundEndParam, boundEndParam)
	}
	statement := withRowLimit(`SELECT concat(p.provider, ':', p.id), tm.team_id, tm.team_name, toString(tm.day), toInt64(tm.commits_count), toInt64(tm.after_hours_commits_count), toInt64(tm.weekend_commits_count), toFloat64(tm.after_hours_commit_ratio), toFloat64(tm.weekend_commit_ratio)
FROM (
	SELECT id, provider, project_key
	FROM projects FINAL
	WHERE org_id = {org_id:String} AND concat(provider, ':', id) IN {ids:Array(String)} AND project_key IS NOT NULL
) AS p
INNER JOIN (
	SELECT provider, project_key, team_id
	FROM team_project_ownership FINAL
	WHERE org_id = {org_id:String} AND project_key IS NOT NULL` + ownershipPredicate + `
	GROUP BY provider, project_key, team_id
) AS tpo ON tpo.provider = p.provider AND tpo.project_key = p.project_key
INNER JOIN (
	SELECT team_id, team_name, day, commits_count, after_hours_commits_count, weekend_commits_count, after_hours_commit_ratio, weekend_commit_ratio,
		row_number() OVER (PARTITION BY team_id ORDER BY day DESC, computed_at DESC, cityHash64(tuple(team_name, commits_count, after_hours_commits_count, weekend_commits_count, after_hours_commit_ratio, weekend_commit_ratio)) DESC) AS rn
	FROM team_metrics_daily
	WHERE org_id = {org_id:String}` + timeBound.dayPredicate("day") + `
) AS tm ON tm.team_id = tpo.team_id AND tm.rn = 1
ORDER BY p.id, tm.team_id`)
	rowCount := 0
	byProject := make(map[string][]projectMetricsRow)
	// projectOrder preserves first-seen scan order (map iteration below
	// would otherwise make fact order nondeterministic across runs of the
	// identical query -- invariant 8's "deterministic ordering" applies to
	// every fact-producing read in this package, not only fact_scope.go's
	// own expansions). The query's own ORDER BY above makes that scan order
	// itself deterministic, not merely incidental to one ClickHouse plan.
	var projectOrder []string
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var projectSubjectKey, teamID, teamName, day string
		var commitsCount, afterHoursCommitsCount, weekendCommitsCount int64
		var afterHoursCommitRatio, weekendCommitRatio float64
		if err := row.Scan(&projectSubjectKey, &teamID, &teamName, &day, &commitsCount, &afterHoursCommitsCount, &weekendCommitsCount, &afterHoursCommitRatio, &weekendCommitRatio); err != nil {
			return err
		}
		if _, ok := bySubject[projectSubjectKey]; !ok {
			return nil
		}
		if _, seen := byProject[projectSubjectKey]; !seen {
			projectOrder = append(projectOrder, projectSubjectKey)
		}
		byProject[projectSubjectKey] = append(byProject[projectSubjectKey], projectMetricsRow{
			projectKey: projectSubjectKey, teamID: teamID, teamName: teamName, day: day,
			commitsCount: commitsCount, afterHoursCommitsCount: afterHoursCommitsCount, weekendCommitsCount: weekendCommitsCount,
			afterHoursCommitRatio: afterHoursCommitRatio, weekendCommitRatio: weekendCommitRatio,
		})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return rowCount, scanErr
	}
	for _, projectKey := range projectOrder {
		rows := byProject[projectKey]
		subject := bySubject[projectKey]
		// team_project_ownership's own ORDER BY key includes `source`, so
		// the SAME team can legitimately appear more than once for one
		// project (e.g. a native AND a manual ownership edge both current
		// at once). Deduplicate by team_id before summing, or a team owning
		// a project through two sources would be double-counted.
		seenTeams := make(map[string]bool, len(rows))
		var totalCommits, totalAfterHoursCommits, totalWeekendCommits int64
		teamRows := make([]contextfabric.FactValueRow, 0, len(rows))
		// evidenceRefIDs is built in this SAME pass (scan order), not by
		// ranging over seenTeams afterwards: a map range order is
		// nondeterministic, and the SAME determinism requirement that
		// ordered projectOrder above applies to a fact's own
		// EvidenceRefIDs too.
		evidenceRefIDs := make([]string, 0, len(rows)+1)
		evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("project", projectKey))
		for _, r := range rows {
			if seenTeams[r.teamID] {
				continue
			}
			seenTeams[r.teamID] = true
			totalCommits += r.commitsCount
			totalAfterHoursCommits += r.afterHoursCommitsCount
			totalWeekendCommits += r.weekendCommitsCount
			teamRows = append(teamRows, contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
				"team_id":                   contextfabric.StringFactValue(r.teamID),
				"team_name":                 contextfabric.StringFactValue(r.teamName),
				"day":                       contextfabric.StringFactValue(r.day),
				"commits_count":             contextfabric.IntegerFactValue(r.commitsCount),
				"after_hours_commits_count": contextfabric.IntegerFactValue(r.afterHoursCommitsCount),
				"weekend_commits_count":     contextfabric.IntegerFactValue(r.weekendCommitsCount),
				"after_hours_commit_ratio":  contextfabric.NumberFactValue(r.afterHoursCommitRatio),
				"weekend_commit_ratio":      contextfabric.NumberFactValue(r.weekendCommitRatio),
			}})
			evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("team", r.teamID))
		}
		if len(teamRows) == 0 {
			continue
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
				"team_count":                contextfabric.IntegerFactValue(int64(len(teamRows))),
				"commits_count":             contextfabric.IntegerFactValue(totalCommits),
				"after_hours_commits_count": contextfabric.IntegerFactValue(totalAfterHoursCommits),
				"weekend_commits_count":     contextfabric.IntegerFactValue(totalWeekendCommits),
				// team_breakdown carries each contributing team's OWN
				// ratio, never averaged -- see the package doc comment.
				"team_breakdown": contextfabric.RowsFactValue(teamRows),
			},
			EvidenceRefIDs: evidenceRefIDs,
		})
	}
	return rowCount, nil
}
