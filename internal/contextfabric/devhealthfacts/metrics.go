package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
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
// factored out so ReadFacts can branch by subject kind the same way
// HealthProvider already does for repo+team. The SQL/scan half, including
// the row_number()/cityHash64 tiebreak reasoning and the mttr_hours
// sentinel-range note, now lives in readers.ReadRepositoryMetrics
// (CHAOS-4377).
func (p *MetricsProvider) readRepositoryMetrics(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	ids, bySubject := subjectIndex(subjects, repositoryPrefix)
	if len(ids) == 0 {
		return 0, nil
	}
	rows, err := readers.ReadRepositoryMetrics(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		subject, ok := bySubject[r.RepoID]
		if !ok {
			continue
		}
		fields := map[string]contextfabric.FactValue{
			"day":                   contextfabric.StringFactValue(r.Day),
			"commits_count":         contextfabric.IntegerFactValue(r.CommitsCount),
			"prs_merged":            contextfabric.IntegerFactValue(r.PRsMerged),
			"median_pr_cycle_hours": contextfabric.NumberFactValue(r.MedianPRCycleHours),
			"change_failure_rate":   contextfabric.NumberFactValue(r.ChangeFailureRate),
			"bus_factor":            contextfabric.IntegerFactValue(r.BusFactor),
			"code_ownership_gini":   contextfabric.NumberFactValue(r.CodeOwnershipGini),
		}
		if r.HasMTTRHours {
			fields["mttr_hours"] = contextfabric.NumberFactValue(r.MTTRHours)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactMetrics, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("repository", r.RepoID)},
		})
	}
	return len(rows), nil
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
