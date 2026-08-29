package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// HealthProvider implements contextfabric.FactProvider for FactHealth from
// compounding_risk_daily -- the one canonical, precomputed-by-Ops risk/health
// signal in ClickHouse today. Dev Health Ops computes compounding_risk and
// severity nightly from a fixed, documented formula (see the table's own
// w_churn/w_complexity/w_ownership/w_review/threshold_* columns, which this
// provider never reads or reinterprets); this provider only ever reads the
// already-computed severity/score, never recomputes a health rule -- Ops
// stays the sole authority for what "healthy" means (§19.6.3/§19.11).
//
// compounding_risk_daily carries both repo-scoped and team-scoped rows
// (scope='repo'/'team'), so this provider supports both subject kinds, the
// same dual-block shape identity.go's IdentityProvider uses for repository +
// work item.
//
// Live data shows up to 86 rows sharing the IDENTICAL computed_at for one
// (scope, scope_id) key (Codex finding F2, confirmed against real
// ClickHouse data) -- an independent argMax(severity, computed_at) and
// argMax(compounding_risk, computed_at) in the same query have no guarantee
// of resolving that tie to the same underlying row, so this provider uses
// row_number() OVER (... ORDER BY day DESC, computed_at DESC), picking rn=1
// and scanning every field off that ONE row.
//
// day DESC, computed_at DESC is still not a TOTAL order given that same
// 86-way computed_at tie (Codex round-2 finding M1): compounding_risk_daily
// has no per-row unique id, so without a further tiebreaker row_number()
// could pick a different tied row on different executions of the identical
// query. cityHash64 of severity/compounding_risk is the last ORDER BY
// term -- arbitrary among an exact tie, but stable, so the same row wins
// every time.
// CHAOS-4363 widens FactHealth to add SubjectProject: a project rolls up
// compounding_risk_daily two ways at once, both via real ownership joins,
// never the CHAOS-4099 activity-proxy route (that route's own project-origin
// entry in fact_scope.go's factScopeEligibility, targeting SubjectRepository,
// stays policy `none` -- it names a DIFFERENT, still-nonexistent path: giving
// a project question access to the ACTUAL repository subjects underneath
// it, not this rollup):
//
//   - team layer: project -> team_project_ownership -> compounding_risk_daily
//     (scope='team'), the same join metrics.go's readProjectMetrics uses.
//   - repo layer: project -> team_project_ownership -> team_repo_ownership ->
//     compounding_risk_daily (scope='repo'), one hop further -- a project's
//     repositories are reached through the teams that own it, since there is
//     no direct project->repository ownership table.
//
// Both layers land in one renderable risk_breakdown table per project,
// tagged by `scope` ('team' or 'repo'), never summed or averaged into a
// single project-level risk score.
type HealthProvider struct{ facts clickhouseFacts }

func newHealthProvider(client contextpacket.ClickHouseQueryClient) *HealthProvider {
	return &HealthProvider{facts: clickhouseFacts{client: client}}
}

func (p *HealthProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactHealth, "devhealthfacts.health", []contextfabric.SubjectKind{
		contextfabric.SubjectRepository, contextfabric.SubjectTeam, contextfabric.SubjectProject,
	})
}

func (p *HealthProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
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

	repoIDs, repoBySubject := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectRepository), repositoryPrefix)
	if len(repoIDs) > 0 {
		rowCount, scanErr := p.readScope(ctx, orgID, "repo", repoIDs, repoBySubject, "repository", &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query repository health", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
	}

	teamIDs, teamBySubject := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectTeam), teamPrefix)
	if len(teamIDs) > 0 {
		rowCount, scanErr := p.readScope(ctx, orgID, "team", teamIDs, teamBySubject, "team", &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team health", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
	}

	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		rowCount, breakdownTruncated, scanErr := p.readProjectHealth(ctx, orgID, projectSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query project health", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery || breakdownTruncated
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	// CHAOS-4521b: this source has no project dimension, so an all-project
	// read that came back empty says something more specific than "no rows".
	retentionReason = explainTeamScopedProjectAbsence(timeBound, state, retentionReason, query.Subjects)
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: truncated}, nil
}

// readScope runs the compounding_risk_daily query for one scope ('repo' or
// 'team'), appending a CanonicalFact per matched subject into facts. scope is
// an internal Go string literal (never caller-supplied), so it is safe to
// inline into the statement the same way withRowLimit's maxFactRowsPerQuery
// is.
// riskRuleComponent names one compounding_risk_daily formula term, in the
// table's own declared column order (CHAOS-4418: risk_rules Rows below
// reports these as one row per component instead of leaving the formula's
// own inputs invisible behind the single combined compounding_risk score).
type riskRuleComponent struct {
	signal, normColumn, weightColumn string
}

