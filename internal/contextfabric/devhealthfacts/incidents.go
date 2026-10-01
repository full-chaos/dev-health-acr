package devhealthfacts

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// IncidentsProvider implements contextfabric.FactProvider for FactIncidents
// from operational_incidents.normalized_status/normalized_severity (falling
// back to the raw_* columns) -- the same columns and fallback
// devhealthsource/tables.go's queryIncidents already reads. A soft-deleted
// incident (is_deleted = 1, devhealthsource's one confirmed soft-delete
// signal for this table) yields no fact entry for that subject, the same as
// any other zero-row match, rather than reporting stale data.
//
// work_graph_deployment_incident_edges (queried by
// devhealthsource/tables.go's queryDeploymentIncidentEdges) is not joined
// in here: it would only add a deployment linkage, which doesn't fit this
// fact kind's scalar Fields cleanly (an incident can correlate to more than
// one deployment) -- left for a future FactKind or a repeated-fact
// refinement rather than guessing a shape now.
type IncidentsProvider struct{ facts clickhouseFacts }

func newIncidentsProvider(client contextpacket.ClickHouseQueryClient) *IncidentsProvider {
	return &IncidentsProvider{facts: clickhouseFacts{client: client}}
}

func (p *IncidentsProvider) Capability() contextfabric.FactCapability {
	capability := newCapability(contextfabric.FactIncidents, "devhealthfacts.incidents", []contextfabric.SubjectKind{contextfabric.SubjectIncident, contextfabric.SubjectTeam})
	capability.Tables = map[contextfabric.SubjectKind][]contextfabric.FactTableShape{
		contextfabric.SubjectTeam: {contextfabric.FactTableBreakdown},
	}
	return capability
}

func (p *IncidentsProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject, rejected := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectIncident), "incident:")
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.incidents", contextfabric.FactIncidents, rejected)
		}
	}()
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	var teamOutcome teamRollupOutcome
	if teamSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectTeam); len(teamSubjects) > 0 {
		var teamErr error
		teamOutcome, teamErr = p.readTeamRollup(ctx, orgID, teamSubjects, &facts, timeBound, query.Time.EvidenceWindow)
		if teamErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team incidents", teamErr)
		}
		rejected += teamOutcome.rejected
	}
	teamFactCount := len(facts)
	// CHAOS-4377: the SQL build + scan half (the status/severity Tier
	// B/Tier C split, the soft-delete guard) moved to
	// github.com/full-chaos/dev-health-go/readers.ReadIncidents; its doc
	// comment carries that reasoning now.
	//
	// The severity omission still surfaces here: a historical read comes
	// back with severity forced to an empty string by the reader, and
	// stringOrNull below turns that into an absent field -- an absent
	// field is unknown, never a guess (§19.8.3, §3.5). The exclusion is
	// named in the provider's own reason (incidentSeverityOmittedReason
	// below) so a reader learns which field went missing and why, rather
	// than silently receiving a thinner fact.
	var rows []readers.IncidentRow
	var scanErr error
	if len(ids) > 0 {
		rows, scanErr = readers.ReadIncidents(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	}
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query incidents", scanErr)
	}
	for _, row := range rows {
		subject, ok := bySubject[row.ID]
		if !ok {
			continue
		}
		fields := map[string]contextfabric.FactValue{"status": stringOrNull(row.Status)}
		if row.Severity != "" {
			fields["severity"] = contextfabric.StringFactValue(row.Severity)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactIncidents, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityIncident, row.ID)},
		})
	}
	state, retentionReason := timeBound.retentionState(len(rows) + teamFactCount)
	result = contextfabric.FactProviderResult{
		Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion,
		Grain: timeBound.effectiveGrain(grainExact), Truncated: len(rows) >= maxFactRowsPerQuery || teamOutcome.truncated,
	}
	applyNoOwnedRepositories(&result, teamOutcome.teamsWithoutRepos)
	// Retention wins over the severity note: with no rows at all there is
	// no fact whose severity could have been omitted, and reporting the
	// omission would point a reader at the wrong limitation.
	if timeBound.active && retentionReason == "" && len(rows) > 0 {
		result.Reason = incidentSeverityOmittedReason
	}
	return result, nil
}

