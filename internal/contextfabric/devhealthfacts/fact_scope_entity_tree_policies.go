package devhealthfacts

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// This file is the traversal of the five entity-tree policies. Every hop
// follows Repository <> Pull request <> Issue <> Project and the ownership
// rows, and none uses an issue's own repository column or a computed team
// attribution:
//
//   - team -> repositories: team_repo_ownership rows, resolved the way ops
//     does (ownedRepositoriesSource), currently valid.
//   - project -> repositories: the project's issues, then work_graph_issue_pr
//     link rows; the repository is the PULL REQUEST's (l.repo_id).
//   - project -> incidents: those repositories, then the deployment-incident
//     edges that name them.
//   - project -> teams: team_project_ownership rows.
//
// Authorization is applied at every hop BEFORE the next read: an unauthorized
// repository is never queried for incidents, and an incident that any
// unauthorized (or unresolvable) repository also names is dropped whole.

// refuseNonCurrentEntityTreeAxis is the boundary check of the current-axis
// only policies; the resolver already refuses a non-current axis at its gate.
func refuseNonCurrentEntityTreeAxis(request contextfabric.FactScopeExpansionRequest) error {
	switch request.TimeContext.Axis {
	case "", contractsv1.ContextFabricTemporalCurrent:
		return nil
	default:
		return fmt.Errorf("devhealthfacts: entity-tree scope policy %q supports the current axis only, got %q: %w",
			request.Policy, request.TimeContext.Axis, contextfabric.ErrFactScopeAxisUnsupported)
	}
}

func (e *ScopeExpander) expandEntityTree(ctx context.Context, orgID string, request contextfabric.FactScopeExpansionRequest, limit int) (contextfabric.FactScopeExpansionResult, error) {
	if err := refuseNonCurrentEntityTreeAxis(request); err != nil {
		return contextfabric.FactScopeExpansionResult{}, err
	}
	switch request.Policy {
	case contextfabric.FactScopePolicyTeamRepositoryOwnershipContinuousIntegration:
		repos, counts, err := e.teamOwnedRepositoryCandidates(ctx, orgID, request.Origins, limit)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		targets, targetBasis, targetRoot, authCounts := authorizeRepositories(request.Principal, repos)
		return contextfabric.FactScopeExpansionResult{Targets: targets, TargetBasis: targetBasis, TargetRoot: targetRoot.first(), TargetRoots: targetRoot, Counts: mergeCounts(counts, authCounts)}, nil

	case contextfabric.FactScopePolicyProjectIssuePullRequestContinuousIntegration,
		contextfabric.FactScopePolicyProjectIssuePullRequestDeployments:
		repos, counts, err := e.projectLinkedRepositories(ctx, orgID, request.Origins, limit)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		targets, targetBasis, targetRoot, authCounts := authorizeRepositories(request.Principal, repos)
		return contextfabric.FactScopeExpansionResult{
			Targets: targets, TargetBasis: targetBasis,
			TargetAttributionSource: repositoryAttributionSources(repos, targets),
			TargetRoot:              targetRoot.first(), TargetRoots: targetRoot, Counts: mergeCounts(counts, authCounts),
		}, nil

	case contextfabric.FactScopePolicyProjectIssuePullRequestIncidents:
		repos, repoCounts, err := e.projectLinkedRepositories(ctx, orgID, request.Origins, maxFactScopeRepositoryFanout)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		authorizedRepos, dropped := splitRepositoriesByAuthorization(request.Principal, repos)
		counts := contextfabric.FactScopeExpansionCounts{
			CandidateCount:                repoCounts.CandidateCount,
			AuthorizationDroppedCount:     dropped,
			MissingNextHopCount:           repoCounts.MissingNextHopCount,
			Truncated:                     repoCounts.Truncated,
			UnknownAttributionSourceCount: repoCounts.UnknownAttributionSourceCount,
		}
		if len(authorizedRepos) == 0 {
			return contextfabric.FactScopeExpansionResult{Counts: counts}, nil
		}
		targets, targetSource, targetRoot, incidentCounts, err := e.incidentsForRepositories(ctx, request.Principal, orgID, authorizedRepos, limit)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		incidentCounts.AuthorizationDroppedCount += dropped
		incidentCounts.MissingNextHopCount += repoCounts.MissingNextHopCount
		incidentCounts.UnknownAttributionSourceCount += repoCounts.UnknownAttributionSourceCount
		incidentCounts.Truncated = incidentCounts.Truncated || repoCounts.Truncated
		return contextfabric.FactScopeExpansionResult{Targets: targets, TargetAttributionSource: targetSource, TargetRoot: targetRoot.first(), TargetRoots: targetRoot, Counts: incidentCounts}, nil

	case contextfabric.FactScopePolicyProjectTeamOwnershipOperationalDeficiencies:
		targets, targetRoot, counts, err := e.projectOwningTeams(ctx, request.Principal, orgID, request.Origins, limit)
		if err != nil {
			return contextfabric.FactScopeExpansionResult{}, err
		}
		return contextfabric.FactScopeExpansionResult{Targets: targets, TargetRoot: targetRoot.first(), TargetRoots: targetRoot, Counts: counts}, nil
	}
	return contextfabric.FactScopeExpansionResult{}, fmt.Errorf("devhealthfacts: scope expander does not implement policy %q", request.Policy)
}