// riskRuleComponents mirrors compounding_risk_daily's own schema exactly
// (churn_norm/complexity_norm/ownership_norm/review_norm paired with
// w_churn/w_complexity/w_ownership/w_review) -- never a second,
// independently maintained list of which signals make up the score.
var riskRuleComponents = []riskRuleComponent{
	{signal: "churn", normColumn: "churn_norm", weightColumn: "w_churn"},
	{signal: "complexity", normColumn: "complexity_norm", weightColumn: "w_complexity"},
	{signal: "ownership", normColumn: "ownership_norm", weightColumn: "w_ownership"},
	{signal: "review", normColumn: "review_norm", weightColumn: "w_review"},
}

// riskRuleValue is one riskRuleComponents entry's scanned value for one
// scope_id row.
type riskRuleValue struct {
	hasNorm bool
	norm    float64
	weight  float64
}

func (p *HealthProvider) readScope(ctx context.Context, orgID, scope string, ids []string, bySubject map[string]contextfabric.SubjectRef, evidenceEntityType string, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, error) {
	// The hash tiebreak's ifNull(compounding_risk, -1) sentinel is only
	// unambiguous while -1 is outside compounding_risk's real domain.
	// compounding_risk is a normalized risk SCORE; live data ranges
	// [0.0000127, 0.58], never negative. There is no ClickHouse-level
	// UInt/CHECK constraint enforcing this -- it is a domain assumption,
	// not a type guarantee.
	//
	// CHAOS-4418: widened to also select the formula's own 4 weighted
	// components (churn/complexity/ownership/review) off the SAME
	// row_number()-picked physical row compounding_risk/severity already
	// come from -- never a second, independent query that could stitch a
	// fact together from a different rerun of the same day (the exact
	// stitching risk this file's own package doc comment already warns
	// about for per-field argMax).
	//
	// Codex R1 (confirmed): the tiebreak hash MUST widen to cover the new
	// columns too, not stay as-is. Two reruns can share the identical
	// day/computed_at/severity/compounding_risk (an exact tie on the OLD
	// hash's own 2-column tuple) while genuinely differing on
	// churn_norm/complexity_norm/ownership_norm/review_norm/w_* -- the
	// package's own doc comment already establishes that reruns carry
	// "genuinely different values, not no-op repeats". Leaving the old
	// 2-column hash would let row_number() pick EITHER tied row
	// arbitrarily on different executions of the identical query, so
	// risk_rules could flap between two different tied rows even though
	// severity/compounding_risk themselves never change -- the exact
	// "same tied inputs must always hash to the same value" property this
	// tiebreak exists to guarantee, now violated for every column beyond
	// the original two.
	statement := withRowLimit(`SELECT scope_id, toString(severity), toUInt8(isNotNull(compounding_risk)), toFloat64(ifNull(compounding_risk, 0)), toString(computed_at),
	toUInt8(isNotNull(churn_norm)), toFloat64(ifNull(churn_norm, 0)), toFloat64(w_churn),
	toUInt8(isNotNull(complexity_norm)), toFloat64(ifNull(complexity_norm, 0)), toFloat64(w_complexity),
	toUInt8(isNotNull(ownership_norm)), toFloat64(ifNull(ownership_norm, 0)), toFloat64(w_ownership),
	toUInt8(isNotNull(review_norm)), toFloat64(ifNull(review_norm, 0)), toFloat64(w_review)
FROM (
	SELECT scope_id, severity, compounding_risk, computed_at, churn_norm, complexity_norm, ownership_norm, review_norm, w_churn, w_complexity, w_ownership, w_review,
		row_number() OVER (PARTITION BY scope_id ORDER BY day DESC, computed_at DESC, cityHash64(tuple(severity, ifNull(compounding_risk, -1), ifNull(churn_norm, -1), ifNull(complexity_norm, -1), ifNull(ownership_norm, -1), ifNull(review_norm, -1), w_churn, w_complexity, w_ownership, w_review)) DESC) AS rn
	FROM compounding_risk_daily
	WHERE org_id = {org_id:String} AND scope = '` + scope + `' AND scope_id IN {ids:Array(String)}` + timeBound.dayPredicate("day") + `
)
WHERE rn = 1`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var scopeID, severity, computedAt string
		var hasRisk uint8
		var risk float64
		values := make([]riskRuleValue, len(riskRuleComponents))
		scanArgs := []any{&scopeID, &severity, &hasRisk, &risk, &computedAt}
		hasNormFlags := make([]uint8, len(riskRuleComponents))
		for i := range riskRuleComponents {
			scanArgs = append(scanArgs, &hasNormFlags[i], &values[i].norm, &values[i].weight)
		}
		if err := row.Scan(scanArgs...); err != nil {
			return err
		}
		for i := range values {
			values[i].hasNorm = hasNormFlags[i] != 0
		}
		subject, ok := bySubject[scopeID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{
			"severity":    stringOrNull(severity),
			"computed_at": contextfabric.StringFactValue(computedAt),
		}
		if hasRisk != 0 {
			fields["compounding_risk"] = contextfabric.NumberFactValue(risk)
		}
		ruleRows := make([]contextfabric.FactValueRow, 0, len(riskRuleComponents))
		for i, component := range riskRuleComponents {
			v := values[i]
			rowFields := map[string]contextfabric.FactValue{
				"signal": contextfabric.StringFactValue(component.signal),
				"weight": contextfabric.NumberFactValue(v.weight),
			}
			// An unrecorded normalized signal is unknown, never zero
			// (AGENTS.md North Star check 12) -- the row still names the
			// signal and its configured weight, but norm_value/
			// weighted_contribution stay explicitly null rather than a
			// fabricated 0 that would understate the component's real,
			// unrecorded contribution.
			if v.hasNorm {
				rowFields["norm_value"] = contextfabric.NumberFactValue(v.norm)
				rowFields["weighted_contribution"] = contextfabric.NumberFactValue(v.weight * v.norm)
			} else {
				rowFields["norm_value"] = contextfabric.NullFactValue()
				rowFields["weighted_contribution"] = contextfabric.NullFactValue()
			}
			ruleRows = append(ruleRows, contextfabric.FactValueRow{Fields: rowFields})
		}
		fields["risk_rules"] = contextfabric.RowsFactValue(ruleRows)
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactHealth, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(evidenceEntityType, scopeID)},
		})
		return nil
	}, timeBound.bindings()...)
	return rowCount, scanErr
}