// incidentRollupRepo is one owned repository's deployment-linked incidents
// inside the rollup window.
type incidentRollupRepo struct{ incidents, resolved int64 }

// readTeamRollup serves a team subject. operational_incidents carries no
// repository, so an incident reaches a repository only through a
// deployment-incident edge that names one (work_graph_deployment_incident_
// edges.repo_id): the rollup is deployment_linked, and incidents with no such
// edge are reported as org-wide not attributable, never assigned to a team.
func (p *IncidentsProvider) readTeamRollup(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound, evidence *contractsv1.ContextFabricRequestedEvidenceWindow) (outcome teamRollupOutcome, err error) {
	teamIDs, bySubject, rejected := subjectIndex(subjects, teamPrefix)
	outcome.rejected = rejected
	if len(teamIDs) == 0 {
		return outcome, nil
	}
	owned, err := teamOwnedRepositories(ctx, p.facts.client, orgID, teamIDs, timeBound)
	if err != nil {
		return outcome, err
	}
	window := resolveRollupWindow(timeBound, evidence, time.Now())
	byRepo := map[string]incidentRollupRepo{}
	incidentTime := "ifNull(i.started_at, i.observed_at)"
	if repoKeys := repoKeysOf(owned); len(repoKeys) > 0 {
		statement := `SELECT toString(e.repo_id), toInt64(count()), toInt64(countIf(i.resolved_at IS NOT NULL AND ` + window.upperExpr("i.resolved_at") + `))
FROM (
	SELECT DISTINCT repo_id, incident_id
	FROM work_graph_deployment_incident_edges
	WHERE toString(org_id) = {org_id:String} AND repo_id IS NOT NULL AND toString(repo_id) IN {ids:Array(String)}
) AS e
INNER JOIN (
	SELECT id, started_at, observed_at, resolved_at
	FROM operational_incidents FINAL
	WHERE org_id = {org_id:String} AND is_deleted = 0
) AS i ON i.id = e.incident_id
WHERE ` + window.timestampExpr(incidentTime) + `
GROUP BY e.repo_id
ORDER BY e.repo_id`
		if scanErr := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadTeamIncidentRollup", statement, orgID, repoKeys, func(row contextpacket.ClickHouseRowScanner) error {
			var repoID string
			var r incidentRollupRepo
			if err := row.Scan(&repoID, &r.incidents, &r.resolved); err != nil {
				return err
			}
			byRepo[repoID] = r
			return nil
		}, window.bindings()...); scanErr != nil {
			return outcome, scanErr
		}
	}
	// Org-wide, once: incidents in the window that no deployment-incident edge
	// ties to any repository. It is not a team count and is declared
	// aggregate so a repository-restricted caller never receives it.
	var notAttributable int64
	if len(owned) > 0 {
		statement := `SELECT toInt64(count())
FROM operational_incidents FINAL
WHERE org_id = {org_id:String} AND is_deleted = 0 AND ` + window.timestampExpr("ifNull(started_at, observed_at)") + `
	AND id NOT IN (SELECT incident_id FROM work_graph_deployment_incident_edges WHERE toString(org_id) = {org_id:String} AND repo_id IS NOT NULL)`
		if scanErr := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadTeamIncidentNotAttributable", statement, orgID, teamIDs, func(row contextpacket.ClickHouseRowScanner) error {
			return row.Scan(&notAttributable)
		}, window.bindings()...); scanErr != nil {
			return outcome, scanErr
		}
	}
	for _, teamID := range teamIDs {
		subject := bySubject[teamID]
		repos := owned[teamID]
		if len(repos) == 0 {
			outcome.teamsWithoutRepos++
			continue
		}
		// An incident linked to two owned repositories counts once for the
		// team, so the team total is a distinct count over the owned set, not
		// the sum of the per-repository counts.
		var withData int64
		breakdown := make([]contextfabric.FactValueRow, 0, len(repos))
		for _, repo := range repos {
			r, ok := byRepo[repo.key]
			if !ok {
				continue
			}
			outcome.contributed++
			withData++
			cells := map[string]contextfabric.FactValue{
				"repository_id":                   contextfabric.StringFactValue(repo.key),
				"incidents_count_window":          contextfabric.IntegerFactValue(r.incidents),
				"resolved_incidents_count_window": contextfabric.IntegerFactValue(r.resolved),
			}
			if repo.name != "" {
				cells["repository_name"] = contextfabric.StringFactValue(repo.name)
			}
			breakdown = append(breakdown, contextfabric.FactValueRow{Fields: cells})
		}
		fields := map[string]contextfabric.FactValue{
			"rollup_basis":                                contextfabric.StringFactValue(teamRollupBasis),
			"incident_attribution_basis":                  contextfabric.StringFactValue("deployment_linked"),
			"window_basis":                                contextfabric.StringFactValue(window.basis),
			"owned_repository_count":                      contextfabric.IntegerFactValue(int64(len(repos))),
			"repositories_with_data_count":                contextfabric.IntegerFactValue(withData),
			"repositories_without_data_count":             contextfabric.IntegerFactValue(int64(len(repos)) - withData),
			"org_incidents_not_attributable_count_window": contextfabric.IntegerFactValue(notAttributable),
		}
		if value, ok := window.startValue(); ok {
			fields["window_start"] = value
		}
		if value, ok := window.endValue(); ok {
			fields["window_end"] = value
		}
		if table, omitted, ok := ownedRepositoriesFactValue(repos); ok {
			fields["owned_repositories"] = table
			if omitted > 0 {
				fields["owned_repositories_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
			}
		}
		if withData > 0 {
			distinct, resolved, distinctErr := p.teamDistinctIncidents(ctx, orgID, repos, window)
			if distinctErr != nil {
				return outcome, distinctErr
			}
			fields["incidents_count_window"] = contextfabric.IntegerFactValue(distinct)
			fields["resolved_incidents_count_window"] = contextfabric.IntegerFactValue(resolved)
			if table, omitted, ok := repositoryBreakdownFactValue(breakdown, grainExact,
				[]string{"incidents_count_window", "resolved_incidents_count_window"}, []string{"repository_name"}); ok {
				fields["repository_breakdown"] = table
				if omitted > 0 {
					outcome.truncated = true
					fields["repository_breakdown_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
				}
			}
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactIncidents, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, teamID)},
		})
	}
	return outcome, nil
}