// teamOwnedRepositoryCandidates is team -> owned repositories. One row per
// distinct repository (min(team_id) names the origin among several requested
// teams); the slug is repos.repo, the same authorization key every other
// repository hop uses. The Limit+1 rows are returned untrimmed so the
// resolver's own overflow detection stays independent.
func (e *ScopeExpander) teamOwnedRepositoryCandidates(ctx context.Context, orgID string, origins []contextfabric.SubjectRef, limit int) ([]repositoryCandidate, contextfabric.FactScopeExpansionCounts, error) {
	teamIDs := decodeTeamOriginIDs(origins)
	if len(teamIDs) == 0 {
		return nil, contextfabric.FactScopeExpansionCounts{AmbiguousOriginCount: len(origins)}, nil
	}
	limitPlusOne := limit + 1
	statement := `SELECT own.repo_key, ifNull(any(r.repo), ''), min(own.team_id), arraySort(groupUniqArray(own.team_id))
FROM ` + ownedRepositoriesSource(` AND team_id IN {team_ids:Array(String)}`+ownershipValidityPredicate(factTimeBound{})) + ` AS own
LEFT JOIN (SELECT toString(id) AS repo_key, repo FROM repos FINAL WHERE org_id = {org_id:String}) AS r ON r.repo_key = own.repo_key
GROUP BY own.repo_key
ORDER BY own.repo_key
LIMIT ` + strconv.Itoa(limitPlusOne)
	rows, err := e.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "team_ids", Value: teamIDs},
	})
	if err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	defer rows.Close()

	originsByID := teamOriginsByRawID(origins)
	var candidates []repositoryCandidate
	counts := contextfabric.FactScopeExpansionCounts{}
	for rows.Next() {
		var repoID, repoSlug, originTeamID string
		var originTeamIDs []string
		if err := rows.Scan(&repoID, &repoSlug, &originTeamID, &originTeamIDs); err != nil {
			return nil, contextfabric.FactScopeExpansionCounts{}, err
		}
		if repoID == zeroRepositoryID || repoSlug == "" {
			counts.MissingNextHopCount++
			continue
		}
		counts.CandidateCount++
		candidates = append(candidates, repositoryCandidate{
			repoID: repoID, repoSlug: repoSlug, originRoot: originsByID[originTeamID],
			originRoots: originRootsOf(originsByID[originTeamID], originTeamIDs, originsByID),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	counts.Truncated = counts.CandidateCount+counts.MissingNextHopCount >= limitPlusOne
	return candidates, counts, nil
}

// projectKeyResolutionSubquery resolves a project id AND a project key to the
// project, and counts how many projects each join key resolves to, so an
// ambiguous key is omitted rather than guessed (see projectRepositories for
// the long account of why the count is taken over the whole organization).
const projectKeyResolutionSubquery = `(
  SELECT id, join_key, count() OVER (PARTITION BY join_key) AS key_resolution_count
  FROM (
    SELECT DISTINCT id, provider, join_key FROM (
      SELECT id, provider, id AS join_key FROM projects FINAL WHERE org_id = {org_id:String}
      UNION ALL
      SELECT id, provider, ifNull(project_key, '') AS join_key FROM projects FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != ''
    )
  )
)`

// projectLinkedRepositories is project -> issues -> linked pull request rows
// -> the pull request's repository. The repository is l.repo_id of the link
// row (the pull request's), never work_items.repo_id.
//
// ONE ROW PER DISTINCT REPOSITORY, plus one row (repository ”) that counts
// the project's issues that carry no link row at all, so "issues exist but
// none is linked to a pull request" is a counted missing hop rather than a
// silent empty. The strongest link tier among a repository's links names its
// provenance (native > explicit_text > heuristic); a repository whose links
// all carry a tier outside that vocabulary is counted, never admitted.
func (e *ScopeExpander) projectLinkedRepositories(ctx context.Context, orgID string, origins []contextfabric.SubjectRef, limit int) ([]repositoryCandidate, contextfabric.FactScopeExpansionCounts, error) {
	projectIDs := decodeProjectOriginIDs(origins)
	if len(projectIDs) == 0 {
		return nil, contextfabric.FactScopeExpansionCounts{AmbiguousOriginCount: len(origins)}, nil
	}
	limitPlusOne := limit + 1
	// link_state: 0 the issue has no admitted link row (none at all, or only
	// links whose pull request does not exist), 2 it has one. Only state 2
	// names a repository; a stale link is skipped by the canonical link
	// projection too.
	statement := `SELECT multiIf(ifNull(l.work_item_id, '') = '', 0, 2) AS link_state,
       if(link_state = 2, toString(l.pr_repo_id), '') AS repo_id_str,
       if(link_state = 2, ifNull(r.repo, ''), '') AS repo_slug,
       min(p.id),
       toUInt8(max(l.link_rank)),
       argMax(ifNull(l.provenance, ''), l.link_rank),
       toInt64(uniqExact(w.work_item_id)),
       arraySort(arrayConcat(groupUniqArray(p.id), arraySlice((SELECT groupArray(toString(number % 10)) FROM numbers(800000)), 1, 0)))
FROM work_items AS w FINAL
INNER JOIN ` + projectKeyResolutionSubquery + ` AS p ON p.join_key = w.project_id
LEFT JOIN ` + issueLinkRowsSQL + ` AS l ON l.work_item_id = w.work_item_id
LEFT JOIN repos AS r FINAL ON r.id = l.pr_repo_id AND r.org_id = w.org_id
WHERE w.org_id = {org_id:String} AND p.key_resolution_count = 1 AND p.id IN {project_ids:Array(String)}
  AND ` + workItemIsIssueSQL("w") + `
GROUP BY link_state, repo_id_str, repo_slug
ORDER BY link_state DESC, repo_id_str
LIMIT ` + strconv.Itoa(limitPlusOne)
	rows, err := e.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "project_ids", Value: projectIDs},
	})
	if err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	defer rows.Close()

	originsByID := projectOriginsByRawID(origins)
	var candidates []repositoryCandidate
	counts := contextfabric.FactScopeExpansionCounts{}
	groups := 0
	for rows.Next() {
		var linkState, rank uint8
		var repoID, repoSlug, originProjectID, tier string
		var issues int64
		var originProjectIDs []string
		if err := rows.Scan(&linkState, &repoID, &repoSlug, &originProjectID, &rank, &tier, &issues, &originProjectIDs); err != nil {
			return nil, contextfabric.FactScopeExpansionCounts{}, err
		}
		groups++
		switch {
		case linkState < 2:
			// Issues of the project with no admitted link row: the next hop
			// is missing, and every such issue is counted.
			counts.MissingNextHopCount += int(issues)
		case repoID == zeroRepositoryID, repoSlug == "":
			counts.MissingNextHopCount++
		case rank == 0:
			counts.UnknownAttributionSourceCount++
		default:
			counts.CandidateCount++
			candidates = append(candidates, repositoryCandidate{
				repoID: repoID, repoSlug: repoSlug, attributionSource: tier, originRoot: originsByID[originProjectID],
				originRoots: originRootsOf(originsByID[originProjectID], originProjectIDs, originsByID),
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	counts.Truncated = groups >= limitPlusOne
	return candidates, counts, nil
}

// incidentsForRepositories reads deployment-incident edges ONLY from the
// already authorized repositories, then returns the incidents whose EVERY
// edge repository is one the caller may read. An incident that also names an
// unauthorized or unresolvable repository is dropped whole and counted:
// admitting it would expose a repository the caller was never granted.
func (e *ScopeExpander) incidentsForRepositories(ctx context.Context, principal storage.Principal, orgID string, repos []repositoryCandidate, limit int) ([]contextfabric.SubjectRef, map[string]string, targetRootSet, contextfabric.FactScopeExpansionCounts, error) {
	if len(repos) == 0 {
		return nil, nil, nil, contextfabric.FactScopeExpansionCounts{}, nil
	}
	limitPlusOne := limit + 1
	statement := `SELECT e.incident_id, groupUniqArray(concat(toString(e.repo_id), '|', ifNull(r.repo, '')))
FROM work_graph_deployment_incident_edges AS e FINAL
LEFT JOIN (SELECT id, repo FROM repos FINAL WHERE org_id = {org_id:String}) AS r ON r.id = e.repo_id
WHERE toString(e.org_id) = {org_id:String} AND e.repo_id IS NOT NULL AND e.deployment_id != '' AND e.incident_id NOT IN ('', 'none')
  AND e.incident_id IN (
    SELECT incident_id FROM work_graph_deployment_incident_edges FINAL
    WHERE toString(org_id) = {org_id:String} AND repo_id IS NOT NULL AND deployment_id != '' AND toString(repo_id) IN {repo_ids:Array(String)}
  )
  AND e.incident_id IN (SELECT id FROM operational_incidents FINAL WHERE org_id = {org_id:String} AND is_deleted = 0)
GROUP BY e.incident_id
ORDER BY e.incident_id
LIMIT ` + strconv.Itoa(limitPlusOne)
	rows, err := e.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "repo_ids", Value: repositoryIDs(repos)},
	})
	if err != nil {
		return nil, nil, nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	defer rows.Close()

	byRepo := repoCandidateIndex(repos)
	var targets []contextfabric.SubjectRef
	var targetSource map[string]string
	var targetRoot targetRootSet
	counts := contextfabric.FactScopeExpansionCounts{}
	scanned := 0
	for rows.Next() {
		var incidentID string
		var edgeRepos []string
		if err := rows.Scan(&incidentID, &edgeRepos); err != nil {
			return nil, nil, nil, contextfabric.FactScopeExpansionCounts{}, err
		}
		scanned++
		counts.CandidateCount++
		sort.Strings(edgeRepos)
		allAuthorized := len(edgeRepos) > 0
		var reaching repositoryCandidate
		var reachingRoots []contextfabric.SubjectRef
		for _, edgeRepo := range edgeRepos {
			repoID, slug, _ := strings.Cut(edgeRepo, "|")
			if slug == "" || !authorizedForRepository(principal, slug) {
				allAuthorized = false
				break
			}
			if candidate, ok := byRepo[repoID]; ok {
				if reaching.repoID == "" {
					reaching = candidate
				}
				reachingRoots = append(reachingRoots, candidate.roots()...)
			}
		}
		if !allAuthorized || reaching.repoID == "" {
			counts.AuthorizationDroppedCount++
			continue
		}
		target := contextfabric.SubjectRef{Kind: contextfabric.SubjectIncident, CanonicalID: "incident:" + incidentID, Label: incidentID}
		targets = append(targets, target)
		key := contextfabric.FactSubjectKey(target)
		if reaching.attributionSource != "" {
			if targetSource == nil {
				targetSource = map[string]string{}
			}
			targetSource[key] = reaching.attributionSource
		}
		targetRoot = targetRoot.add(key, reaching.roots())
		targetRoot = targetRoot.add(key, reachingRoots)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	counts.Truncated = scanned >= limitPlusOne
	return targets, targetSource, targetRoot, counts, nil
}

// projectOwningTeams is project -> owning teams through
// team_project_ownership. A team is admitted under the graph's own rule: it
// is visible to a repository-restricted caller only when the caller may read
// at least one repository it owns (a team that owns none carries the "*"
// scope, which admits nobody restricted).
func (e *ScopeExpander) projectOwningTeams(ctx context.Context, principal storage.Principal, orgID string, origins []contextfabric.SubjectRef, limit int) ([]contextfabric.SubjectRef, targetRootSet, contextfabric.FactScopeExpansionCounts, error) {
	projectKeys, bySubject, _ := v2Index(origins, identity.KindProject)
	if len(projectKeys) == 0 {
		return nil, nil, contextfabric.FactScopeExpansionCounts{AmbiguousOriginCount: len(origins)}, nil
	}
	limitPlusOne := limit + 1
	// ONE ROW PER DISTINCT TEAM, before the limit: the ownership join returns a
	// row per (team, source), so limiting its rows would let a few multi-source
	// teams use the whole limit and cut the set with Truncated unset.
	statement := `SELECT p.team_id, min(concat(p.provider, ':', p.id)) AS project_key, ifNull(any(t.name), ''), arraySort(groupUniqArray(concat(p.provider, ':', p.id))) AS project_keys
FROM ` + projectOwnershipJoinSQL(ownershipValidityPredicate(factTimeBound{})) + `
INNER JOIN (SELECT id, name FROM teams FINAL WHERE org_id = {org_id:String} AND ` + devhealthschema.ActiveTeamPredicate("") + `) AS t ON t.id = p.team_id
GROUP BY p.team_id
ORDER BY p.team_id
LIMIT ` + strconv.Itoa(limitPlusOne)
	rows, err := e.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "ids", Value: projectKeys},
	})
	if err != nil {
		return nil, nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	type owningTeam struct {
		id, name, originKey string
		originKeys          []string
	}
	var teams []owningTeam
	seen := map[string]struct{}{}
	for rows.Next() {
		var team owningTeam
		if err := rows.Scan(&team.id, &team.originKey, &team.name, &team.originKeys); err != nil {
			rows.Close()
			return nil, nil, contextfabric.FactScopeExpansionCounts{}, err
		}
		if _, dup := seen[team.id]; dup {
			continue
		}
		seen[team.id] = struct{}{}
		teams = append(teams, team)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	rows.Close()

	counts := contextfabric.FactScopeExpansionCounts{CandidateCount: len(teams)}
	if len(teams) == 0 {
		return nil, nil, counts, nil
	}
	teamIDs := make([]string, 0, len(teams))
	for _, team := range teams {
		teamIDs = append(teamIDs, team.id)
	}
	slugs, err := e.teamOwnedRepositorySlugs(ctx, orgID, teamIDs)
	if err != nil {
		return nil, nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	var targets []contextfabric.SubjectRef
	var targetRoot targetRootSet
	for _, team := range teams {
		// The graph's own convention for a team: its owned repositories'
		// slugs, or the wildcard string when it owns none.
		var scope interface{} = "*"
		if owned := slugs[team.id]; len(owned) > 0 {
			scope = owned
		}
		if !graphrank.AuthorizedAttributes(principal, contextfabric.RequestedScope{}, map[string]interface{}{"authorization_repositories": scope}) {
			counts.AuthorizationDroppedCount++
			continue
		}
		label := team.name
		if label == "" {
			label = team.id
		}
		target := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: teamCanonicalIDPrefix + team.id, Label: label}
		targets = append(targets, target)
		targetRoot = targetRoot.add(contextfabric.FactSubjectKey(target), originRootsOf(bySubject[team.originKey], team.originKeys, bySubject))
	}
	counts.Truncated = len(teams) >= limitPlusOne
	return targets, targetRoot, counts, nil
}

// teamOwnedRepositorySlugs is team -> the slugs (repos.repo) of the
// repositories it currently owns.
func (e *ScopeExpander) teamOwnedRepositorySlugs(ctx context.Context, orgID string, teamIDs []string) (map[string][]string, error) {
	source := ownedRepositoriesSource(` AND team_id IN {team_ids:Array(String)}` + ownershipValidityPredicate(factTimeBound{}))
	slugs := map[string][]string{}
	err := pagedTeamRepoRead(
		teamRepoCursor{teamExpr: "own.team_id", repoExpr: "r.repo", repoType: "String"},
		func(having string, limit int) string {
			return `SELECT own.team_id, ifNull(r.repo, '')
FROM ` + source + ` AS own
LEFT JOIN (SELECT toString(id) AS repo_key, repo FROM repos FINAL WHERE org_id = {org_id:String}) AS r ON r.repo_key = own.repo_key
GROUP BY own.team_id, r.repo
` + having + `
ORDER BY own.team_id, r.repo
LIMIT ` + strconv.Itoa(limit)
		},
		func(statement string, extra []readers.Binding, scan func(contextpacket.ClickHouseRowScanner) error) error {
			bindings := []contextpacket.ClickHouseBinding{
				{Name: "org_id", Value: orgID},
				{Name: "team_ids", Value: teamIDs},
			}
			for _, binding := range extra {
				bindings = append(bindings, contextpacket.ClickHouseBinding{Name: binding.Name, Value: binding.Value})
			}
			rows, err := e.client.Query(ctx, statement, bindings)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				if err := scan(rows); err != nil {
					return err
				}
			}
			return rows.Err()
		},
		func(row contextpacket.ClickHouseRowScanner) (string, string, error) {
			var teamID, slug string
			if err := row.Scan(&teamID, &slug); err != nil {
				return "", "", err
			}
			if slug != "" {
				slugs[teamID] = append(slugs[teamID], slug)
			}
			return teamID, slug, nil
		})
	if err != nil {
		return nil, err
	}
	return slugs, nil
}