// compoundingRiskLatestSubquery returns the row_number()-deduplicated latest
// row per scope_id for one compounding_risk_daily scope ('repo' or 'team'),
// mirroring readScope's own statement exactly (scope is an internal Go
// string literal, never caller-supplied, so it is safe to inline the same
// way readScope's own `scope` parameter already is).
func compoundingRiskLatestSubquery(scope string, timeBound factTimeBound) string {
	return `SELECT scope_id, severity, compounding_risk, computed_at,
		row_number() OVER (PARTITION BY scope_id ORDER BY day DESC, computed_at DESC, cityHash64(tuple(severity, ifNull(compounding_risk, -1))) DESC) AS rn
	FROM compounding_risk_daily
	WHERE org_id = {org_id:String} AND scope = '` + scope + `'` + timeBound.dayPredicate("day")
}

// healthRollupRow is one (project, scope, scope_id) triple's contribution to
// a project's health rollup, scanned off the ownership join before Go-side
// grouping. scope is 'team' or 'repo' -- see readProjectHealth's doc
// comment for the two-layer chain.
type healthRollupRow struct {
	scope, scopeID, scopeName, severity, computedAt string
	hasRisk                                         bool
	risk                                            float64
}

// readProjectHealth rolls FactHealth up for a project two ways at once (see
// the package doc comment): a team layer via team_project_ownership and a
// repo layer one hop further via team_repo_ownership, both landing in one
// renderable risk_breakdown table per project, tagged by scope. Neither
// layer is summed or averaged into a single project-level risk score.
func (p *HealthProvider) readProjectHealth(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (rowCount int, breakdownTruncated bool, err error) {
	ids, bySubject := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, false, nil
	}
	ownershipPredicate := ownershipValidityPredicate(timeBound)
	// Round-1 P2: the team/repo UNION ALL is wrapped in an outer SELECT
	// before withRowLimit's LIMIT is applied. Appending LIMIT directly after
	// two UNION ALL'd SELECTs binds it to the SECOND (repo) branch only --
	// the team branch would be unbounded, exceeding the advertised
	// maxFactRowsPerQuery cap -- and UNION ALL output order is otherwise
	// unspecified, which would make risk_breakdown/evidence ordering vary
	// between identical reads. The outer ORDER BY makes both the bound and
	// the ordering apply to the COMBINED result.
	statement := withRowLimit(`SELECT project_key, scope, scope_id, scope_name, severity, has_risk, risk, computed_at
FROM (
	SELECT concat(p.provider, ':', p.id) AS project_key, 'team' AS scope, tpo.team_id AS scope_id, ifNull(t.name, '') AS scope_name, toString(cr.severity) AS severity, toUInt8(isNotNull(cr.compounding_risk)) AS has_risk, toFloat64(ifNull(cr.compounding_risk, 0)) AS risk, toString(cr.computed_at) AS computed_at
	FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
	INNER JOIN (` + compoundingRiskLatestSubquery("team", timeBound) + `) AS cr ON cr.scope_id = tpo.team_id AND cr.rn = 1
	LEFT JOIN (SELECT id, name FROM teams FINAL WHERE org_id = {org_id:String}) AS t ON t.id = tpo.team_id

	UNION ALL

	SELECT concat(p.provider, ':', p.id) AS project_key, 'repo' AS scope, tro.repo_key AS scope_id, tro.repo_full_name AS scope_name, toString(cr.severity) AS severity, toUInt8(isNotNull(cr.compounding_risk)) AS has_risk, toFloat64(ifNull(cr.compounding_risk, 0)) AS risk, toString(cr.computed_at) AS computed_at
	FROM ` + projectOwnershipJoinSQL(ownershipPredicate) + `
	INNER JOIN (
		SELECT team_id, toString(repo_id) AS repo_key, repo_full_name
		FROM team_repo_ownership FINAL
		WHERE org_id = {org_id:String} AND repo_id IS NOT NULL` + ownershipPredicate + `
		GROUP BY team_id, repo_key, repo_full_name
	) AS tro ON tro.team_id = tpo.team_id
	INNER JOIN (` + compoundingRiskLatestSubquery("repo", timeBound) + `) AS cr ON cr.scope_id = tro.repo_key AND cr.rn = 1
)
ORDER BY project_key, scope, scope_id`)
	byProject := make(map[string][]healthRollupRow)
	var projectOrder []string
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var projectSubjectKey, scope, scopeID, scopeName, severity, computedAt string
		var hasRisk uint8
		var risk float64
		if err := row.Scan(&projectSubjectKey, &scope, &scopeID, &scopeName, &severity, &hasRisk, &risk, &computedAt); err != nil {
			return err
		}
		if _, ok := bySubject[projectSubjectKey]; !ok {
			return nil
		}
		if _, seen := byProject[projectSubjectKey]; !seen {
			projectOrder = append(projectOrder, projectSubjectKey)
		}
		byProject[projectSubjectKey] = append(byProject[projectSubjectKey], healthRollupRow{
			scope: scope, scopeID: scopeID, scopeName: scopeName, severity: severity, computedAt: computedAt,
			hasRisk: hasRisk != 0, risk: risk,
		})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return rowCount, false, scanErr
	}
	for _, projectKey := range projectOrder {
		rows := byProject[projectKey]
		subject := bySubject[projectKey]
		seenScopeEntries := make(map[string]bool, len(rows))
		seenTeams := make(map[string]bool, len(rows))
		seenRepos := make(map[string]bool, len(rows))
		riskRows := make([]contextfabric.FactValueRow, 0, len(rows))
		evidenceRefIDs := make([]string, 0, len(rows)+1)
		evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("project", projectKey))
		for _, r := range rows {
			dedupeKey := r.scope + "\x00" + r.scopeID
			if dedupeTeamRow(seenScopeEntries, dedupeKey) {
				continue
			}
			switch r.scope {
			case "team":
				if !dedupeTeamRow(seenTeams, r.scopeID) {
					evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("team", r.scopeID))
				}
			case "repo":
				if !dedupeTeamRow(seenRepos, r.scopeID) {
					evidenceRefIDs = append(evidenceRefIDs, evidenceRefID("repository", r.scopeID))
				}
			}
			rowFields := map[string]contextfabric.FactValue{
				"scope":       contextfabric.StringFactValue(r.scope),
				"scope_id":    contextfabric.StringFactValue(r.scopeID),
				"scope_name":  stringOrNull(r.scopeName),
				"severity":    stringOrNull(r.severity),
				"computed_at": contextfabric.StringFactValue(r.computedAt),
			}
			if r.hasRisk {
				rowFields["compounding_risk"] = contextfabric.NumberFactValue(r.risk)
			}
			riskRows = append(riskRows, contextfabric.FactValueRow{Fields: rowFields})
		}
		if len(riskRows) == 0 {
			continue
		}
		var omitted int
		riskRows, omitted = capFactValueRows(riskRows)
		breakdownTruncated = breakdownTruncated || omitted > 0
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactHealth, Subject: subject,
			Fields: map[string]contextfabric.FactValue{
				// rollup_basis discloses BOTH chains this fact draws from --
				// see the package doc comment's two-layer explanation.
				"rollup_basis":   contextfabric.StringFactValue("team_project_ownership_and_team_repo_ownership"),
				"team_count":     contextfabric.IntegerFactValue(int64(len(seenTeams))),
				"repo_count":     contextfabric.IntegerFactValue(int64(len(seenRepos))),
				"risk_breakdown": contextfabric.RowsFactValue(riskRows),
			},
			EvidenceRefIDs: evidenceRefIDs,
		})
	}
	return rowCount, breakdownTruncated, nil
}