// teamDistinctIncidents counts the distinct incidents linked to any of one
// team's owned repositories inside the window.
func (p *IncidentsProvider) teamDistinctIncidents(ctx context.Context, orgID string, repos []teamOwnedRepo, window rollupWindow) (distinct, resolved int64, err error) {
	keys := make([]string, 0, len(repos))
	for _, repo := range repos {
		keys = append(keys, repo.key)
	}
	statement := `SELECT toInt64(count()), toInt64(countIf(i.resolved_at IS NOT NULL AND ` + window.upperExpr("i.resolved_at") + `))
FROM (
	SELECT DISTINCT incident_id
	FROM work_graph_deployment_incident_edges
	WHERE toString(org_id) = {org_id:String} AND repo_id IS NOT NULL AND toString(repo_id) IN {ids:Array(String)}
) AS e
INNER JOIN (
	SELECT id, started_at, observed_at, resolved_at
	FROM operational_incidents FINAL
	WHERE org_id = {org_id:String} AND is_deleted = 0
) AS i ON i.id = e.incident_id
WHERE ` + window.timestampExpr("ifNull(i.started_at, i.observed_at)")
	err = readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadTeamIncidentDistinct", statement, orgID, keys, func(row contextpacket.ClickHouseRowScanner) error {
		return row.Scan(&distinct, &resolved)
	}, window.bindings()...)
	return distinct, resolved, err
}
